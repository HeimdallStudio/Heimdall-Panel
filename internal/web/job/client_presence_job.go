package job

import (
	"sort"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"
)

type clientPresenceDeps struct {
	getOnlineUsers func() (service.OnlineUsersPoll, bool, error)
	resolve        func([]string) ([]string, error)
	commit         func(service.OnlineUsersPoll, []string) (bool, bool)
	clearStopped   func() (bool, bool)
	current        func() []string
	hasClients     func() bool
	broadcast      func(any)
}

// ClientPresenceJob keeps the local online set aligned with the custom core's
// exact connection snapshot without coupling it to the five-second traffic job.
type ClientPresenceJob struct {
	mu   sync.Mutex
	deps clientPresenceDeps
}

// NewClientPresenceJob creates the production presence poller.
func NewClientPresenceJob() *ClientPresenceJob {
	xrayService := new(service.XrayService)
	inboundService := new(service.InboundService)
	resolver := service.NewPresenceEmailResolver()

	return newClientPresenceJob(clientPresenceDeps{
		getOnlineUsers: xrayService.GetOnlineUsers,
		resolve:        resolver.Resolve,
		commit:         xrayService.CommitOnlineUsersSnapshot,
		clearStopped:   xrayService.ClearStoppedXrayOnlineSnapshot,
		current:        inboundService.GetOnlineClients,
		hasClients:     websocket.HasClients,
		broadcast:      websocket.BroadcastPresence,
	})
}

func newClientPresenceJob(deps clientPresenceDeps) *ClientPresenceJob {
	return &ClientPresenceJob{deps: deps}
}

// Run performs one presence reconciliation. A transient RPC or resolver error
// preserves the last known set. An unsupported old core leaves legacy
// traffic-delta grace mode in control.
func (j *ClientPresenceJob) Run() {
	if !j.mu.TryLock() {
		return
	}
	defer j.mu.Unlock()

	before := normalizePresenceEmails(j.deps.current())

	// Clear through the restart lock first. A true committed result means the
	// current process is still stopped (or absent); false means it is running and
	// the authoritative poll should proceed.
	if changed, stopped := j.deps.clearStopped(); stopped {
		j.broadcastCommitted(before, changed)
		return
	}

	poll, supported, err := j.deps.getOnlineUsers()
	if err != nil {
		logger.Debug("client presence snapshot failed:", err)
		return
	}
	if !supported {
		return
	}

	runtimeEmails := make([]string, 0, len(poll.Users))
	for _, user := range poll.Users {
		runtimeEmails = append(runtimeEmails, user.Email)
	}

	logicalEmails, err := j.deps.resolve(runtimeEmails)
	if err != nil {
		logger.Debug("client presence email resolution failed:", err)
		return
	}

	changed, committed := j.deps.commit(poll, logicalEmails)
	if !committed {
		return
	}
	j.broadcastCommitted(before, changed)
}

func (j *ClientPresenceJob) broadcastCommitted(before []string, changed bool) {
	broadcastPresenceTransition(
		before,
		changed,
		j.deps.current,
		j.deps.hasClients,
		j.deps.broadcast,
	)
}

func broadcastPresenceTransition(
	before []string,
	changed bool,
	current func() []string,
	hasClients func() bool,
	broadcast func(any),
) {
	if !changed || current == nil {
		return
	}

	after := normalizePresenceEmails(current())
	if equalPresenceEmails(normalizePresenceEmails(before), after) {
		return
	}
	if hasClients != nil && !hasClients() {
		return
	}
	if broadcast != nil {
		broadcast(map[string]any{"onlineClients": after})
	}
}

func normalizePresenceEmails(emails []string) []string {
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

func equalPresenceEmails(left, right []string) bool {
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
