package xray

import (
	"reflect"
	"sync"
	"testing"
	"time"
)

const testPresenceGraceMs int64 = 20_000

func TestSourceSeparatedPresenceUnionAndDeduplication(t *testing.T) {
	p := NewProcess(&Config{})

	p.RefreshAuxiliaryOnline(
		[]string{"mtproto@example.test", "shared@example.test"},
		[]string{"mtproto-in"},
		1_000,
		testPresenceGraceMs,
	)
	if !p.ReplaceXrayOnlineSnapshot(
		[]string{"xray@example.test", "shared@example.test"},
		2_000,
		testPresenceGraceMs,
	) {
		t.Fatal("first exact Xray snapshot must change the visible union")
	}

	want := []string{
		"mtproto@example.test",
		"shared@example.test",
		"xray@example.test",
	}
	if got := p.GetLocalOnlineClients(); !reflect.DeepEqual(got, want) {
		t.Fatalf("local union = %#v, want %#v", got, want)
	}

	p.ReplaceXrayOnlineSnapshot(
		[]string{"next@example.test"},
		3_000,
		testPresenceGraceMs,
	)
	want = []string{"mtproto@example.test", "next@example.test", "shared@example.test"}
	if got := p.GetLocalOnlineClients(); !reflect.DeepEqual(got, want) {
		t.Fatalf("next Xray snapshot erased auxiliary presence: %#v", got)
	}
}

func TestEmptyXraySnapshotPreservesAuxiliaryAndStoppedXraySemantics(t *testing.T) {
	p := NewProcess(&Config{})

	p.RefreshAuxiliaryOnline(
		[]string{"mtproto@example.test"},
		[]string{"mtproto-in"},
		1_000,
		testPresenceGraceMs,
	)
	p.ReplaceXrayOnlineSnapshot(
		[]string{"xray@example.test"},
		2_000,
		testPresenceGraceMs,
	)

	if !p.ReplaceXrayOnlineSnapshot(nil, 3_000, testPresenceGraceMs) {
		t.Fatal("empty exact snapshot must remove the Xray-only client")
	}
	if got, want := p.GetLocalOnlineClients(), []string{"mtproto@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("empty Xray snapshot removed auxiliary client: got %#v want %#v", got, want)
	}
	if p.ReplaceXrayOnlineSnapshot(nil, 4_000, testPresenceGraceMs) {
		t.Fatal("repeated empty Xray snapshot must not report a visible change")
	}
}

func TestAuxiliaryExpiryAndDisconnectDoNotTouchExactXray(t *testing.T) {
	p := NewProcess(&Config{})

	p.ReplaceXrayOnlineSnapshot(
		[]string{"xray@example.test"},
		1_000,
		testPresenceGraceMs,
	)
	p.RefreshAuxiliaryOnline(
		[]string{"mtproto@example.test"},
		nil,
		2_000,
		testPresenceGraceMs,
	)

	p.RefreshAuxiliaryOnline(nil, nil, 21_999, testPresenceGraceMs)
	if got := p.GetLocalOnlineClients(); len(got) != 2 {
		t.Fatalf("auxiliary expired too early: %#v", got)
	}

	if !p.RefreshAuxiliaryOnline(nil, nil, 22_000, testPresenceGraceMs) {
		t.Fatal("auxiliary expiry must change the visible union")
	}
	if got, want := p.GetLocalOnlineClients(), []string{"xray@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("auxiliary expiry changed exact Xray presence: got %#v want %#v", got, want)
	}
}

func TestLegacyXrayCannotResurrectAfterExactButStillTracksInboundActivity(t *testing.T) {
	p := NewProcess(&Config{})

	p.RefreshLegacyXrayOnline(
		[]string{"legacy@example.test"},
		[]string{"legacy-in"},
		1_000,
		testPresenceGraceMs,
	)
	if got := p.GetLocalOnlineClients(); !reflect.DeepEqual(got, []string{"legacy@example.test"}) {
		t.Fatalf("legacy fallback = %#v", got)
	}

	p.ReplaceXrayOnlineSnapshot(
		[]string{"exact@example.test"},
		2_000,
		testPresenceGraceMs,
	)
	p.RefreshLegacyXrayOnline(
		[]string{"legacy@example.test", "stale@example.test"},
		[]string{"exact-active-in"},
		3_000,
		testPresenceGraceMs,
	)

	if got, want := p.GetLocalOnlineClients(), []string{"exact@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy traffic resurrected an exact-offline user: got %#v want %#v", got, want)
	}
	active := p.GetLocalActiveInbounds()
	if !reflect.DeepEqual(active, []string{"exact-active-in", "legacy-in"}) {
		t.Fatalf("inbound activity union = %#v", active)
	}
}

