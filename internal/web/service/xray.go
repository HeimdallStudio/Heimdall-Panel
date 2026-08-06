package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/frontmux"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/util/json_util"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"go.uber.org/atomic"
)

var (
	p                     *xray.Process
	lock                  sync.Mutex
	onlineUsersPollMu     sync.Mutex
	isNeedXrayRestart     atomic.Bool   // Indicates that restart was requested for Xray
	xrayRestartGeneration atomic.Uint64 // Monotonic generation of restart/reconcile requests
	isManuallyStopped     atomic.Bool   // Indicates that Xray was stopped manually from the panel
	result                string
	frontMuxManager       = frontmux.NewManager(func(err error) {
		logger.Warning("shared-port frontmux: ", err)
	})
)

const onlineUsersSnapshotMaxAge = 3 * time.Second

// OnlineUsersPoll binds one raw GetUsersStats result to the exact Xray process
// that produced it. The process identity is intentionally private; callers may
// inspect Users but can only commit the poll through XrayService.
type OnlineUsersPoll struct {
	process *xray.Process
	Users   []xray.OnlineUser
}

// markXrayRestartNeeded records a new restart/reconcile request. The generation
// lets an in-flight restart distinguish an older request it has covered from a
// newer request that arrived while it was building or applying its config.
func markXrayRestartNeeded() {
	xrayRestartGeneration.Inc()
	isNeedXrayRestart.Store(true)
}

// finishXrayRestart preserves newer requests that arrived during the operation.
// A failed operation always schedules another attempt with a new generation.
func finishXrayRestart(startGeneration uint64, clearPending bool, err error) error {
	if err != nil {
		markXrayRestartNeeded()
		return err
	}

	if clearPending && xrayRestartGeneration.Load() == startGeneration {
		isNeedXrayRestart.Store(false)
	}

	return nil
}

// XrayService provides business logic for Xray process management.
// It handles starting, stopping, restarting Xray, and managing its configuration.
type XrayService struct {
	inboundService InboundService
	settingService SettingService
	nodeService    NodeService
	xrayAPI        xray.XrayAPI
}

// IsXrayRunning checks if the Xray process is currently running.
func (s *XrayService) IsXrayRunning() bool {
	return p != nil && p.IsRunning()
}

// XrayProcess returns the current Xray process instance (may be nil when Xray
// is not running). It exposes the package-level process to callers outside this
// package (e.g. the tgbot subpackage) without changing access semantics.
func XrayProcess() *xray.Process {
	return p
}

// GetXrayErr returns the error from the Xray process, if any.
func (s *XrayService) GetXrayErr() error {
	if p == nil {
		return nil
	}

	err := p.GetErr()
	if err == nil {
		return nil
	}

	if runtime.GOOS == "windows" && err.Error() == "exit status 1" {
		// exit status 1 on Windows means that Xray process was killed
		// as we kill process to stop in on Windows, this is not an error
		return nil
	}

	return err
}

// GetXrayResult returns the result string from the Xray process.
func (s *XrayService) GetXrayResult() string {
	if result != "" {
		return result
	}
	if s.IsXrayRunning() {
		return ""
	}
	if p == nil {
		return ""
	}

	result = p.GetResult()

	if runtime.GOOS == "windows" && result == "exit status 1" {
		// exit status 1 on Windows means that Xray process was killed
		// as we kill process to stop in on Windows, this is not an error
		return ""
	}

	return result
}

// GetXrayVersion returns the version of the running Xray process.
func (s *XrayService) GetXrayVersion() string {
	if p == nil {
		return "Unknown"
	}
	return p.GetXrayVersion()
}

// RemoveIndex removes an element at the specified index from a slice.
// Returns a new slice with the element removed.
func RemoveIndex(s []any, index int) []any {
	return append(s[:index], s[index+1:]...)
}

