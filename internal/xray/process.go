package xray

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/common"
)

// GetBinaryName returns the Xray binary filename for the current OS and architecture.
func GetBinaryName() string {
	arch := runtime.GOARCH
	if arch == "arm" {
		arch = "arm32"
	}
	return fmt.Sprintf("xray-%s-%s", runtime.GOOS, arch)
}

// GetBinaryPath returns the full path to the Xray binary executable.
func GetBinaryPath() string {
	return config.GetBinFolderPath() + "/" + GetBinaryName()
}

// GetConfigPath returns the path to the Xray configuration file in the binary folder.
func GetConfigPath() string {
	return config.GetBinFolderPath() + "/config.json"
}

// GetGeositePath returns the path to the geosite data file used by Xray.
func GetGeositePath() string {
	return config.GetBinFolderPath() + "/geosite.dat"
}

// GetGeoipPath returns the path to the geoip data file used by Xray.
func GetGeoipPath() string {
	return config.GetBinFolderPath() + "/geoip.dat"
}

// GetIPLimitLogPath returns the path to the IP limit log file.
func GetIPLimitLogPath() string {
	return config.GetLogFolder() + "/3xipl.log"
}

// GetIPLimitBannedLogPath returns the path to the banned IP log file.
func GetIPLimitBannedLogPath() string {
	return config.GetLogFolder() + "/3xipl-banned.log"
}

// GetIPLimitBannedPrevLogPath returns the path to the previous banned IP log file.
func GetIPLimitBannedPrevLogPath() string {
	return config.GetLogFolder() + "/3xipl-banned.prev.log"
}

func getLogPath(key string) (string, error) {
	config, err := os.ReadFile(GetConfigPath())
	if err != nil {
		logger.Warningf("Failed to read configuration file: %s", err)
		return "", err
	}

	jsonConfig := map[string]any{}
	err = json.Unmarshal(config, &jsonConfig)
	if err != nil {
		logger.Warningf("Failed to parse JSON configuration: %s", err)
		return "", err
	}

	if jsonLog, ok := jsonConfig["log"].(map[string]any); ok {
		if logPath, ok := jsonLog[key].(string); ok {
			return logPath, nil
		}
	}
	return "", err
}

// GetAccessLogPath reads the Xray config and returns the access log file path.
func GetAccessLogPath() (string, error) {
	return getLogPath("access")
}

// GetErrorLogPath reads the Xray config and returns the error log file path.
func GetErrorLogPath() (string, error) {
	return getLogPath("error")
}

// stopProcess calls Stop on the given Process instance.
func stopProcess(p *Process) {
	_ = p.Stop()
}

// Process wraps an Xray process instance and provides management methods.
type Process struct {
	*process
}

// NewProcess creates a new Xray process and sets up cleanup on garbage collection.
func NewProcess(xrayConfig *Config) *Process {
	p := &Process{newProcess(xrayConfig)}
	runtime.SetFinalizer(p, stopProcess)
	return p
}

// NewTestProcess creates a new Xray process that uses a specific config file path.
// Used for test runs (e.g. outbound test) so the main config.json is not overwritten.
// The config file at configPath is removed when the process is stopped.
func NewTestProcess(xrayConfig *Config, configPath string) *Process {
	p := &Process{newTestProcess(xrayConfig, configPath)}
	runtime.SetFinalizer(p, stopProcess)
	return p
}

