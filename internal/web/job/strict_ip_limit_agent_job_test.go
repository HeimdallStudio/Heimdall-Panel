package job

import (
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	coreiplimit "github.com/mhsanaei/3x-ui/v3/internal/iplimit"
)

func TestStrictIPLimitAgentRootRoundTrip(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })

	const clientGuid = "aaaaaaaa-1111-4111-8111-111111111111"
	if err := database.GetDB().Create(&model.ClientRecord{
		Email:      "agent-test@example.com",
		ClientGuid: clientGuid,
		LimitIP:    1,
		Enable:     true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	agent := &StrictIPLimitAgent{socketPath: filepath.Join(t.TempDir(), "ip-limit.sock")}
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		agent.mu.Lock()
		agent.started = false
		listener := agent.listener
		agent.listener = nil
		agent.mu.Unlock()
		if listener != nil {
			_ = listener.Close()
		}
	})

	request := func(req coreiplimit.LeaseRequest) coreiplimit.LeaseResponse {
		t.Helper()
		conn, err := net.DialTimeout("unix", agent.socketPath, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if err := json.NewEncoder(conn).Encode(req); err != nil {
			t.Fatal(err)
		}
		var resp coreiplimit.LeaseResponse
		if err := json.NewDecoder(conn).Decode(&resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	first := request(coreiplimit.LeaseRequest{
		Operation:  coreiplimit.LeaseAcquire,
		ClientGuid: clientGuid,
		IP:         "198.51.100.10",
	})
	if !first.Allowed || first.ActiveSlots != 1 || first.LeaseTTLMillis <= 0 {
		t.Fatalf("first root acquire = %#v", first)
	}

	second := request(coreiplimit.LeaseRequest{
		Operation:  coreiplimit.LeaseAcquire,
		ClientGuid: clientGuid,
		IP:         "203.0.113.20",
	})
	if second.Allowed || second.Reason != coreiplimit.DecisionLimitReached {
		t.Fatalf("second distinct root IP = %#v", second)
	}

	released := request(coreiplimit.LeaseRequest{
		Operation:  coreiplimit.LeaseRelease,
		ClientGuid: clientGuid,
		IP:         "198.51.100.10",
	})
	if !released.Allowed || !released.Released {
		t.Fatalf("root release = %#v", released)
	}

}