// GetXrayConfig retrieves and builds the Xray configuration from settings and inbounds.
func (s *XrayService) GetXrayConfig() (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	err = json.Unmarshal([]byte(templateConfig), xrayConfig)
	if err != nil {
		return nil, err
	}
	xrayConfig.LogConfig = resolveXrayLogPaths(xrayConfig.LogConfig)
	xrayConfig.API = ensureAPIServices(xrayConfig.API)
	xrayConfig.Policy = ensureStatsPolicy(xrayConfig.Policy)
	xrayConfig.RouterConfig = stripDisabledRules(xrayConfig.RouterConfig)
	// Template outbounds authored before the xray-core #6258 XHTTP rename may
	// still carry sessionPlacement/sessionKey; lift them too (same reason as
	// the per-inbound lift below).
	xrayConfig.OutboundConfigs = liftOutboundsXhttpSessionIDKeys(xrayConfig.OutboundConfigs)

	_, _, _ = s.inboundService.AddTraffic(nil, nil)

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	backendAllocator, err := newSharedBackendAllocatorForConfig(xrayConfig, inbounds)
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		if inbound.NodeID != nil {
			continue
		}
		if inbound.Protocol == model.MTProto {
			continue
		}
		settings := map[string]any{}
		_ = json.Unmarshal([]byte(inbound.Settings), &settings)

		dbClients, listErr := s.inboundService.clientService.ListForInbound(nil, inbound.Id)
		if listErr != nil {
			return nil, listErr
		}

		// Keep canonical client/client_inbound rows in sync with the same inbound
		// client list used to generate Xray runtime users. This prevents freshly
		// created or attached clients from being emitted as hmstat_* users without
		// a matching client_inbound_traffics resolver row.
		if syncErr := s.inboundService.clientService.SyncInbound(nil, inbound.Id, dbClients); syncErr != nil {
			return nil, syncErr
		}
		if mapErr := s.inboundService.EnsureClientInboundTrafficMappingsForInbound(inbound.Id); mapErr != nil {
			return nil, mapErr
		}

		clientStats := inbound.ClientStats
		enableMap := make(map[string]bool, len(clientStats))
		for _, clientTraffic := range clientStats {
			enableMap[clientTraffic.Email] = clientTraffic.Enable
		}

		var finalClients []any
		var wgPeers []any
		for i := range dbClients {
			c := dbClients[i]
			runtimeEmail := model.RuntimeClientEmailForInbound(inbound, c.Email)
			if runtimeEmail == "" {
				runtimeEmail = c.Email
			}
			if enable, exists := enableMap[c.Email]; exists && !enable {
				logger.Infof("Remove Inbound User %s due to expiration or traffic limit", c.Email)
				continue
			}
			if !c.Enable {
				continue
			}
			flow := c.Flow
			if flow == "xtls-rprx-vision-udp443" {
				flow = "xtls-rprx-vision"
			}
			entry := map[string]any{"email": runtimeEmail}
			switch inbound.Protocol {
			case model.VLESS:
				if c.ID != "" {
					entry["id"] = c.ID
				}
				if flow != "" {
					entry["flow"] = flow
				}
				if c.Reverse != nil {
					entry["reverse"] = c.Reverse
				}
			case model.VMESS:
				if c.ID != "" {
					entry["id"] = c.ID
				}
				if c.Security != "" {
					entry["security"] = c.Security
				}
			case model.Trojan:
				if c.Password != "" {
					entry["password"] = c.Password
				}
				if flow != "" {
					entry["flow"] = flow
				}
			case model.Shadowsocks:
				if c.Password != "" {
					entry["password"] = c.Password
				}
			case model.Hysteria:
				if c.Auth != "" {
					entry["auth"] = c.Auth
				}
			case model.WireGuard:
				wgPeers = append(wgPeers, model.WireguardPeerFromClient(c))
				continue
			}
			finalClients = append(finalClients, entry)
		}

		var mutated bool
		if inbound.Protocol == model.WireGuard {
			delete(settings, "clients")
			if wgPeers == nil {
				wgPeers = []any{}
			}
			settings["peers"] = wgPeers
			mutated = true
		} else {
			_, hadClients := settings["clients"]
			mutated = hadClients || len(finalClients) > 0
			if mutated {
				settings["clients"] = finalClients
			}
		}

		if inboundCanHostFallbacks(inbound) {
			fallbacks, fbErr := s.inboundService.fallbackService.BuildFallbacksJSON(nil, inbound.Id)
			if fbErr != nil {
				return nil, fbErr
			}
			if len(fallbacks) > 0 {
				generic := make([]any, 0, len(fallbacks))
				for _, f := range fallbacks {
					generic = append(generic, f)
				}
				settings["fallbacks"] = generic
				mutated = true
			}
		}

		if mutated {
			modifiedSettings, err := json.MarshalIndent(settings, "", "  ")
			if err != nil {
				return nil, err
			}
			inbound.Settings = string(modifiedSettings)
		}

		var rawRuntimeProfileStream map[string]any
		if len(inbound.StreamSettings) > 0 {
			// Unmarshal stream JSON. Keep an untouched copy for the runtime-profile
			// compiler before stripping panel-only externalProxy metadata from the
			// parent Xray inbound.
			var stream map[string]any
			_ = json.Unmarshal([]byte(inbound.StreamSettings), &stream)
			rawRuntimeProfileStream, _ = cloneMap(stream)

			// Remove the "settings" field under "tlsSettings" and "realitySettings"
			tlsSettings, ok1 := stream["tlsSettings"].(map[string]any)
			realitySettings, ok2 := stream["realitySettings"].(map[string]any)
			if ok1 || ok2 {
				if ok1 {
					delete(tlsSettings, "settings")
				} else if ok2 {
					delete(realitySettings, "settings")
				}
			}

			delete(stream, "externalProxy")

			// HTTPUpgrade keeps Host as a dedicated field. The pinned core rejects
			// Host inside headers, including legacy case variants. Normalize only
			// the generated runtime config; persisted/subscription data stays intact.
			normalizeHTTPUpgradeRuntimeHeaders(stream)

			// finalmask.tcp + REALITY panics Xray-core on the first connection
			// (XTLS/Xray-core#6453). AddInbound/UpdateInbound reject this
			// combination at save time, but a row saved before that guard
			// existed (upgrade, node sync, restored backup, direct DB edit)
			// would still crash Xray on the next restart without this — drop
			// it here too, the same way liftXhttpSessionIDKeys and
			// HealShadowsocksClientMethods heal other legacy data in place.
			if len(finalMaskRealityTcpMasks(stream)) > 0 {
				logger.Warningf("Inbound %q: dropping finalmask, incompatible with REALITY security (crashes Xray-core, see XTLS/Xray-core#6453)", inbound.Tag)
				delete(stream, "finalmask")
			}

			// xray-core v26.6.22 (#6258) renamed the XHTTP session keys and
			// kept no fallback. Lift legacy sessionPlacement/sessionKey onto the
			// new names here so inbounds stored before the rename keep working
			// without the admin re-saving them.
			liftXhttpSessionIDKeys(stream)

			newStream, err := json.MarshalIndent(stream, "", "  ")
			if err != nil {
				return nil, err
			}
			inbound.StreamSettings = string(newStream)
		}

		if inbound.Protocol == model.Shadowsocks {
			if healed, ok := model.HealShadowsocksClientMethods(inbound.Settings); ok {
				inbound.Settings = healed
			}
		}

		topology, topologyErr := compileRuntimeProfileTopology(
			inbound,
			rawRuntimeProfileStream,
			backendAllocator,
		)
		if topologyErr != nil {
			return nil, topologyErr
		}
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, topology.Parent)
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, topology.Synthetic...)
		xrayConfig.SharedPortPlan.Groups = append(
			xrayConfig.SharedPortPlan.Groups,
			topology.SharedPlan.Groups...,
		)
	}

	// Merge subscription-derived outbounds (if any) into the final outbounds array.
	// These are additive: each subscription is placed before or after the template
	// outbounds based on its Prepend flag, ordered by Priority. Tags assigned by the
	// subscription service are kept stable across refreshes so that balancers and
	// routing rules continue to work.
	subSvc := &OutboundSubscriptionService{}
	if prepend, appendList, err := subSvc.activeOutboundsSplit(); err == nil && (len(prepend) > 0 || len(appendList) > 0) {
		mergeSubscriptionOutbounds(xrayConfig, prepend, appendList)
	}

	// Route opted-in local mtproto inbounds through the core's router. Each one
	// gets a loopback SOCKS bridge — tagged with the inbound's own tag so it is
	// matchable in routing rules — that its mtg sidecar dials Telegram through.
	// Done after the subscription merge so a selected subscription outbound (or
	// balancer) is a valid rule target.
	for i := range inbounds {
		inbound := inbounds[i]
		if inbound.Protocol != model.MTProto || !inbound.Enable || inbound.NodeID != nil {
			continue
		}
		injectMtprotoEgress(xrayConfig, inbound)
	}

	// Wire the panel's own HTTP traffic through the configured outbound, after
	// the subscription merge so subscription outbound tags are valid targets.
	if egressTag, err := s.settingService.GetPanelOutbound(); err != nil {
		logger.Warning("read panelOutbound setting failed:", err)
	} else if egressTag != "" {
		injectPanelEgress(xrayConfig, egressTag)
	}

	nodes, err := s.nodeService.GetAll()
	if err != nil {
		logger.Warning("read nodes for egress injection failed:", err)
	} else {
		injectNodeEgresses(xrayConfig, nodes)
	}

	if err := validateRuntimeProfileBindings(xrayConfig); err != nil {
		return nil, err
	}
	if err := validateSharedPortPlanAgainstXrayConfig(xrayConfig); err != nil {
		return nil, err
	}
	xrayConfig.SharedPortPlan = xrayConfig.SharedPortPlan.Canonical()

	return xrayConfig, nil
}