type process struct {
	// mu guards the process lifecycle fields (cmd, done, exitErr) which are
	// written by Start/startCommand and the waitForCommand goroutine while being
	// read concurrently by IsRunning/GetErr/GetResult/Stop from other goroutines
	// (status endpoint, check-xray-running job). Snapshot under the lock, then do
	// any blocking syscall (Wait/Signal/Kill) on the local copy without holding it.
	mu   sync.RWMutex
	cmd  *exec.Cmd
	done chan struct{}

	version string
	apiPort int

	// onlineClients is the cached, sorted union of every local presence source.
	// Xray exact/legacy and auxiliary sidecars are tracked independently so one
	// source can never erase another source's live clients.
	onlineClients []string
	// xrayOnlineClients is the authoritative GetUsersStats snapshot for the
	// running Xray process. It is used only after xrayOnlineExact becomes true.
	xrayOnlineClients []string
	// xrayOnlineExact becomes true after the current core returns its first
	// authoritative connection snapshot. From that point traffic deltas may
	// still maintain per-inbound activity, but cannot resurrect Xray users.
	xrayOnlineExact bool
	// legacyXrayLastOnline records traffic-delta presence only while the running
	// core does not provide an authoritative online snapshot.
	legacyXrayLastOnline map[string]int64
	// auxiliaryLastOnline tracks logical emails reported by non-Xray runtimes,
	// currently the MTProto mtg sidecar. It remains independent from Xray state.
	auxiliaryLastOnline map[string]int64
	// localActiveInbounds is the cached union of Xray and auxiliary inbound
	// activity. Source timestamps stay separate so restarts and pruning cannot
	// erase another runtime's active tags.
	localActiveInbounds        []string
	xrayInboundLastActive      map[string]int64
	auxiliaryInboundLastActive map[string]int64
	// nodeOnlineTrees holds, per direct remote node (keyed by that node's
	// panel-local id), the GUID-keyed online-emails subtree that node
	// reported — its own clients under its panelGuid plus every descendant
	// under theirs. Keying the stored value by GUID (not node id) lets the
	// master attribute a deeply nested client to the node that physically
	// hosts it across a chain (#4983); the outer node-id key is only so a
	// failed probe can drop that whole branch's contribution. NodeTrafficSyncJob
	// populates entries per cron tick and clears them when a probe fails. The
	// mutex guards remote trees and every local source snapshot above.
	nodeOnlineTrees map[int]map[string][]string
	onlineMu        sync.RWMutex

	// onlineUsersSnapshot is the latest successful raw GetUsersStats result.
	// Presence owns the RPC poll; traffic and IP-observation jobs consume this
	// per-process cache so one core snapshot is not fetched three times.
	onlineUsersSnapshot      []OnlineUser
	onlineUsersSnapshotAt    time.Time
	onlineUsersSnapshotReady bool

	// onlineAPISupport caches whether the running core implements the
	// online-stats RPCs (GetUsersStats). A new process is created on every
	// restart/version switch, so the flag resets to Unknown and is re-probed
	// lazily by the first caller.
	onlineAPISupport atomic.Int32

	config     *Config
	configPath string // if set, use this path instead of GetConfigPath() and remove on Stop
	logWriter  *LogWriter
	exitErr    error
	startTime  time.Time

	intentionalStop atomic.Bool
}

// OnlineAPISupport describes whether the running Xray core implements the
// online-stats API (statsUserOnline + GetUsersStats).
type OnlineAPISupport int32

const (
	// OnlineAPIUnknown means support has not been probed yet for this process.
	OnlineAPIUnknown OnlineAPISupport = iota
	// OnlineAPISupported means the core answered the online-stats RPC.
	OnlineAPISupported
	// OnlineAPIUnsupported means the core returned Unimplemented (older binary).
	OnlineAPIUnsupported
)

// OnlineAPISupport returns the cached online-stats capability of this process.
func (p *process) OnlineAPISupport() OnlineAPISupport {
	return OnlineAPISupport(p.onlineAPISupport.Load())
}

// SetOnlineAPISupport records the probed online-stats capability of this process.
func (p *process) SetOnlineAPISupport(v OnlineAPISupport) {
	p.onlineAPISupport.Store(int32(v))
}

func cloneOnlineUsersSnapshot(users []OnlineUser) []OnlineUser {
	out := make([]OnlineUser, len(users))
	for i := range users {
		out[i] = users[i]
		out[i].IPs = append([]OnlineIP(nil), users[i].IPs...)
	}
	return out
}

// StoreOnlineUsersSnapshot records one successful authoritative core snapshot.
// An empty slice is a valid snapshot and must remain distinguishable from no
// successful poll yet. Production presence uses CommitXrayOnlineSnapshot so the
// canonical and raw views become visible under one mutex acquisition.
func (p *process) StoreOnlineUsersSnapshot(users []OnlineUser, at time.Time) {
	p.onlineMu.Lock()
	p.onlineUsersSnapshot = cloneOnlineUsersSnapshot(users)
	p.onlineUsersSnapshotAt = at
	p.onlineUsersSnapshotReady = true
	p.onlineMu.Unlock()
}