func TestConcurrentXrayAndAuxiliaryPresenceUpdates(t *testing.T) {
	p := NewProcess(&Config{})
	var wg sync.WaitGroup

	for i := int64(0); i < 200; i++ {
		wg.Add(2)
		go func(now int64) {
			defer wg.Done()
			p.ReplaceXrayOnlineSnapshot(
				[]string{"xray@example.test", "shared@example.test"},
				now,
				testPresenceGraceMs,
			)
		}(10_000 + i)
		go func(now int64) {
			defer wg.Done()
			p.RefreshAuxiliaryOnline(
				[]string{"mtproto@example.test", "shared@example.test"},
				[]string{"mtproto-in"},
				now,
				testPresenceGraceMs,
			)
		}(10_000 + i)
	}
	wg.Wait()

	want := []string{
		"mtproto@example.test",
		"shared@example.test",
		"xray@example.test",
	}
	if got := p.GetLocalOnlineClients(); !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent union = %#v, want %#v", got, want)
	}
}

func TestAuxiliaryPresenceSnapshotSurvivesProcessReplacement(t *testing.T) {
	oldProcess := NewProcess(&Config{})
	oldProcess.RefreshAuxiliaryOnline(
		[]string{"mtproto@example.test"},
		[]string{"mtproto-in"},
		1_000,
		testPresenceGraceMs,
	)

	snapshot := oldProcess.SnapshotAuxiliaryPresence()
	newProcess := NewProcess(&Config{})
	if !newProcess.RestoreAuxiliaryPresence(
		snapshot,
		5_000,
		testPresenceGraceMs,
	) {
		t.Fatal("fresh auxiliary snapshot must change the new process")
	}
	if got, want := newProcess.GetLocalOnlineClients(), []string{"mtproto@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored auxiliary presence = %#v, want %#v", got, want)
	}
	if got, want := newProcess.GetLocalActiveInbounds(), []string{"mtproto-in"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("restored auxiliary inbounds = %#v, want %#v", got, want)
	}

	staleProcess := NewProcess(&Config{})
	if staleProcess.RestoreAuxiliaryPresence(
		snapshot,
		21_000,
		testPresenceGraceMs,
	) {
		t.Fatal("stale auxiliary snapshot must not change the new process")
	}
	if got := staleProcess.GetLocalOnlineClients(); len(got) != 0 {
		t.Fatalf("stale auxiliary presence restored: %#v", got)
	}
}

func TestOnlineUsersSnapshotCacheFreshnessIsolationAndEmptyState(t *testing.T) {
	p := NewProcess(&Config{})
	at := time.Unix(1700000000, 0)
	source := []OnlineUser{{
		Email: "runtime@example.test",
		IPs:   []OnlineIP{{IP: "203.0.113.10", LastSeen: 1700000000}},
	}}

	if _, ok := p.CachedOnlineUsersSnapshot(3*time.Second, at); ok {
		t.Fatal("cache must be unavailable before the first successful poll")
	}
	if p.HasFreshOnlineUsersSnapshot(3*time.Second, at) {
		t.Fatal("freshness gate must be false before the first successful poll")
	}

	p.StoreOnlineUsersSnapshot(source, at)
	source[0].Email = "mutated"
	source[0].IPs[0].IP = "198.51.100.9"

	got, ok := p.CachedOnlineUsersSnapshot(3*time.Second, at.Add(2*time.Second))
	if !ok {
		t.Fatal("fresh snapshot was not returned")
	}
	if !p.HasFreshOnlineUsersSnapshot(3*time.Second, at.Add(2*time.Second)) {
		t.Fatal("freshness gate rejected a fresh snapshot")
	}
	if got[0].Email != "runtime@example.test" || got[0].IPs[0].IP != "203.0.113.10" {
		t.Fatalf("stored snapshot was aliased: %#v", got)
	}

	got[0].Email = "consumer mutation"
	got[0].IPs[0].IP = "192.0.2.7"
	again, ok := p.CachedOnlineUsersSnapshot(3*time.Second, at.Add(2*time.Second))
	if !ok || again[0].Email != "runtime@example.test" || again[0].IPs[0].IP != "203.0.113.10" {
		t.Fatalf("returned snapshot was not isolated: %#v", again)
	}

	if _, ok := p.CachedOnlineUsersSnapshot(3*time.Second, at.Add(4*time.Second)); ok {
		t.Fatal("stale snapshot must not be returned")
	}
	if p.HasFreshOnlineUsersSnapshot(3*time.Second, at.Add(4*time.Second)) {
		t.Fatal("freshness gate accepted a stale snapshot")
	}

	p.StoreOnlineUsersSnapshot([]OnlineUser{}, at.Add(5*time.Second))
	empty, ok := p.CachedOnlineUsersSnapshot(3*time.Second, at.Add(6*time.Second))
	if !ok || empty == nil || len(empty) != 0 {
		t.Fatalf("successful empty snapshot lost: ok=%v value=%#v", ok, empty)
	}
	if !p.HasFreshOnlineUsersSnapshot(3*time.Second, at.Add(6*time.Second)) {
		t.Fatal("successful empty snapshot must still satisfy the freshness gate")
	}
}