// PanelEgressInboundTag is the tag of the loopback SOCKS inbound injected into
// the generated config when a panel outbound is configured. The panel's own
// HTTP clients dial through it to egress via the chosen outbound.
const PanelEgressInboundTag = "panel-egress"

// panelEgressBasePort is the first port tried for the egress bridge; ports
// already taken by other inbounds in the generated config are skipped.
const panelEgressBasePort = 62790

// injectPanelEgress appends a loopback SOCKS inbound to the generated config
// and prepends a routing rule sending it to outboundTag. Both live only in the
// generated config — the stored template is never modified — and both are
// hot-appliable, so changing the panel outbound never restarts the core.
func injectPanelEgress(cfg *xray.Config, outboundTag string) {
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag == PanelEgressInboundTag {
			logger.Warning("panel egress: inbound tag [", PanelEgressInboundTag, "] already exists, skipping injection")
			return
		}
	}

	// The rule must exist before the inbound takes traffic, otherwise the
	// bridge would silently egress through the default outbound instead.
	routing := map[string]any{}
	if len(cfg.RouterConfig) > 0 {
		if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
			logger.Warning("panel egress: routing section is unparsable, skipping injection:", err)
			return
		}
	}
	rules, _ := routing["rules"].([]any)
	rule := map[string]any{
		"type":       "field",
		"inboundTag": []any{PanelEgressInboundTag},
	}
	// The configured tag may name a routing balancer instead of a concrete
	// outbound. A field rule can target either, so emit the matching key —
	// balancerTag load-balances the panel's own traffic across the balancer's
	// outbounds, while a plain outbound tag keeps the original behavior.
	if routingTagIsBalancer(routing, outboundTag) {
		rule["balancerTag"] = outboundTag
	} else {
		rule["outboundTag"] = outboundTag
	}
	routing["rules"] = append([]any{rule}, rules...)
	newRouting, err := json.Marshal(routing)
	if err != nil {
		logger.Warning("panel egress: failed to rebuild routing section, skipping injection:", err)
		return
	}
	cfg.RouterConfig = json_util.RawMessage(newRouting)

	used := make(map[int]struct{}, len(cfg.InboundConfigs))
	for i := range cfg.InboundConfigs {
		used[cfg.InboundConfigs[i].Port] = struct{}{}
	}
	port := panelEgressBasePort
	for {
		if _, taken := used[port]; !taken {
			break
		}
		port++
	}

	cfg.InboundConfigs = append(cfg.InboundConfigs, xray.InboundConfig{
		Listen:   json_util.RawMessage(`"127.0.0.1"`),
		Port:     port,
		Protocol: "socks",
		Settings: json_util.RawMessage(`{"auth":"noauth","udp":false}`),
		Tag:      PanelEgressInboundTag,
	})
}

// NodeEgressInboundTag returns the loopback SOCKS inbound tag for a given node.
func NodeEgressInboundTag(nodeID int) string {
	return fmt.Sprintf("node-egress-%d", nodeID)
}

// nodeEgressBasePort is the first port tried for node egress bridges.
const nodeEgressBasePort = 62800

// injectNodeEgresses appends a loopback SOCKS inbound per enabled node that has
// an OutboundTag, and prepends a routing rule sending that inbound's traffic to
// the selected outbound tag. These bridges are hot-appliable.
func injectNodeEgresses(cfg *xray.Config, nodes []*model.Node) {
	routing := map[string]any{}
	if len(cfg.RouterConfig) > 0 {
		if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
			logger.Warning("node egress: routing section is unparsable, skipping injection:", err)
			return
		}
	}

	used := make(map[int]struct{}, len(cfg.InboundConfigs))
	usedTags := make(map[string]struct{}, len(cfg.InboundConfigs))
	for i := range cfg.InboundConfigs {
		used[cfg.InboundConfigs[i].Port] = struct{}{}
		usedTags[cfg.InboundConfigs[i].Tag] = struct{}{}
	}

	rules, _ := routing["rules"].([]any)
	newRules := make([]any, 0)

	for _, n := range nodes {
		if !n.Enable || n.OutboundTag == "" {
			continue
		}
		tag := NodeEgressInboundTag(n.Id)
		if _, exists := usedTags[tag]; exists {
			logger.Warning("node egress: inbound tag [", tag, "] already exists, skipping")
			continue
		}
		usedTags[tag] = struct{}{}

		rule := map[string]any{
			"type":       "field",
			"inboundTag": []any{tag},
		}
		if routingTagIsBalancer(routing, n.OutboundTag) {
			rule["balancerTag"] = n.OutboundTag
		} else {
			rule["outboundTag"] = n.OutboundTag
		}
		newRules = append(newRules, rule)

		port := nodeEgressBasePort + n.Id
		for {
			if _, taken := used[port]; !taken {
				break
			}
			port++
		}
		used[port] = struct{}{}

		cfg.InboundConfigs = append(cfg.InboundConfigs, xray.InboundConfig{
			Listen:   json_util.RawMessage(`"127.0.0.1"`),
			Port:     port,
			Protocol: "socks",
			Settings: json_util.RawMessage(`{"auth":"noauth","udp":false}`),
			Tag:      tag,
		})
	}

	if len(newRules) == 0 {
		return
	}
	routing["rules"] = append(newRules, rules...)
	newRouting, err := json.Marshal(routing)
	if err != nil {
		logger.Warning("node egress: failed to rebuild routing section, skipping injection:", err)
		return
	}
	cfg.RouterConfig = json_util.RawMessage(newRouting)
}