// CachedOnlineUsersSnapshot returns an isolated copy only while the last
// successful snapshot is fresh enough for secondary consumers.
func (p *process) CachedOnlineUsersSnapshot(maxAge time.Duration, now time.Time) ([]OnlineUser, bool) {
	if maxAge <= 0 {
		return nil, false
	}

	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()

	if !p.onlineUsersSnapshotReady {
		return nil, false
	}
	age := now.Sub(p.onlineUsersSnapshotAt)
	if age < 0 {
		age = 0
	}
	if age > maxAge {
		return nil, false
	}
	return cloneOnlineUsersSnapshot(p.onlineUsersSnapshot), true
}

// HasFreshOnlineUsersSnapshot checks cache readiness without cloning the full
// user/IP payload. Traffic polling only needs this freshness gate.
func (p *process) HasFreshOnlineUsersSnapshot(maxAge time.Duration, now time.Time) bool {
	if maxAge <= 0 {
		return false
	}

	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()

	if !p.onlineUsersSnapshotReady {
		return false
	}
	age := now.Sub(p.onlineUsersSnapshotAt)
	if age < 0 {
		age = 0
	}
	return age <= maxAge
}

var (
	xrayGracefulStopTimeout = 5 * time.Second
	xrayForceStopTimeout    = 2 * time.Second
	// OnCrash is called when xray crashes unexpectedly. Set from web layer.
	OnCrash func(err error)
)

// newProcess creates a new internal process struct for Xray.
func newProcess(config *Config) *process {
	return &process{
		version:   "Unknown",
		config:    config,
		logWriter: NewLogWriter(),
		startTime: time.Now(),
	}
}

// newTestProcess creates a process that writes and runs with a specific config path.
func newTestProcess(config *Config, configPath string) *process {
	p := newProcess(config)
	p.configPath = configPath
	return p
}

// IsRunning returns true if the Xray process is currently running.
func (p *process) IsRunning() bool {
	p.mu.RLock()
	cmd, done := p.cmd, p.done
	p.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return false
	}
	// done is closed by the waitForCommand goroutine exactly when cmd.Wait
	// returns, i.e. when the process has exited; it is the race-free signal here
	// (reading cmd.ProcessState would race with that Wait).
	if done != nil {
		select {
		case <-done:
			return false
		default:
		}
	}
	return true
}

// GetErr returns the last error encountered by the Xray process.
func (p *process) GetErr() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exitErr
}

// GetResult returns the last log line or error from the Xray process.
func (p *process) GetResult() string {
	p.mu.RLock()
	exitErr := p.exitErr
	p.mu.RUnlock()
	lastLine := p.logWriter.LastLine()
	if len(lastLine) == 0 && exitErr != nil {
		return exitErr.Error()
	}
	return lastLine
}

// GetXrayVersion returns the version string of the Xray process.
func (p *process) GetXrayVersion() string {
	return p.version
}

// GetAPIPort returns the API port used by the Xray process.
func (p *Process) GetAPIPort() int {
	return p.apiPort
}

// GetConfig returns the configuration used by the Xray process.
func (p *Process) GetConfig() *Config {
	return p.config
}

// SetConfig replaces the stored configuration snapshot after the running
// process has been reconciled with it through the gRPC API (hot apply), so
// later change detection compares against what is actually running.
func (p *Process) SetConfig(config *Config) {
	p.config = config
}

// GetOnlineClients returns the union of every local presence source and
// node-online clients from registered remote panels. Dedupes by email so a
// client connected through multiple runtimes or nodes surfaces once.
func (p *Process) GetOnlineClients() []string {
	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()

	if len(p.nodeOnlineTrees) == 0 {
		// Hot path for single-panel deployments: avoid the map+dedupe
		// work entirely and return the local slice as-is.
		return p.onlineClients
	}

	seen := make(map[string]struct{}, len(p.onlineClients))
	out := make([]string, 0, len(p.onlineClients))
	add := func(emails []string) {
		for _, email := range emails {
			if _, dup := seen[email]; dup {
				continue
			}
			seen[email] = struct{}{}
			out = append(out, email)
		}
	}
	add(p.onlineClients)
	for _, tree := range p.nodeOnlineTrees {
		for _, emails := range tree {
			add(emails)
		}
	}
	return out
}

