package job

import (
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/xray"
)

func TestActiveClientTrafficEmailsUsesCanonicalIdentityOnce(t *testing.T) {
	active, activeSet := activeClientTrafficEmails([]*xray.ClientTraffic{
		{Email: "alice@example.test", Up: 10},
		{Email: "alice@example.test", Down: 20},
		{Email: "bob@example.test"},
		nil,
	})

	if want := []string{"alice@example.test"}; !reflect.DeepEqual(active, want) {
		t.Fatalf("active = %#v, want %#v", active, want)
	}
	if _, ok := activeSet["alice@example.test"]; !ok {
		t.Fatal("alice missing from active set")
	}
	if _, ok := activeSet["hmstat_1_aaaaaaaaaaaaaaaa"]; ok {
		t.Fatal("runtime identity leaked into canonical active set")
	}
}

func TestIdleExactXrayClientsExcludesClientsWithCanonicalDelta(t *testing.T) {
	idle := idleExactXrayClients(
		[]string{"alice@example.test", "bob@example.test"},
		map[string]struct{}{"alice@example.test": {}},
	)
	if want := []string{"bob@example.test"}; !reflect.DeepEqual(idle, want) {
		t.Fatalf("idle = %#v, want %#v", idle, want)
	}
}

func TestBroadcastPresenceTransitionPublishesPrunedSnapshotOnce(t *testing.T) {
	var broadcasts []any
	broadcastPresenceTransition(
		[]string{"alice@example.test", "stale@example.test"},
		true,
		func() []string { return []string{"alice@example.test"} },
		func() bool { return true },
		func(payload any) { broadcasts = append(broadcasts, payload) },
	)

	if len(broadcasts) != 1 {
		t.Fatalf("broadcasts = %d, want 1", len(broadcasts))
	}
	payload := broadcasts[0].(map[string]any)
	if got, want := payload["onlineClients"].([]string), []string{"alice@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("onlineClients = %#v, want %#v", got, want)
	}
}

func TestBroadcastPresenceTransitionSkipsNoChangeAndNoClients(t *testing.T) {
	calls := 0
	broadcast := func(any) { calls++ }

	broadcastPresenceTransition(
		[]string{"alice@example.test"},
		false,
		func() []string { return nil },
		func() bool { return true },
		broadcast,
	)
	broadcastPresenceTransition(
		[]string{"alice@example.test"},
		true,
		func() []string { return []string{"alice@example.test"} },
		func() bool { return true },
		broadcast,
	)
	broadcastPresenceTransition(
		[]string{"alice@example.test"},
		true,
		func() []string { return []string{} },
		func() bool { return false },
		broadcast,
	)

	if calls != 0 {
		t.Fatalf("broadcast calls = %d, want 0", calls)
	}
}