// routingTagIsBalancer reports whether tag names a balancer in the parsed
// routing section. The panel-egress rule targets a balancer via balancerTag and
// a concrete outbound via outboundTag, so the caller picks the key from this.
func routingTagIsBalancer(routing map[string]any, tag string) bool {
	if tag == "" {
		return false
	}
	balancers, ok := routing["balancers"].([]any)
	if !ok {
		return false
	}
	for _, b := range balancers {
		bm, ok := b.(map[string]any)
		if !ok {
			continue
		}
		if t, ok := bm["tag"].(string); ok && t == tag {
			return true
		}
	}
	return false
}

// mtprotoEgressSocksSettings is the loopback SOCKS server a routed mtproto
// inbound exposes for its mtg sidecar to dial Telegram through. mtg makes plain
// TCP connections, so UDP is left off (matching the panel egress bridge).
const mtprotoEgressSocksSettings = `{"auth":"noauth","udp":false}`

// injectMtprotoEgress wires one routed mtproto inbound into the generated
// config: it appends a loopback SOCKS inbound (tagged with the inbound's own tag,
// on the egress port persisted in settings) and, when an outbound is selected,
// prepends a routing rule sending that tag to it. Both live only in the generated
// config — the stored template is untouched — and both are hot-appliable, so
// toggling routing never forces a full Xray restart. Mirrors injectPanelEgress.
func injectMtprotoEgress(cfg *xray.Config, inbound *model.Inbound) {
	var parsed struct {
		RouteThroughXray bool   `json:"routeThroughXray"`
		RouteXrayPort    int    `json:"routeXrayPort"`
		OutboundTag      string `json:"outboundTag"`
	}
	if err := json.Unmarshal([]byte(inbound.Settings), &parsed); err != nil {
		return
	}
	if !parsed.RouteThroughXray || parsed.RouteXrayPort <= 0 || inbound.Tag == "" {
		return
	}
	tag := inbound.Tag
	for i := range cfg.InboundConfigs {
		if cfg.InboundConfigs[i].Tag == tag {
			logger.Warning("mtproto egress: inbound tag [", tag, "] already present in generated config, skipping bridge")
			return
		}
	}

	if parsed.OutboundTag != "" {
		routing := map[string]any{}
		parseOK := true
		if len(cfg.RouterConfig) > 0 {
			if err := json.Unmarshal(cfg.RouterConfig, &routing); err != nil {
				logger.Warning("mtproto egress: routing section is unparsable, skipping rule:", err)
				parseOK = false
			}
		}
		if parseOK {
			rules, _ := routing["rules"].([]any)
			rule := map[string]any{
				"type":       "field",
				"inboundTag": []any{tag},
			}
			if routingTagIsBalancer(routing, parsed.OutboundTag) {
				rule["balancerTag"] = parsed.OutboundTag
			} else {
				rule["outboundTag"] = parsed.OutboundTag
			}
			routing["rules"] = append([]any{rule}, rules...)
			if newRouting, err := json.Marshal(routing); err == nil {
				cfg.RouterConfig = json_util.RawMessage(newRouting)
			} else {
				logger.Warning("mtproto egress: failed to rebuild routing section, skipping rule:", err)
			}
		}
	}

	cfg.InboundConfigs = append(cfg.InboundConfigs, xray.InboundConfig{
		Listen:   json_util.RawMessage(`"127.0.0.1"`),
		Port:     parsed.RouteXrayPort,
		Protocol: "socks",
		Settings: json_util.RawMessage(mtprotoEgressSocksSettings),
		Tag:      tag,
	})
}

// mergeSubscriptionOutbounds appends the subscription outbounds to the
// OutboundConfigs array of the xray config. It works on the already-unmarshaled
// template so that manually configured outbounds are never overwritten.
//
// Safety: if we cannot parse the template's outbounds array, we leave
// OutboundConfigs exactly as it came from the template (we do not inject
// subscription outbounds). This prevents us from accidentally dropping the
// user's manually configured outbounds when the template is in a weird state.
func mergeSubscriptionOutbounds(cfg *xray.Config, prepend, appendList []any) {
	if len(prepend) == 0 && len(appendList) == 0 {
		return
	}
	var templateOutbounds []any
	if len(cfg.OutboundConfigs) > 0 {
		if err := json.Unmarshal(cfg.OutboundConfigs, &templateOutbounds); err != nil {
			// Corrupt template outbounds — do not touch the field at all.
			// The user will see problems on Xray start / next save.
			return
		}
	}
	var merged []any
	merged = append(merged, prepend...)
	merged = append(merged, templateOutbounds...)
	merged = append(merged, appendList...)
	combined, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return
	}
	cfg.OutboundConfigs = json_util.RawMessage(combined)
}

// ensureAPIServices guarantees the gRPC services the panel depends on are
// listed in the generated config's api block: HandlerService and StatsService
// have always been required for inbound/user management and traffic polling,
// and RoutingService enables hot routing reload on templates saved before it
// was added to the default template. The stored template itself is not
// modified — only the generated runtime config.
func ensureAPIServices(api json_util.RawMessage) json_util.RawMessage {
	if len(api) == 0 {
		// No api block means the panel's API integration is deliberately
		// disabled; don't resurrect it behind the user's back.
		return api
	}
	var parsed map[string]any
	if err := json.Unmarshal(api, &parsed); err != nil {
		return api
	}
	services, _ := parsed["services"].([]any)
	have := make(map[string]bool, len(services))
	for _, svc := range services {
		if name, ok := svc.(string); ok {
			have[name] = true
		}
	}
	added := false
	for _, name := range []string{"HandlerService", "StatsService", "RoutingService"} {
		if !have[name] {
			services = append(services, name)
			added = true
		}
	}
	if !added {
		return api
	}
	parsed["services"] = services
	out, err := json.Marshal(parsed)
	if err != nil {
		return api
	}
	return out
}

// ensureStatsPolicy guarantees every policy level in the generated config has
// statsUserOnline enabled, so the core tracks per-email online IPs for the
// panel's online view and access-log-free IP limiting. Generated clients carry
// no explicit level, so level "0" is created when absent. The flag is panel
// infrastructure and is forced on even over an explicit false in the template,
// same as the api services above. An entirely missing or unparsable policy
// block is left alone; the stored template itself is never modified — only the
// generated runtime config.
func ensureStatsPolicy(policy json_util.RawMessage) json_util.RawMessage {
	if len(policy) == 0 {
		return policy
	}
	var parsed map[string]any
	if err := json.Unmarshal(policy, &parsed); err != nil {
		return policy
	}
	levels, _ := parsed["levels"].(map[string]any)
	if levels == nil {
		levels = make(map[string]any)
	}
	if _, ok := levels["0"]; !ok {
		levels["0"] = map[string]any{}
	}
	changed := false
	for _, raw := range levels {
		level, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if enabled, ok := level["statsUserOnline"].(bool); !ok || !enabled {
			level["statsUserOnline"] = true
			changed = true
		}
	}
	if !changed {
		return policy
	}
	parsed["levels"] = levels
	out, err := json.Marshal(parsed)
	if err != nil {
		return policy
	}
	return out
}