// GetLocalOnlineClients returns a copy of the deduplicated local union:
// exact/legacy Xray plus auxiliary runtimes such as MTProto.
func (p *Process) GetLocalOnlineClients() []string {
	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()
	if len(p.onlineClients) == 0 {
		return nil
	}
	out := make([]string, len(p.onlineClients))
	copy(out, p.onlineClients)
	return out
}

// GetMergedNodeTrees returns the union of every direct node's reported subtree,
// keyed by the panelGuid of the node that physically hosts each client set.
// Because each child already reports its descendants under their own GUIDs,
// merging the direct children yields the whole tree at any depth (#4983), so a
// client three hops down is attributed to its real node, not the intermediate
// one. GUIDs are globally unique, but a set reported under the same GUID by more
// than one path is deduped per key; empty sets are omitted.
func (p *Process) GetMergedNodeTrees() map[string][]string {
	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()
	if len(p.nodeOnlineTrees) == 0 {
		return map[string][]string{}
	}
	out := make(map[string][]string)
	seen := make(map[string]map[string]struct{})
	for _, tree := range p.nodeOnlineTrees {
		for guid, emails := range tree {
			if guid == "" || len(emails) == 0 {
				continue
			}
			dedup := seen[guid]
			if dedup == nil {
				dedup = make(map[string]struct{}, len(emails))
				seen[guid] = dedup
			}
			for _, email := range emails {
				if _, ok := dedup[email]; ok {
					continue
				}
				dedup[email] = struct{}{}
				out[guid] = append(out[guid], email)
			}
		}
	}
	return out
}

// GetLocalActiveInbounds returns the local union of Xray and auxiliary
// inbound tags that carried traffic within the grace window. Remote-node
// snapshots do not carry this detail.
func (p *Process) GetLocalActiveInbounds() []string {
	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()
	if len(p.localActiveInbounds) == 0 {
		return nil
	}
	out := make([]string, len(p.localActiveInbounds))
	copy(out, p.localActiveInbounds)
	return out
}

