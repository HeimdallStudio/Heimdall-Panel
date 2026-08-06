package job

import (
	"errors"
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

type presenceJobHarness struct {
	running    bool
	state      []string
	commitN    int
	clearN     int
	broadcasts []any
}

func (h *presenceJobHarness) current() []string {
	return append([]string(nil), h.state...)
}

func (h *presenceJobHarness) apply(next []string) bool {
	next = normalizePresenceEmails(next)
	if equalPresenceEmails(normalizePresenceEmails(h.state), next) {
		return false
	}
	h.state = next
	return true
}

func (h *presenceJobHarness) commit(
	_ service.OnlineUsersPoll,
	next []string,
) (bool, bool) {
	h.commitN++
	return h.apply(next), true
}

func (h *presenceJobHarness) clearStopped() (bool, bool) {
	h.clearN++
	if h.running {
		return false, false
	}
	return h.apply(nil), true
}

func (h *presenceJobHarness) broadcast(payload any) {
	h.broadcasts = append(h.broadcasts, payload)
}

func pollWithUsers(users ...xray.OnlineUser) service.OnlineUsersPoll {
	return service.OnlineUsersPoll{Users: users}
}

func TestClientPresenceJobSuccessfulEmptySnapshotClearsImmediately(t *testing.T) {
	h := &presenceJobHarness{
		running: true,
		state:   []string{"alice@example.test"},
	}
	job := newClientPresenceJob(clientPresenceDeps{
		getOnlineUsers: func() (service.OnlineUsersPoll, bool, error) {
			return pollWithUsers(), true, nil
		},
		resolve:      func(emails []string) ([]string, error) { return emails, nil },
		commit:       h.commit,
		clearStopped: h.clearStopped,
		current:      h.current,
		hasClients:   func() bool { return true },
		broadcast:    h.broadcast,
	})

	job.Run()

	if len(h.state) != 0 {
		t.Fatalf("state = %#v, want empty", h.state)
	}
	if len(h.broadcasts) != 1 {
		t.Fatalf("broadcasts = %d, want 1", len(h.broadcasts))
	}
	payload := h.broadcasts[0].(map[string]any)
	got := payload["onlineClients"].([]string)
	if got == nil || len(got) != 0 {
		t.Fatalf("onlineClients = %#v, want non-nil empty slice", got)
	}
}

func TestClientPresenceJobTransientErrorPreservesState(t *testing.T) {
	h := &presenceJobHarness{
		running: true,
		state:   []string{"alice@example.test"},
	}
	job := newClientPresenceJob(clientPresenceDeps{
		getOnlineUsers: func() (service.OnlineUsersPoll, bool, error) {
			return service.OnlineUsersPoll{}, false, errors.New("temporary grpc error")
		},
		resolve:      func(emails []string) ([]string, error) { return emails, nil },
		commit:       h.commit,
		clearStopped: h.clearStopped,
		current:      h.current,
		hasClients:   func() bool { return true },
		broadcast:    h.broadcast,
	})

	job.Run()

	if !reflect.DeepEqual(h.state, []string{"alice@example.test"}) {
		t.Fatalf("state changed: %#v", h.state)
	}
	if h.commitN != 0 || len(h.broadcasts) != 0 {
		t.Fatalf("commit=%d broadcasts=%d", h.commitN, len(h.broadcasts))
	}
}

func TestClientPresenceJobStoppedCoreClearsState(t *testing.T) {
	h := &presenceJobHarness{
		running: false,
		state:   []string{"alice@example.test"},
	}
	job := newClientPresenceJob(clientPresenceDeps{
		getOnlineUsers: func() (service.OnlineUsersPoll, bool, error) {
			panic("must not call")
		},
		resolve: func(emails []string) ([]string, error) {
			panic("must not call")
		},
		commit: func(service.OnlineUsersPoll, []string) (bool, bool) {
			panic("must not call")
		},
		clearStopped: h.clearStopped,
		current:      h.current,
		hasClients:   func() bool { return true },
		broadcast:    h.broadcast,
	})

	job.Run()

	if len(h.state) != 0 || len(h.broadcasts) != 1 || h.clearN != 1 {
		t.Fatalf(
			"state=%#v broadcasts=%d clear=%d",
			h.state,
			len(h.broadcasts),
			h.clearN,
		)
	}
}

func TestClientPresenceJobConcurrentStartRejectsStoppedClear(t *testing.T) {
	h := &presenceJobHarness{state: []string{"alice@example.test"}}
	job := newClientPresenceJob(clientPresenceDeps{
		clearStopped: func() (bool, bool) {
			return false, false
		},
		getOnlineUsers: func() (service.OnlineUsersPoll, bool, error) {
			return service.OnlineUsersPoll{}, false, nil
		},
		current:    h.current,
		hasClients: func() bool { return true },
		broadcast:  h.broadcast,
	})

	job.Run()

	if !reflect.DeepEqual(h.state, []string{"alice@example.test"}) {
		t.Fatalf("state changed: %#v", h.state)
	}
	if len(h.broadcasts) != 0 {
		t.Fatalf("broadcasts = %d, want 0", len(h.broadcasts))
	}
}

func TestClientPresenceJobUnchangedSnapshotDoesNotBroadcast(t *testing.T) {
	h := &presenceJobHarness{
		running: true,
		state:   []string{"alice@example.test"},
	}
	job := newClientPresenceJob(clientPresenceDeps{
		getOnlineUsers: func() (service.OnlineUsersPoll, bool, error) {
			return pollWithUsers(xray.OnlineUser{Email: "alice@example.test"}), true, nil
		},
		resolve: func(emails []string) ([]string, error) {
			return []string{"alice@example.test"}, nil
		},
		commit:       h.commit,
		clearStopped: h.clearStopped,
		current:      h.current,
		hasClients:   func() bool { return true },
		broadcast:    h.broadcast,
	})

	job.Run()

	if len(h.broadcasts) != 0 {
		t.Fatalf("broadcasts = %d, want 0", len(h.broadcasts))
	}
}

func TestClientPresenceJobUnsupportedCoreKeepsLegacyState(t *testing.T) {
	h := &presenceJobHarness{
		running: true,
		state:   []string{"legacy@example.test"},
	}
	job := newClientPresenceJob(clientPresenceDeps{
		getOnlineUsers: func() (service.OnlineUsersPoll, bool, error) {
			return service.OnlineUsersPoll{}, false, nil
		},
		resolve: func(emails []string) ([]string, error) {
			panic("must not call")
		},
		commit:       h.commit,
		clearStopped: h.clearStopped,
		current:      h.current,
		hasClients:   func() bool { return true },
		broadcast:    h.broadcast,
	})

	job.Run()

	if h.commitN != 0 || !reflect.DeepEqual(h.state, []string{"legacy@example.test"}) {
		t.Fatalf("commit=%d state=%#v", h.commitN, h.state)
	}
}

func TestClientPresenceJobRejectsPollFromReplacedProcess(t *testing.T) {
	h := &presenceJobHarness{
		running: true,
		state:   []string{"new-process@example.test"},
	}
	job := newClientPresenceJob(clientPresenceDeps{
		getOnlineUsers: func() (service.OnlineUsersPoll, bool, error) {
			return pollWithUsers(xray.OnlineUser{Email: "old-runtime"}), true, nil
		},
		resolve: func([]string) ([]string, error) {
			return []string{"old-process@example.test"}, nil
		},
		commit: func(service.OnlineUsersPoll, []string) (bool, bool) {
			return false, false
		},
		clearStopped: h.clearStopped,
		current:      h.current,
		hasClients:   func() bool { return true },
		broadcast:    h.broadcast,
	})

	job.Run()

	if !reflect.DeepEqual(h.state, []string{"new-process@example.test"}) {
		t.Fatalf("state changed: %#v", h.state)
	}
	if len(h.broadcasts) != 0 {
		t.Fatalf("broadcasts = %d, want 0", len(h.broadcasts))
	}
}

func TestClientPresenceJobCommitsRawAndCanonicalTogether(t *testing.T) {
	order := make([]string, 0, 2)
	var committedPoll service.OnlineUsersPoll
	var committedLogical []string
	h := &presenceJobHarness{running: true}

	job := newClientPresenceJob(clientPresenceDeps{
		getOnlineUsers: func() (service.OnlineUsersPoll, bool, error) {
			return pollWithUsers(xray.OnlineUser{
				Email: "hmstat_7_deadbeefdeadbeef",
				IPs:   []xray.OnlineIP{{IP: "203.0.113.7", LastSeen: 1700000000}},
			}), true, nil
		},
		resolve: func(emails []string) ([]string, error) {
			order = append(order, "resolve")
			return []string{"alice@example.test"}, nil
		},
		commit: func(poll service.OnlineUsersPoll, logical []string) (bool, bool) {
			order = append(order, "commit")
			committedPoll = poll
			committedLogical = append([]string(nil), logical...)
			h.apply(logical)
			return true, true
		},
		clearStopped: h.clearStopped,
		current:      h.current,
		hasClients:   func() bool { return false },
	})

	job.Run()

	if !reflect.DeepEqual(order, []string{"resolve", "commit"}) {
		t.Fatalf("order = %#v", order)
	}
	if !reflect.DeepEqual(committedLogical, []string{"alice@example.test"}) {
		t.Fatalf("logical = %#v", committedLogical)
	}
	if len(committedPoll.Users) != 1 ||
		committedPoll.Users[0].Email != "hmstat_7_deadbeefdeadbeef" ||
		len(committedPoll.Users[0].IPs) != 1 ||
		committedPoll.Users[0].IPs[0].IP != "203.0.113.7" {
		t.Fatalf("poll = %#v", committedPoll)
	}
}