func resolveXrayLogPaths(logCfg json_util.RawMessage) json_util.RawMessage {
	if len(logCfg) == 0 {
		return logCfg
	}
	var parsed map[string]any
	if err := json.Unmarshal(logCfg, &parsed); err != nil {
		return logCfg
	}
	changed := false
	for _, key := range []string{"access", "error"} {
		v, ok := parsed[key].(string)
		if !ok {
			continue
		}
		trimmed := strings.TrimSpace(v)
		if trimmed == "" || strings.EqualFold(trimmed, "none") {
			continue
		}
		base := path.Base(filepath.ToSlash(trimmed))
		if base == "" || base == "." || base == ".." || base == "/" {
			continue
		}
		confined := filepath.Join(config.GetLogFolder(), base)
		if confined == trimmed {
			continue
		}
		parsed[key] = confined
		changed = true
	}
	if !changed {
		return logCfg
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return logCfg
	}
	return out
}

// stripDisabledRules removes routing rules marked `enabled: false` from the
// generated runtime config and strips the panel-only `enabled` key from the
// rest, since xray-core has no such field. The internal api rule is always
// kept (see isApiRule) so traffic stats can't be toggled off. The stored
// template is untouched — only the generated config is filtered.
func stripDisabledRules(routerCfg json_util.RawMessage) json_util.RawMessage {
	if len(routerCfg) == 0 {
		return routerCfg
	}
	var parsed map[string]any
	if err := json.Unmarshal(routerCfg, &parsed); err != nil {
		return routerCfg
	}
	rules, ok := parsed["rules"].([]any)
	if !ok || len(rules) == 0 {
		return routerCfg
	}

	var activeRules []any
	changed := false
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok {
			activeRules = append(activeRules, rawRule)
			continue
		}

		if enabledRaw, exists := rule["enabled"]; exists {
			// The internal api rule carries traffic stats and must never be
			// dropped, even if it was somehow marked disabled.
			enabled, ok := enabledRaw.(bool)
			if ok && !enabled && !isApiRule(rule) {
				changed = true
				continue
			}
			delete(rule, "enabled")
			changed = true
		}
		activeRules = append(activeRules, rule)
	}

	if !changed {
		return routerCfg
	}

	parsed["rules"] = activeRules
	out, err := json.Marshal(parsed)
	if err != nil {
		return routerCfg
	}
	return out
}

// GetXrayTraffic fetches the current traffic statistics from the running Xray process.
func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, []*xray.ClientTraffic, error) {
	if !s.IsXrayRunning() {
		err := errors.New("xray is not running")
		logger.Debug("Attempted to fetch Xray traffic, but Xray is not running:", err)
		return nil, nil, err
	}
	apiPort := p.GetAPIPort()
	if err := s.xrayAPI.Init(apiPort); err != nil {
		logger.Debug("Failed to initialize Xray API:", err)
		return nil, nil, err
	}
	defer s.xrayAPI.Close()

	traffic, clientTraffic, err := s.xrayAPI.GetTraffic()
	if err != nil {
		logger.Debug("Failed to fetch Xray traffic:", err)
		return nil, nil, err
	}
	return traffic, clientTraffic, nil
}

// GetOnlineUsers performs the authoritative online-stats RPC. The one-second
// ClientPresenceJob is the production owner of this direct poll. The returned
// poll remains bound to the exact process that answered the RPC.
func (s *XrayService) GetOnlineUsers() (OnlineUsersPoll, bool, error) {
	onlineUsersPollMu.Lock()
	defer onlineUsersPollMu.Unlock()

	lock.Lock()
	proc := p
	lock.Unlock()
	if proc == nil || !proc.IsRunning() {
		return OnlineUsersPoll{}, false, nil
	}
	if proc.OnlineAPISupport() == xray.OnlineAPIUnsupported {
		return OnlineUsersPoll{}, false, nil
	}
	if err := s.xrayAPI.Init(proc.GetAPIPort()); err != nil {
		logger.Debug("Failed to initialize Xray API:", err)
		return OnlineUsersPoll{}, false, err
	}
	defer s.xrayAPI.Close()

	users, err := s.xrayAPI.GetOnlineUsers()
	if err != nil {
		if xray.IsUnimplementedErr(err) {
			proc.SetOnlineAPISupport(xray.OnlineAPIUnsupported)
			logger.Info("xray core does not support the online-stats API; falling back to traffic-delta onlines")
			return OnlineUsersPoll{}, false, nil
		}
		logger.Debug("Failed to fetch Xray online users:", err)
		return OnlineUsersPoll{}, false, err
	}
	if proc.OnlineAPISupport() == xray.OnlineAPIUnknown {
		proc.SetOnlineAPISupport(xray.OnlineAPISupported)
		logger.Info("xray online-stats API detected; connection-aware online clients enabled")
	}

	lock.Lock()
	valid := p == proc && proc.IsRunning()
	lock.Unlock()
	if !valid {
		return OnlineUsersPoll{}, false, nil
	}
	return OnlineUsersPoll{
		process: proc,
		Users:   users,
	}, true, nil
}

func commitOnlineUsersPoll(
	current, polled *xray.Process,
	running bool,
	logicalEmails []string,
	users []xray.OnlineUser,
	at time.Time,
) (changed, committed bool) {
	if current == nil || polled == nil || current != polled || !running {
		return false, false
	}
	changed = polled.CommitXrayOnlineSnapshot(
		logicalEmails,
		users,
		at,
		at.UnixMilli(),
		onlineGracePeriodMs,
	)
	return changed, true
}

// CommitOnlineUsersSnapshot atomically commits canonical presence and the raw
// user/IP snapshot only when the process that produced the poll is still the
// current running process. RestartXray uses the same lock for process swaps.
func (s *XrayService) CommitOnlineUsersSnapshot(
	poll OnlineUsersPoll,
	logicalEmails []string,
) (changed, committed bool) {
	lock.Lock()
	defer lock.Unlock()

	proc := poll.process
	return commitOnlineUsersPoll(
		p,
		proc,
		proc != nil && proc.IsRunning(),
		logicalEmails,
		poll.Users,
		time.Now(),
	)
}