// normalizeOnlineEmails returns a stable, deduplicated snapshot.
func normalizeOnlineEmails(emails []string) []string {
	seen := make(map[string]struct{}, len(emails))
	out := make([]string, 0, len(emails))
	for _, raw := range emails {
		email := strings.TrimSpace(raw)
		if email == "" {
			continue
		}
		if _, duplicate := seen[email]; duplicate {
			continue
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}
	sort.Strings(out)
	return out
}

func equalOnlineEmails(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func updateLastSeenEmails(lastSeen map[string]int64, emails []string, now int64) map[string]int64 {
	if lastSeen == nil && len(emails) > 0 {
		lastSeen = make(map[string]int64, len(emails))
	}
	for _, raw := range emails {
		email := strings.TrimSpace(raw)
		if email != "" {
			lastSeen[email] = now
		}
	}
	return lastSeen
}

func pruneLastSeen(lastSeen map[string]int64, now, graceMs int64) {
	for key, seenAt := range lastSeen {
		if graceMs <= 0 || now-seenAt >= graceMs {
			delete(lastSeen, key)
		}
	}
}

func (p *Process) rebuildLocalOnlineLocked() bool {
	seen := make(map[string]struct{})
	out := make([]string, 0, len(p.xrayOnlineClients)+len(p.legacyXrayLastOnline)+len(p.auxiliaryLastOnline))
	add := func(email string) {
		email = strings.TrimSpace(email)
		if email == "" {
			return
		}
		if _, exists := seen[email]; exists {
			return
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}

	if p.xrayOnlineExact {
		for _, email := range p.xrayOnlineClients {
			add(email)
		}
	} else {
		for email := range p.legacyXrayLastOnline {
			add(email)
		}
	}
	for email := range p.auxiliaryLastOnline {
		add(email)
	}
	sort.Strings(out)

	if equalOnlineEmails(p.onlineClients, out) {
		return false
	}
	p.onlineClients = out
	return true
}

func updateLastSeenTags(lastSeen map[string]int64, tags []string, now int64) map[string]int64 {
	if lastSeen == nil && len(tags) > 0 {
		lastSeen = make(map[string]int64, len(tags))
	}
	for _, raw := range tags {
		tag := strings.TrimSpace(raw)
		if tag != "" {
			lastSeen[tag] = now
		}
	}
	return lastSeen
}

func (p *Process) rebuildActiveInboundsLocked() {
	seen := make(map[string]struct{}, len(p.xrayInboundLastActive)+len(p.auxiliaryInboundLastActive))
	active := make([]string, 0, len(p.xrayInboundLastActive)+len(p.auxiliaryInboundLastActive))
	add := func(tag string) {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			return
		}
		if _, exists := seen[tag]; exists {
			return
		}
		seen[tag] = struct{}{}
		active = append(active, tag)
	}
	for tag := range p.xrayInboundLastActive {
		add(tag)
	}
	for tag := range p.auxiliaryInboundLastActive {
		add(tag)
	}
	sort.Strings(active)
	p.localActiveInbounds = active
}

func (p *Process) replaceXrayOnlineSnapshotLocked(next []string, now, graceMs int64) bool {
	p.xrayOnlineExact = true
	p.xrayOnlineClients = next
	p.legacyXrayLastOnline = nil
	pruneLastSeen(p.auxiliaryLastOnline, now, graceMs)
	pruneLastSeen(p.xrayInboundLastActive, now, graceMs)
	pruneLastSeen(p.auxiliaryInboundLastActive, now, graceMs)
	p.rebuildActiveInboundsLocked()
	return p.rebuildLocalOnlineLocked()
}

// ReplaceXrayOnlineSnapshot installs the core's authoritative Xray presence
// snapshot while preserving clients reported by auxiliary runtimes.
func (p *Process) ReplaceXrayOnlineSnapshot(emails []string, now, graceMs int64) bool {
	next := normalizeOnlineEmails(emails)

	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()

	return p.replaceXrayOnlineSnapshotLocked(next, now, graceMs)
}

// CommitXrayOnlineSnapshot atomically installs the canonical exact-presence set
// and the raw user/IP snapshot consumed by secondary jobs. Both views share
// onlineMu, so readers cannot observe a canonical tick without its matching raw
// snapshot or vice versa.
func (p *Process) CommitXrayOnlineSnapshot(
	emails []string,
	users []OnlineUser,
	at time.Time,
	now, graceMs int64,
) bool {
	next := normalizeOnlineEmails(emails)
	raw := cloneOnlineUsersSnapshot(users)

	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()

	changed := p.replaceXrayOnlineSnapshotLocked(next, now, graceMs)
	p.onlineUsersSnapshot = raw
	p.onlineUsersSnapshotAt = at
	p.onlineUsersSnapshotReady = true
	return changed
}

// ClearXrayOnlineSnapshot clears only Xray-owned presence and invalidates the
// raw snapshot cache while preserving auxiliary runtimes such as MTProto.
func (p *Process) ClearXrayOnlineSnapshot(now, graceMs int64) bool {
	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()

	changed := p.replaceXrayOnlineSnapshotLocked([]string{}, now, graceMs)
	p.onlineUsersSnapshot = nil
	p.onlineUsersSnapshotAt = time.Time{}
	p.onlineUsersSnapshotReady = false
	return changed
}

// XrayOnlineExact reports whether the current process has committed at least
// one authoritative GetUsersStats snapshot.
func (p *Process) XrayOnlineExact() bool {
	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()
	return p.xrayOnlineExact
}

// GetExactXrayOnlineClients returns an isolated exact Xray-only snapshot.
// Auxiliary sidecar clients are deliberately excluded.
func (p *Process) GetExactXrayOnlineClients() []string {
	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()
	if !p.xrayOnlineExact || len(p.xrayOnlineClients) == 0 {
		return []string{}
	}
	out := make([]string, len(p.xrayOnlineClients))
	copy(out, p.xrayOnlineClients)
	return out
}

// FreshExactXrayOnlineClients returns the exact Xray-only set only when the raw
// snapshot from the same atomic commit remains fresh.
func (p *Process) FreshExactXrayOnlineClients(
	maxAge time.Duration,
	now time.Time,
) ([]string, bool) {
	if maxAge <= 0 {
		return nil, false
	}

	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()

	if !p.xrayOnlineExact || !p.onlineUsersSnapshotReady {
		return nil, false
	}
	age := now.Sub(p.onlineUsersSnapshotAt)
	if age < 0 {
		age = 0
	}
	if age > maxAge {
		return nil, false
	}
	out := make([]string, len(p.xrayOnlineClients))
	copy(out, p.xrayOnlineClients)
	return out, true
}

// RefreshLegacyXrayOnline records traffic-delta Xray presence for cores without
// GetUsersStats. Once exact mode is active, email deltas are ignored, but Xray
// inbound activity and auxiliary expiry are still maintained.
func (p *Process) RefreshLegacyXrayOnline(activeEmails, activeInboundTags []string, now, graceMs int64) bool {
	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()

	if !p.xrayOnlineExact {
		p.legacyXrayLastOnline = updateLastSeenEmails(p.legacyXrayLastOnline, activeEmails, now)
		pruneLastSeen(p.legacyXrayLastOnline, now, graceMs)
	} else {
		p.legacyXrayLastOnline = nil
	}
	p.xrayInboundLastActive = updateLastSeenTags(p.xrayInboundLastActive, activeInboundTags, now)
	pruneLastSeen(p.xrayInboundLastActive, now, graceMs)
	pruneLastSeen(p.auxiliaryLastOnline, now, graceMs)
	pruneLastSeen(p.auxiliaryInboundLastActive, now, graceMs)
	p.rebuildActiveInboundsLocked()
	return p.rebuildLocalOnlineLocked()
}

// RefreshAuxiliaryOnline records logical emails reported by non-Xray runtimes,
// currently MTProto's mtg sidecar. It never changes the authoritative Xray set.
func (p *Process) RefreshAuxiliaryOnline(activeEmails, activeInboundTags []string, now, graceMs int64) bool {
	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()

	p.auxiliaryLastOnline = updateLastSeenEmails(p.auxiliaryLastOnline, activeEmails, now)
	p.auxiliaryInboundLastActive = updateLastSeenTags(p.auxiliaryInboundLastActive, activeInboundTags, now)
	pruneLastSeen(p.auxiliaryLastOnline, now, graceMs)
	pruneLastSeen(p.auxiliaryInboundLastActive, now, graceMs)
	if !p.xrayOnlineExact {
		pruneLastSeen(p.legacyXrayLastOnline, now, graceMs)
	}
	pruneLastSeen(p.xrayInboundLastActive, now, graceMs)
	p.rebuildActiveInboundsLocked()
	return p.rebuildLocalOnlineLocked()
}

// PruneLocalPresence expires stale grace-based sources without adding activity.
// Exact Xray users remain controlled exclusively by ReplaceXrayOnlineSnapshot.
func (p *Process) PruneLocalPresence(now, graceMs int64) bool {
	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()

	if !p.xrayOnlineExact {
		pruneLastSeen(p.legacyXrayLastOnline, now, graceMs)
	}
	pruneLastSeen(p.auxiliaryLastOnline, now, graceMs)
	pruneLastSeen(p.xrayInboundLastActive, now, graceMs)
	pruneLastSeen(p.auxiliaryInboundLastActive, now, graceMs)
	p.rebuildActiveInboundsLocked()
	return p.rebuildLocalOnlineLocked()
}

// AuxiliaryPresenceSnapshot carries non-Xray presence across an Xray process
// replacement. Timestamps are retained so restore cannot extend a stale lease.
type AuxiliaryPresenceSnapshot struct {
	LastOnline        map[string]int64
	InboundLastActive map[string]int64
}

// SnapshotAuxiliaryPresence returns an isolated copy of auxiliary timestamps.
func (p *Process) SnapshotAuxiliaryPresence() AuxiliaryPresenceSnapshot {
	p.onlineMu.RLock()
	defer p.onlineMu.RUnlock()

	out := AuxiliaryPresenceSnapshot{}
	if len(p.auxiliaryLastOnline) > 0 {
		out.LastOnline = make(map[string]int64, len(p.auxiliaryLastOnline))
		for email, seenAt := range p.auxiliaryLastOnline {
			out.LastOnline[email] = seenAt
		}
	}
	if len(p.auxiliaryInboundLastActive) > 0 {
		out.InboundLastActive = make(map[string]int64, len(p.auxiliaryInboundLastActive))
		for tag, seenAt := range p.auxiliaryInboundLastActive {
			out.InboundLastActive[tag] = seenAt
		}
	}
	return out
}

// RestoreAuxiliaryPresence restores only still-fresh non-Xray entries.
func (p *Process) RestoreAuxiliaryPresence(snapshot AuxiliaryPresenceSnapshot, now, graceMs int64) bool {
	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()

	if len(snapshot.LastOnline) == 0 && len(snapshot.InboundLastActive) == 0 {
		return false
	}
	if len(snapshot.LastOnline) > 0 {
		p.auxiliaryLastOnline = make(map[string]int64, len(snapshot.LastOnline))
		for email, seenAt := range snapshot.LastOnline {
			p.auxiliaryLastOnline[email] = seenAt
		}
	}
	if len(snapshot.InboundLastActive) > 0 {
		p.auxiliaryInboundLastActive = make(map[string]int64, len(snapshot.InboundLastActive))
		for tag, seenAt := range snapshot.InboundLastActive {
			p.auxiliaryInboundLastActive[tag] = seenAt
		}
	}
	pruneLastSeen(p.auxiliaryLastOnline, now, graceMs)
	pruneLastSeen(p.auxiliaryInboundLastActive, now, graceMs)
	p.rebuildActiveInboundsLocked()
	return p.rebuildLocalOnlineLocked()
}

// SetNodeOnlineTree records the GUID-keyed online subtree one direct remote
// node reported (its own clients under its panelGuid plus every descendant
// under theirs). Replaces any previous entry for that node — NodeTrafficSyncJob
// always sends the full subtree per tick.
func (p *Process) SetNodeOnlineTree(nodeID int, tree map[string][]string) {
	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()
	if p.nodeOnlineTrees == nil {
		p.nodeOnlineTrees = map[int]map[string][]string{}
	}
	p.nodeOnlineTrees[nodeID] = tree
}

// ClearNodeOnlineClients drops a direct node's whole subtree contribution.
// Called when a probe fails so a downed node — and everything behind it — doesn't
// keep its clients listed as "online" until the next successful probe.
func (p *Process) ClearNodeOnlineClients(nodeID int) {
	p.onlineMu.Lock()
	defer p.onlineMu.Unlock()
	delete(p.nodeOnlineTrees, nodeID)
}

// GetUptime returns the uptime of the Xray process in seconds.
func (p *Process) GetUptime() uint64 {
	return uint64(time.Since(p.startTime).Seconds())
}

// refreshAPIPort updates the API port from the inbound configs.
func (p *process) refreshAPIPort() {
	for _, inbound := range p.config.InboundConfigs {
		if inbound.Tag == "api" {
			p.apiPort = inbound.Port
			break
		}
	}
}

// refreshVersion updates the version string by running the Xray binary with -version.
func (p *process) refreshVersion() {
	cmd := exec.CommandContext(context.Background(), GetBinaryPath(), "-version")
	data, err := cmd.Output()
	if err != nil {
		p.version = "Unknown"
	} else {
		datas := bytes.Split(data, []byte(" "))
		if len(datas) <= 1 {
			p.version = "Unknown"
		} else {
			p.version = string(datas[1])
		}
	}
}

// Start launches the Xray process with the current configuration.
func (p *process) Start() (err error) {
	if p.IsRunning() {
		return errors.New("xray is already running")
	}

	defer func() {
		if err != nil {
			logger.Error("Failure in running xray-core process: ", err)
			p.setExitErr(err)
		}
	}()

	data, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return common.NewErrorf("Failed to generate XRAY configuration files: %v", err)
	}

	err = os.MkdirAll(config.GetLogFolder(), 0o770)
	if err != nil {
		logger.Warningf("Failed to create log folder: %s", err)
	}

	configPath := GetConfigPath()
	if p.configPath != "" {
		configPath = p.configPath
	}
	err = writeFileAtomic(configPath, data, 0o600)
	if err != nil {
		return common.NewErrorf("Failed to write configuration file: %v", err)
	}

	cmd := exec.CommandContext(context.Background(), GetBinaryPath(), "-c", configPath)
	cmd.Stdout = p.logWriter
	cmd.Stderr = p.logWriter

	err = p.startCommand(cmd)
	if err != nil {
		return err
	}

	p.refreshVersion()
	p.refreshAPIPort()

	return nil
}

// writeFileAtomic writes data to path via a same-directory temp file that is
// permissioned, synced, and renamed into place, so a crash can never leave a
// partial config; the config holds credentials, hence the 0600 perm. After the
// rename the parent directory is fsynced to persist the directory entry. That
// final step is skipped on Windows, where directory fsync is unsupported and
// os.Rename already uses replace-existing semantics.
func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		if err != nil {
			_ = os.Remove(tmpPath)
		}
	}()
	if err = tmp.Chmod(perm); err != nil {
		return err
	}
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = renameFile(tmpPath, path); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		return nil
	}
	dirHandle, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = dirHandle.Sync()
	_ = dirHandle.Close()
	return err
}

