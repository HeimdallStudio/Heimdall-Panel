package job

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	coreiplimit "github.com/mhsanaei/3x-ui/v3/internal/iplimit"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

const (
	defaultStrictIPLimitSocketPath = "/run/secx/ip-limit.sock"
	strictIPLimitSocketEnv         = "XUI_STRICT_IP_LIMIT_SOCKET"
	strictIPLimitRequestMaxBytes   = 16 << 10
	strictIPLimitIOTimeout         = 5 * time.Second
)

type StrictIPLimitAgent struct {
	socketPath string
	service    service.StrictIPLimitService

	mu       sync.Mutex
	listener *net.UnixListener
	started  bool
}

func newStrictIPLimitAgent() *StrictIPLimitAgent {
	path := strings.TrimSpace(os.Getenv(strictIPLimitSocketEnv))
	if path == "" {
		path = defaultStrictIPLimitSocketPath
	}
	return &StrictIPLimitAgent{socketPath: filepath.Clean(path)}
}

func (a *StrictIPLimitAgent) Start() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.started {
		return nil
	}
	if a.socketPath == "" || a.socketPath == "." || a.socketPath == string(filepath.Separator) {
		return errors.New("strict ip-limit socket path is invalid")
	}
	if err := os.MkdirAll(filepath.Dir(a.socketPath), 0o755); err != nil {
		return fmt.Errorf("create strict ip-limit socket directory: %w", err)
	}
	if info, err := os.Lstat(a.socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket path %s", a.socketPath)
		}
		if err := os.Remove(a.socketPath); err != nil {
			return fmt.Errorf("remove stale strict ip-limit socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: a.socketPath, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen strict ip-limit socket: %w", err)
	}
	if err := os.Chmod(a.socketPath, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(a.socketPath)
		return fmt.Errorf("secure strict ip-limit socket: %w", err)
	}
	a.listener = listener
	a.started = true
	go a.acceptLoop(listener)
	return nil
}

func (a *StrictIPLimitAgent) acceptLoop(listener *net.UnixListener) {
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			a.mu.Lock()
			running := a.started && a.listener == listener
			a.mu.Unlock()
			if !running {
				return
			}
			continue
		}
		go a.handleConn(conn)
	}
}

func (a *StrictIPLimitAgent) handleConn(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(strictIPLimitIOTimeout))

	var req coreiplimit.LeaseRequest
	decoder := json.NewDecoder(io.LimitReader(conn, strictIPLimitRequestMaxBytes))
	if err := decoder.Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(coreiplimit.LeaseResponse{Allowed: false, Error: "invalid strict ip-limit local request"})
		return
	}

	resp, err := a.service.ResolveLocal(context.Background(), req)
	if err != nil {
		resp = coreiplimit.LeaseResponse{Allowed: false, Error: err.Error()}
	}
	_ = json.NewEncoder(conn).Encode(resp)
}

type StrictIPLimitAgentJob struct{}

var (
	strictIPLimitAgentMu     sync.Mutex
	activeStrictIPLimitAgent *StrictIPLimitAgent
)

func NewStrictIPLimitAgentJob() *StrictIPLimitAgentJob { return &StrictIPLimitAgentJob{} }

func (j *StrictIPLimitAgentJob) Run() {
	strictIPLimitAgentMu.Lock()
	defer strictIPLimitAgentMu.Unlock()
	if activeStrictIPLimitAgent != nil {
		return
	}
	agent := newStrictIPLimitAgent()
	if err := agent.Start(); err != nil {
		logger.Errorf("start Strict-B IP-limit agent failed: %v", err)
		return
	}
	activeStrictIPLimitAgent = agent
	logger.Infof("Strict-B IP-limit agent listening on %s", agent.socketPath)
}