// ClearStoppedXrayOnlineSnapshot clears only Xray-owned presence when the
// current process is still stopped. A concurrent restart wins and the clear is
// rejected instead of erasing the replacement process.
func (s *XrayService) ClearStoppedXrayOnlineSnapshot() (changed, committed bool) {
	lock.Lock()
	defer lock.Unlock()

	proc := p
	if proc == nil {
		return false, true
	}
	if proc.IsRunning() {
		return false, false
	}
	now := time.Now().UnixMilli()
	return proc.ClearXrayOnlineSnapshot(now, onlineGracePeriodMs), true
}

// GetCachedOnlineUsers returns the latest fresh snapshot without opening another
// gRPC connection or issuing another GetUsersStats RPC.
func (s *XrayService) GetCachedOnlineUsers() ([]xray.OnlineUser, bool) {
	lock.Lock()
	proc := p
	lock.Unlock()
	if proc == nil || !proc.IsRunning() {
		return nil, false
	}
	if proc.OnlineAPISupport() == xray.OnlineAPIUnsupported {
		return nil, false
	}

	users, ok := proc.CachedOnlineUsersSnapshot(onlineUsersSnapshotMaxAge, time.Now())
	if !ok {
		return nil, false
	}
	lock.Lock()
	valid := p == proc && proc.IsRunning()
	lock.Unlock()
	if !valid {
		return nil, false
	}
	return users, true
}

// HasFreshOnlineUsersSnapshot reports whether secondary consumers may trust the
// current process cache without copying its full user/IP payload.
func (s *XrayService) HasFreshOnlineUsersSnapshot() bool {
	lock.Lock()
	proc := p
	lock.Unlock()
	if proc == nil || !proc.IsRunning() {
		return false
	}
	if proc.OnlineAPISupport() == xray.OnlineAPIUnsupported {
		return false
	}
	fresh := proc.HasFreshOnlineUsersSnapshot(onlineUsersSnapshotMaxAge, time.Now())
	if !fresh {
		return false
	}
	lock.Lock()
	valid := p == proc && proc.IsRunning()
	lock.Unlock()
	return valid
}

// GetFreshExactXrayOnlineClients returns a process-bound exact Xray-only set
// whose matching raw snapshot is still fresh. Auxiliary clients are excluded.
func (s *XrayService) GetFreshExactXrayOnlineClients() ([]string, bool) {
	lock.Lock()
	proc := p
	lock.Unlock()
	if proc == nil || !proc.IsRunning() {
		return nil, false
	}
	if proc.OnlineAPISupport() == xray.OnlineAPIUnsupported {
		return nil, false
	}

	online, ok := proc.FreshExactXrayOnlineClients(
		onlineUsersSnapshotMaxAge,
		time.Now(),
	)
	if !ok {
		return nil, false
	}
	lock.Lock()
	valid := p == proc && proc.IsRunning()
	lock.Unlock()
	if !valid {
		return nil, false
	}
	return online, true
}

// BalancerStatus is the live view of one balancer for the panel UI. Running
// is false when the balancer isn't present in the running core (e.g. xray is
// stopped or the balancer hasn't been saved/applied yet).
type BalancerStatus struct {
	Tag      string   `json:"tag"`
	Running  bool     `json:"running"`
	Override string   `json:"override"`
	Selected []string `json:"selected"`
}

// GetBalancersStatus queries the running core for the live state of the
// given balancer tags. Per-tag failures are reported as Running=false rather
// than failing the whole call, so the UI can render saved-but-not-applied
// balancers alongside live ones.
func (s *XrayService) GetBalancersStatus(tags []string) ([]BalancerStatus, error) {
	statuses := make([]BalancerStatus, 0, len(tags))
	if !s.IsXrayRunning() {
		for _, tag := range tags {
			statuses = append(statuses, BalancerStatus{Tag: tag})
		}
		return statuses, nil
	}
	if err := s.xrayAPI.Init(p.GetAPIPort()); err != nil {
		return nil, err
	}
	defer s.xrayAPI.Close()

	for _, tag := range tags {
		info, err := s.xrayAPI.GetBalancerInfo(tag)
		if err != nil {
			logger.Debug("get balancer info [", tag, "] failed:", err)
			statuses = append(statuses, BalancerStatus{Tag: tag})
			continue
		}
		statuses = append(statuses, BalancerStatus{
			Tag:      tag,
			Running:  true,
			Override: info.Override,
			Selected: info.Selected,
		})
	}
	return statuses, nil
}

// OverrideBalancer forces a balancer in the running core to use the given
// outbound tag; an empty target clears the override. When target names
// another balancer, the override resolves to the loopback outbound that
// routes traffic through the target balancer via the routing rules.
func (s *XrayService) OverrideBalancer(tag, target string) error {
	if !s.IsXrayRunning() {
		return errors.New("xray is not running")
	}
	if target != "" {
		resolved, err := s.resolveOverrideTarget(target)
		if err != nil {
			return err
		}
		if resolved != "" {
			target = resolved
		}
	}
	if err := s.xrayAPI.Init(p.GetAPIPort()); err != nil {
		return err
	}
	defer s.xrayAPI.Close()
	return s.xrayAPI.SetBalancerTarget(tag, target)
}

// resolveOverrideTarget checks if target names a balancer and, if so,
// returns the loopback outbound tag that routes to it through the
// routing rules. Returns empty if target is already a concrete outbound.
func (s *XrayService) resolveOverrideTarget(target string) (string, error) {
	template, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return "", err
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(template), &cfg); err != nil {
		return "", err
	}
	routing, _ := cfg["routing"].(map[string]any)
	if routing == nil {
		return "", nil
	}
	rules, _ := routing["rules"].([]any)
	for _, r := range rules {
		rule, ok := r.(map[string]any)
		if !ok {
			continue
		}
		if rule["balancerTag"] != target {
			continue
		}
		inboundTags, ok := rule["inboundTag"].([]any)
		if !ok || len(inboundTags) == 0 {
			continue
		}
		if lbTag, ok := inboundTags[0].(string); ok && strings.HasPrefix(lbTag, "_bl_") {
			return lbTag, nil
		}
	}
	return "", nil
}

// TestRoute asks the running core which outbound its router picks for the
// described connection.
func (s *XrayService) TestRoute(req xray.RouteTestRequest) (*xray.RouteTestResult, error) {
	if !s.IsXrayRunning() {
		return nil, errors.New("xray is not running")
	}
	if err := s.xrayAPI.Init(p.GetAPIPort()); err != nil {
		return nil, err
	}
	defer s.xrayAPI.Close()
	return s.xrayAPI.TestRoute(req)
}