var renameFile = os.Rename

func (p *process) startCommand(cmd *exec.Cmd) error {
	p.mu.Lock()
	p.cmd = cmd
	p.done = make(chan struct{})
	p.exitErr = nil
	done := p.done
	p.mu.Unlock()
	p.intentionalStop.Store(false)

	if err := cmd.Start(); err != nil {
		close(done)
		p.mu.Lock()
		p.cmd = nil
		p.mu.Unlock()
		return err
	}

	attachChildLifetime(cmd)

	go p.waitForCommand(cmd, done)
	return nil
}

func (p *process) setExitErr(err error) {
	p.mu.Lock()
	p.exitErr = err
	p.mu.Unlock()
}

func (p *process) waitForCommand(cmd *exec.Cmd, done chan struct{}) {
	defer close(done)

	err := cmd.Wait()
	if err == nil || p.intentionalStop.Load() {
		return
	}

	// On Windows, killing the process results in "exit status 1" which isn't an error for us.
	if runtime.GOOS == "windows" {
		errStr := strings.ToLower(err.Error())
		if strings.Contains(errStr, "exit status 1") {
			p.setExitErr(err)
			return
		}
	}

	logger.Error("Failure in running xray-core:", err)
	p.setExitErr(err)
	if OnCrash != nil {
		OnCrash(err)
	}
}

