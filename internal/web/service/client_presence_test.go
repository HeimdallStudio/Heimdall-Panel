package service

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestPresenceEmailResolverLooksUpOnlyRuntimeIdentifiers(t *testing.T) {
	lookupCalls := 0
	var lookupInput []string
	resolver := newPresenceEmailResolver(func(emails []string) (map[string]string, error) {
		lookupCalls++
		lookupInput = append([]string(nil), emails...)
		return map[string]string{
			"hmstat_7_deadbeefdeadbeef": "alice@example.test",
		}, nil
	})

	got, err := resolver.Resolve([]string{
		"direct@example.test",
		"hmstat_team@example.test",
		"hmstat_7_deadbeefdeadbeef",
		"hmstat_7_deadbeefdeadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"alice@example.test", "direct@example.test", "hmstat_team@example.test"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resolved = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(lookupInput, []string{"hmstat_7_deadbeefdeadbeef"}) {
		t.Fatalf("lookup input = %#v", lookupInput)
	}

	got, err = resolver.Resolve([]string{
		"direct@example.test",
		"hmstat_team@example.test",
		"hmstat_7_deadbeefdeadbeef",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cached resolved = %#v, want %#v", got, want)
	}
	if lookupCalls != 1 {
		t.Fatalf("lookup calls = %d, want 1", lookupCalls)
	}
}

func TestPresenceEmailResolverRetriesMissingMappingNextTick(t *testing.T) {
	calls := 0
	resolver := newPresenceEmailResolver(func(emails []string) (map[string]string, error) {
		calls++
		if calls == 1 {
			return map[string]string{}, nil
		}
		return map[string]string{"hmstat_9_cafecafecafecafe": "logical@example.test"}, nil
	})

	got, err := resolver.Resolve([]string{"hmstat_9_cafecafecafecafe"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"hmstat_9_cafecafecafecafe"}) {
		t.Fatalf("first resolved = %#v", got)
	}

	got, err = resolver.Resolve([]string{"hmstat_9_cafecafecafecafe"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"logical@example.test"}) {
		t.Fatalf("second resolved = %#v", got)
	}
	if calls != 2 {
		t.Fatalf("lookup calls = %d, want 2", calls)
	}
}

func TestPresenceEmailResolverPrunesInactivePositiveMapping(t *testing.T) {
	calls := 0
	resolver := newPresenceEmailResolver(func(emails []string) (map[string]string, error) {
		calls++
		return map[string]string{"hmstat_3_beefbeefbeefbeef": "logical@example.test"}, nil
	})

	if _, err := resolver.Resolve([]string{"hmstat_3_beefbeefbeefbeef"}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve([]string{"direct@example.test"}); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve([]string{"hmstat_3_beefbeefbeefbeef"}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("lookup calls after inactive prune = %d, want 2", calls)
	}
}

func TestPresenceEmailResolverDoesNotCacheLookupFailure(t *testing.T) {
	calls := 0
	resolver := newPresenceEmailResolver(func(emails []string) (map[string]string, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("temporary database error")
		}
		return map[string]string{"hmstat_1_aaaaaaaaaaaaaaaa": "logical"}, nil
	})

	if _, err := resolver.Resolve([]string{"hmstat_1_aaaaaaaaaaaaaaaa"}); err == nil {
		t.Fatal("first lookup must fail")
	}
	got, err := resolver.Resolve([]string{"hmstat_1_aaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"logical"}) {
		t.Fatalf("resolved = %#v", got)
	}
	if calls != 2 {
		t.Fatalf("lookup calls = %d, want 2", calls)
	}
}

func TestCommitOnlineUsersPollRejectsReplacedProcess(t *testing.T) {
	oldProcess := xray.NewProcess(&xray.Config{})
	newProcess := xray.NewProcess(&xray.Config{})
	at := time.Unix(1700000000, 0)
	users := []xray.OnlineUser{{
		Email: "hmstat_7_deadbeefdeadbeef",
		IPs:   []xray.OnlineIP{{IP: "203.0.113.7", LastSeen: at.Unix()}},
	}}

	changed, committed := commitOnlineUsersPoll(
		newProcess,
		oldProcess,
		true,
		[]string{"old-process@example.test"},
		users,
		at,
	)
	if changed || committed {
		t.Fatalf("changed=%v committed=%v, want false/false", changed, committed)
	}
	if got := newProcess.GetLocalOnlineClients(); len(got) != 0 {
		t.Fatalf("new process presence changed: %#v", got)
	}
	if _, ok := newProcess.CachedOnlineUsersSnapshot(time.Minute, at); ok {
		t.Fatal("new process raw snapshot changed")
	}
	if got := oldProcess.GetLocalOnlineClients(); len(got) != 0 {
		t.Fatalf("old process presence changed: %#v", got)
	}
}

func TestCommitOnlineUsersPollCommitsCanonicalAndRawAtomically(t *testing.T) {
	proc := xray.NewProcess(&xray.Config{})
	at := time.Unix(1700000000, 0)
	users := []xray.OnlineUser{{
		Email: "hmstat_7_deadbeefdeadbeef",
		IPs:   []xray.OnlineIP{{IP: "203.0.113.7", LastSeen: at.Unix()}},
	}}

	changed, committed := commitOnlineUsersPoll(
		proc,
		proc,
		true,
		[]string{"alice@example.test"},
		users,
		at,
	)
	if !changed || !committed {
		t.Fatalf("changed=%v committed=%v, want true/true", changed, committed)
	}
	if got, want := proc.GetExactXrayOnlineClients(), []string{"alice@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exact presence = %#v, want %#v", got, want)
	}
	raw, ok := proc.CachedOnlineUsersSnapshot(time.Minute, at)
	if !ok || len(raw) != 1 || raw[0].Email != "hmstat_7_deadbeefdeadbeef" {
		t.Fatalf("raw snapshot = %#v ok=%v", raw, ok)
	}
}