// RestartXray reconciles the running Xray process with the current desired
// config. When isForce is false it first tries to apply the changes through
// the Xray gRPC API without restarting the process (inbounds, outbounds and
// routing rules/balancers are hot-reloadable); only changes the core cannot
// take at runtime — or a force request — stop and restart the process.
func (s *XrayService) RestartXray(isForce bool) error {
	return s.restartXray(isForce, true, true)
}

// ReconcileXray immediately reconciles the generated configuration with the
// running core without treating an unrelated pending flag as a reason for a
// forced process restart. It is used after successful runtime API mutations,
// where keeping the Process config snapshot aligned is required.
func (s *XrayService) ReconcileXray() error {
	return s.restartXray(false, false, false)
}

func (s *XrayService) restartXray(isForce, honorPending, clearPending bool) error {
	lock.Lock()
	defer lock.Unlock()

	startGeneration := xrayRestartGeneration.Load()

	logger.Debug(
		"restart Xray, force:", isForce,
		"honor pending:", honorPending,
	)
	isManuallyStopped.Store(false)

	xrayConfig, err := s.GetXrayConfig()
	if err != nil {
		return finishXrayRestart(
			startGeneration,
			clearPending,
			err,
		)
	}

	var previousConfig *xray.Config
	var auxiliaryPresence xray.AuxiliaryPresenceSnapshot
	if p != nil {
		auxiliaryPresence = p.SnapshotAuxiliaryPresence()
	}
	restoreAuxiliaryPresence := func(proc *xray.Process) {
		if proc == nil {
			return
		}
		proc.RestoreAuxiliaryPresence(
			auxiliaryPresence,
			time.Now().UnixMilli(),
			onlineGracePeriodMs,
		)
	}

	wasRunning := s.IsXrayRunning()
	if wasRunning {
		previousConfig = p.GetConfig()
		configUnchanged := previousConfig.Equals(xrayConfig)
		pending := isNeedXrayRestart.Load()

		if !isForce &&
			configUnchanged &&
			(!honorPending || !pending) {
			logger.Debug("It does not need to restart Xray")
			return finishXrayRestart(
				startGeneration,
				clearPending,
				nil,
			)
		}

		if !isForce &&
			!configUnchanged &&
			previousConfig.SharedPortPlan.Empty() &&
			xrayConfig.SharedPortPlan.Empty() &&
			s.tryHotApply(xrayConfig) {
			logger.Info(
				"Xray config changes applied through the core API, no restart needed",
			)
			return finishXrayRestart(
				startGeneration,
				clearPending,
				nil,
			)
		}

		// Stop the public frontmux before its private Xray backends. If Xray
		// refuses to stop, restore the old public listeners immediately.
		if stopErr := stopXrayRuntime(p); stopErr != nil {
			recovered, recoverErr := recoverPreviousXrayRuntime(p, previousConfig)
			if recoverErr == nil {
				p = recovered
				restoreAuxiliaryPresence(p)
			}
			return finishXrayRestart(
				startGeneration,
				clearPending,
				errors.Join(
					fmt.Errorf("stop Xray/shared-port runtime before restart: %w", stopErr),
					recoverErr,
				),
			)
		}
	}

	result = ""
	s.xrayAPI.StatsLastValues = nil

	newProcess, startErr := startXrayRuntime(xrayConfig)
	p = newProcess
	restoreAuxiliaryPresence(p)
	if startErr != nil {
		// startXrayRuntime already performs best-effort cleanup. Defensively
		// retry when a partially-started process survived, and never launch the
		// rollback process while that replacement still owns listener sockets.
		var cleanupErr error
		if newProcess != nil && newProcess.IsRunning() {
			cleanupErr = stopXrayRuntime(newProcess)
		}

		var rollbackErr error
		if wasRunning && (newProcess == nil || !newProcess.IsRunning()) {
			var restored *xray.Process
			restored, rollbackErr = restoreXrayRuntime(previousConfig)
			if rollbackErr == nil {
				p = restored
				restoreAuxiliaryPresence(p)
			}
		} else if wasRunning {
			rollbackErr = errors.New("replacement Xray process is still running; previous runtime was not started to avoid socket corruption")
		}
		return finishXrayRestart(
			startGeneration,
			clearPending,
			errors.Join(startErr, cleanupErr, rollbackErr),
		)
	}

	return finishXrayRestart(
		startGeneration,
		clearPending,
		nil,
	)
}

// tryHotApply attempts to reconcile the running Xray instance with newCfg
// through the core gRPC API (HandlerService for inbounds/outbounds,
// RoutingService for rules/balancers). It returns true when the running
// instance now matches newCfg; on any failure it returns false and the
// caller falls back to a full process restart, which cleans up whatever was
// partially applied. Callers must hold the package-level lock.
func (s *XrayService) tryHotApply(newCfg *xray.Config) bool {
	oldCfg := p.GetConfig()
	if !oldCfg.SharedPortPlan.Empty() || !newCfg.SharedPortPlan.Empty() {
		logger.Debug("hot apply: shared-port topology requires a transactional full restart")
		return false
	}
	diff, ok := xray.ComputeHotDiff(oldCfg, newCfg)
	if !ok {
		logger.Debug("hot apply: config change is not API-applicable, falling back to restart")
		return false
	}
	if diff.Empty() {
		p.SetConfig(newCfg)
		return true
	}

	apiPort := p.GetAPIPort()
	if apiPort <= 0 {
		return false
	}
	// A dedicated client: s.xrayAPI may be in use by traffic polling on other
	// service instances and is reset around restarts.
	hotAPI := xray.XrayAPI{}
	if err := hotAPI.Init(apiPort); err != nil {
		logger.Debug("hot apply: failed to init xray api:", err)
		return false
	}
	defer hotAPI.Close()

	// Removals first so changed handlers and port swaps never collide with
	// the additions that follow.
	for _, u := range diff.RemovedUsers {
		if err := hotAPI.RemoveUser(u.Tag, u.Email); err != nil && !xray.IsMissingHandlerErr(err) {
			logger.Info("hot apply: remove user [", u.Email, "] from [", u.Tag, "] failed:", err)
			return false
		}
	}
	for _, tag := range diff.RemovedInboundTags {
		if err := hotAPI.DelInbound(tag); err != nil && !xray.IsMissingHandlerErr(err) {
			logger.Info("hot apply: remove inbound [", tag, "] failed:", err)
			return false
		}
	}
	for _, tag := range diff.RemovedOutboundTags {
		if err := hotAPI.DelOutbound(tag); err != nil && !xray.IsMissingHandlerErr(err) {
			logger.Info("hot apply: remove outbound [", tag, "] failed:", err)
			return false
		}
	}
	for _, ob := range diff.AddedOutbounds {
		if err := addOutboundReconciling(&hotAPI, ob); err != nil {
			logger.Info("hot apply: add outbound failed:", err)
			return false
		}
	}
	for _, ib := range diff.AddedInbounds {
		if err := addInboundReconciling(&hotAPI, ib); err != nil {
			logger.Info("hot apply: add inbound failed:", err)
			return false
		}
	}
	for _, u := range diff.AddedUsers {
		if err := addUserReconciling(&hotAPI, u); err != nil {
			logger.Info("hot apply: add user [", u.Email, "] to [", u.Tag, "] failed:", err)
			return false
		}
	}
	if diff.RoutingConfig != nil {
		if err := hotAPI.ApplyRoutingConfig(diff.RoutingConfig); err != nil {
			logger.Info("hot apply: apply routing config failed:", err)
			return false
		}
	}

	p.SetConfig(newCfg)
	return true
}