// Stop terminates the running Xray process.
func (p *process) Stop() error {
	if !p.IsRunning() {
		return errors.New("xray is not running")
	}
	p.intentionalStop.Store(true)

	// Snapshot cmd once, then run the blocking Signal/Kill/Wait on the local copy
	// without holding the lock.
	p.mu.RLock()
	cmd := p.cmd
	p.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return errors.New("xray is not running")
	}

	// Remove temporary config file used for test runs so main config is never touched
	if p.configPath != "" {
		if p.configPath != GetConfigPath() {
			// Check if file exists before removing
			if _, err := os.Stat(p.configPath); err == nil {
				_ = os.Remove(p.configPath)
			}
		}
	}

	if runtime.GOOS == "windows" {
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		return p.waitForExit(xrayForceStopTimeout)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		if errors.Is(err, os.ErrProcessDone) {
			return p.waitForExit(xrayForceStopTimeout)
		}
		return err
	}

	if err := p.waitForExit(xrayGracefulStopTimeout); err == nil {
		return nil
	}

	logger.Warning("xray-core did not stop after SIGTERM, killing process")
	if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return p.waitForExit(xrayForceStopTimeout)
}

func (p *process) waitForExit(timeout time.Duration) error {
	p.mu.RLock()
	done := p.done
	p.mu.RUnlock()
	if done == nil {
		return nil
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
		return nil
	case <-timer.C:
		return common.NewErrorf("timed out waiting for xray-core process to stop after %s", timeout)
	}
}

const (
	crashReportPrefix = "core_crash_"
	crashReportSuffix = ".log"
	maxCrashReports   = 10
)

// writeCrashReport persists a captured xray crash chunk to the log folder
// with nanosecond-precision filename so restart-loop bursts don't overwrite
// each other, and prunes old reports to keep the folder bounded.
func writeCrashReport(m []byte) error {
	dir := config.GetLogFolder()
	if err := os.MkdirAll(dir, 0o770); err != nil {
		return err
	}
	pruneOldCrashReports(dir, maxCrashReports-1)
	name := crashReportPrefix + time.Now().Format("20060102_150405_000000000") + crashReportSuffix
	return os.WriteFile(filepath.Join(dir, name), m, 0o640)
}

func pruneOldCrashReports(dir string, keep int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var reports []string
	for _, e := range entries {
		n := e.Name()
		if !e.IsDir() && strings.HasPrefix(n, crashReportPrefix) && strings.HasSuffix(n, crashReportSuffix) {
			reports = append(reports, n)
		}
	}
	if len(reports) <= keep {
		return
	}
	sort.Strings(reports)
	for _, old := range reports[:len(reports)-keep] {
		_ = os.Remove(filepath.Join(dir, old))
	}
}