func TestCommitXrayOnlineSnapshotAndClearAreSourceSafe(t *testing.T) {
	p := NewProcess(&Config{})
	at := time.Unix(1700000000, 0)

	p.RefreshAuxiliaryOnline(
		[]string{"mtproto@example.test"},
		[]string{"mtproto-in"},
		at.UnixMilli(),
		testPresenceGraceMs,
	)

	changed := p.CommitXrayOnlineSnapshot(
		[]string{"xray@example.test"},
		[]OnlineUser{{
			Email: "hmstat_1_aaaaaaaaaaaaaaaa",
			IPs:   []OnlineIP{{IP: "203.0.113.1", LastSeen: at.Unix()}},
		}},
		at,
		at.UnixMilli(),
		testPresenceGraceMs,
	)
	if !changed {
		t.Fatal("atomic commit must change the visible union")
	}
	if !p.XrayOnlineExact() {
		t.Fatal("exact mode was not enabled")
	}
	if got, want := p.GetLocalOnlineClients(), []string{
		"mtproto@example.test",
		"xray@example.test",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("local online = %#v, want %#v", got, want)
	}
	if got, want := p.GetExactXrayOnlineClients(), []string{"xray@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exact Xray online = %#v, want %#v", got, want)
	}
	freshExact, fresh := p.FreshExactXrayOnlineClients(time.Minute, at.Add(time.Second))
	if want := []string{"xray@example.test"}; !fresh || !reflect.DeepEqual(freshExact, want) {
		t.Fatalf("fresh exact Xray online = %#v fresh=%v, want %#v/true", freshExact, fresh, want)
	}
	if _, fresh := p.FreshExactXrayOnlineClients(time.Minute, at.Add(2*time.Minute)); fresh {
		t.Fatal("stale exact Xray snapshot was accepted")
	}
	raw, ok := p.CachedOnlineUsersSnapshot(time.Minute, at)
	if !ok || len(raw) != 1 || raw[0].Email != "hmstat_1_aaaaaaaaaaaaaaaa" {
		t.Fatalf("raw snapshot = %#v ok=%v", raw, ok)
	}

	if !p.ClearXrayOnlineSnapshot(at.Add(time.Second).UnixMilli(), testPresenceGraceMs) {
		t.Fatal("clearing Xray presence must change the visible union")
	}
	if got, want := p.GetLocalOnlineClients(), []string{"mtproto@example.test"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("local online after clear = %#v, want %#v", got, want)
	}
	if _, ok := p.CachedOnlineUsersSnapshot(time.Minute, at.Add(time.Second)); ok {
		t.Fatal("raw snapshot cache remained valid after clear")
	}
	if _, fresh := p.FreshExactXrayOnlineClients(time.Minute, at.Add(time.Second)); fresh {
		t.Fatal("fresh exact Xray snapshot remained valid after clear")
	}
}

func TestConcurrentAtomicXrayCommitAndAuxiliaryRefresh(t *testing.T) {
	p := NewProcess(&Config{})
	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			at := time.UnixMilli(int64(i + 1))
			p.CommitXrayOnlineSnapshot(
				[]string{"xray@example.test"},
				[]OnlineUser{{Email: "hmstat_1_aaaaaaaaaaaaaaaa"}},
				at,
				at.UnixMilli(),
				testPresenceGraceMs,
			)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			now := int64(i + 1)
			p.RefreshAuxiliaryOnline(
				[]string{"mtproto@example.test"},
				[]string{"mtproto-in"},
				now,
				testPresenceGraceMs,
			)
		}
	}()

	wg.Wait()

	if got, want := p.GetLocalOnlineClients(), []string{
		"mtproto@example.test",
		"xray@example.test",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("local online = %#v, want %#v", got, want)
	}
}