// addUserReconciling adds a user, and on an email conflict (the user was
// already applied through the runtime API) replaces the existing user instead.
func addUserReconciling(api *xray.XrayAPI, u xray.UserOp) error {
	err := api.AddUser(u.Protocol, u.Tag, u.User)
	if err == nil || !xray.IsUserExistsErr(err) {
		return err
	}
	if delErr := api.RemoveUser(u.Tag, u.Email); delErr != nil && !xray.IsMissingHandlerErr(delErr) {
		return delErr
	}
	return api.AddUser(u.Protocol, u.Tag, u.User)
}

// addInboundReconciling adds an inbound, and on a tag conflict (the handler
// was already created through the runtime API while the stored snapshot was
// stale) replaces the existing handler instead.
func addInboundReconciling(api *xray.XrayAPI, inbound []byte) error {
	err := api.AddInbound(inbound)
	if err == nil || !xray.IsExistingTagErr(err) {
		return err
	}
	var meta struct {
		Tag string `json:"tag"`
	}
	if jsonErr := json.Unmarshal(inbound, &meta); jsonErr != nil || meta.Tag == "" {
		return err
	}
	if delErr := api.DelInbound(meta.Tag); delErr != nil && !xray.IsMissingHandlerErr(delErr) {
		return delErr
	}
	return api.AddInbound(inbound)
}

// addOutboundReconciling mirrors addInboundReconciling for outbounds.
func addOutboundReconciling(api *xray.XrayAPI, outbound []byte) error {
	err := api.AddOutbound(outbound)
	if err == nil || !xray.IsExistingTagErr(err) {
		return err
	}
	var meta struct {
		Tag string `json:"tag"`
	}
	if jsonErr := json.Unmarshal(outbound, &meta); jsonErr != nil || meta.Tag == "" {
		return err
	}
	if delErr := api.DelOutbound(meta.Tag); delErr != nil && !xray.IsMissingHandlerErr(delErr) {
		return delErr
	}
	return api.AddOutbound(outbound)
}

// StopXray stops the running Xray process.
func (s *XrayService) StopXray() error {
	lock.Lock()
	defer lock.Unlock()
	isManuallyStopped.Store(true)
	logger.Debug("Attempting to stop Xray...")
	if s.IsXrayRunning() || frontMuxManager.Running() {
		return stopXrayRuntime(p)
	}
	return errors.New("xray is not running")
}

// SetToNeedRestart marks that Xray needs to be restarted.
func (s *XrayService) SetToNeedRestart() {
	markXrayRestartNeeded()
}

// GetXrayAPIPort returns the port the local xray process is listening on
// for its gRPC HandlerService, or 0 when xray isn't currently running.
// Exposed for the runtime package's LocalRuntime adapter — runtime can't
// reach into the package-level `p` directly without a service-package
// import cycle.
func (s *XrayService) GetXrayAPIPort() int {
	if p == nil || !p.IsRunning() {
		return 0
	}
	return p.GetAPIPort()
}

// IsNeedRestartAndSetFalse checks if restart is needed and resets the flag to false.
func (s *XrayService) IsNeedRestartAndSetFalse() bool {
	return isNeedXrayRestart.CompareAndSwap(true, false)
}

// DidXrayCrash checks if Xray crashed by verifying it's not running and wasn't manually stopped.
func (s *XrayService) DidXrayCrash() bool {
	return !s.IsXrayRunning() && !isManuallyStopped.Load()
}

// liftXhttpSessionIDKeys renames the legacy XHTTP session keys
// (sessionPlacement/sessionKey) to the v26.6.22 #6258 names
// (sessionIDPlacement/sessionIDKey) inside a streamSettings map. xray-core kept
// no fallback for the old names, so a config stored before the rename would be
// silently ignored by the engine. Returns true if it changed anything.
func liftXhttpSessionIDKeys(stream map[string]any) bool {
	xhttp, ok := stream["xhttpSettings"].(map[string]any)
	if !ok {
		return false
	}
	changed := false
	for legacy, renamed := range map[string]string{
		"sessionPlacement": "sessionIDPlacement",
		"sessionKey":       "sessionIDKey",
	} {
		v, has := xhttp[legacy]
		if !has {
			continue
		}
		if _, exists := xhttp[renamed]; !exists {
			xhttp[renamed] = v
		}
		delete(xhttp, legacy)
		changed = true
	}
	return changed
}

// liftOutboundsXhttpSessionIDKeys applies liftXhttpSessionIDKeys to every
// outbound's streamSettings in the raw outbounds array. The original bytes are
// returned untouched when nothing needs lifting, so an unchanged config never
// looks modified to the hot-reload diff.
func liftOutboundsXhttpSessionIDKeys(raw json_util.RawMessage) json_util.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	var outbounds []map[string]any
	if err := json.Unmarshal(raw, &outbounds); err != nil {
		return raw
	}
	changed := false
	for _, ob := range outbounds {
		if stream, ok := ob["streamSettings"].(map[string]any); ok {
			if liftXhttpSessionIDKeys(stream) {
				changed = true
			}
		}
	}
	if !changed {
		return raw
	}
	if rewritten, err := json.Marshal(outbounds); err == nil {
		return rewritten
	}
	return raw
}
