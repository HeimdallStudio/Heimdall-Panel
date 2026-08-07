package iplimit

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

const (
	testClientGuid = "11111111-2222-3333-4444-555555555555"
	testOtherGuid  = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
)

func initCoordinatorDB(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	t.Setenv("XUI_DB_TYPE", "sqlite")
	t.Setenv("XUI_DB_DSN", "")
	dir := t.TempDir()
	path := filepath.Join(dir, "x-ui.db")
	if err := database.InitDB(path); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
	return database.GetDB(), path
}

func seedLimitedClient(t *testing.T, db *gorm.DB, guid string, limit int) {
	t.Helper()
	row := &model.ClientRecord{
		Email:      guid + "@example.invalid",
		ClientGuid: guid,
		LimitIP:    limit,
		Enable:     true,
	}
	if err := db.Create(row).Error; err != nil {
		t.Fatalf("seed client: %v", err)
	}
}

func TestCoordinatorSameIPAcrossHoldersUsesOneSlot(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c := NewCoordinator(db, time.Minute)

	first, err := c.Acquire(context.Background(), testClientGuid, "203.0.113.10", "node-a")
	if err != nil || !first.Allowed || first.ActiveSlots != 1 || first.Reason != DecisionNewSlot {
		t.Fatalf("first acquire = %#v, err=%v", first, err)
	}
	second, err := c.Acquire(context.Background(), testClientGuid, "203.0.113.10", "node-b")
	if err != nil || !second.Allowed || second.ActiveSlots != 1 || !second.ExistingSlot || second.Reason != DecisionExistingSlot {
		t.Fatalf("same-IP acquire = %#v, err=%v", second, err)
	}

	snapshot, err := c.Snapshot(context.Background(), testClientGuid)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot) != 2 || snapshot[0].IP != "203.0.113.10" || snapshot[1].IP != "203.0.113.10" {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestCoordinatorRenewSameHolderUpdatesLeaseWithoutDuplicateRow(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c := NewCoordinator(db, 10*time.Second)
	now := time.Unix(1_800_000_000, 0).UTC()
	c.now = func() time.Time { return now }
	ctx := context.Background()

	first, err := c.Acquire(ctx, testClientGuid, "203.0.113.15", "node-a")
	if err != nil || !first.Allowed {
		t.Fatalf("first acquire = %#v err=%v", first, err)
	}
	now = now.Add(5 * time.Second)
	second, err := c.Acquire(ctx, testClientGuid, "203.0.113.15", "node-a")
	if err != nil || !second.Allowed || second.ExpiresAt <= first.ExpiresAt {
		t.Fatalf("renew acquire = %#v first=%#v err=%v", second, first, err)
	}

	var rows []model.ClientIPLeaseHolder
	if err := db.Where("client_guid = ?", testClientGuid).Find(&rows).Error; err != nil {
		t.Fatalf("load holder rows: %v", err)
	}
	if len(rows) != 1 || rows[0].ExpiresAt != second.ExpiresAt {
		t.Fatalf("holder rows = %#v", rows)
	}
}

func TestCoordinatorRejectsSecondDistinctIPAtLimitOne(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c := NewCoordinator(db, time.Minute)

	if d, err := c.Acquire(context.Background(), testClientGuid, "203.0.113.10", "node-a"); err != nil || !d.Allowed {
		t.Fatalf("first acquire = %#v, err=%v", d, err)
	}
	d, err := c.Acquire(context.Background(), testClientGuid, "203.0.113.11", "node-b")
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if d.Allowed || d.Reason != DecisionLimitReached || d.ActiveSlots != 1 || d.Limit != 1 {
		t.Fatalf("second acquire = %#v", d)
	}
}

func TestCoordinatorConcurrentDifferentIPRaceGrantsExactlyOne(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c1 := NewCoordinator(db, time.Minute)
	c2 := NewCoordinator(db, time.Minute)

	start := make(chan struct{})
	decisions := make(chan Decision, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, tc := range []struct {
		c      *Coordinator
		ip     string
		holder string
	}{
		{c1, "198.51.100.10", "node-a"},
		{c2, "198.51.100.11", "node-b"},
	} {
		_ = i
		wg.Add(1)
		go func(tc struct {
			c      *Coordinator
			ip     string
			holder string
		}) {
			defer wg.Done()
			<-start
			d, err := tc.c.Acquire(context.Background(), testClientGuid, tc.ip, tc.holder)
			if err != nil {
				errs <- err
				return
			}
			decisions <- d
		}(tc)
	}
	close(start)
	wg.Wait()
	close(decisions)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Acquire: %v", err)
	}

	allowed := 0
	rejected := 0
	for d := range decisions {
		if d.Allowed {
			allowed++
		} else if d.Reason == DecisionLimitReached {
			rejected++
		}
	}
	if allowed != 1 || rejected != 1 {
		t.Fatalf("race results: allowed=%d rejected=%d", allowed, rejected)
	}
}

func TestCoordinatorExpiryFreesSlot(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c := NewCoordinator(db, 10*time.Second)
	now := time.Unix(1_800_000_000, 0).UTC()
	c.now = func() time.Time { return now }

	if d, err := c.Acquire(context.Background(), testClientGuid, "192.0.2.10", "node-a"); err != nil || !d.Allowed {
		t.Fatalf("first acquire = %#v, err=%v", d, err)
	}
	now = now.Add(11 * time.Second)
	d, err := c.Acquire(context.Background(), testClientGuid, "192.0.2.11", "node-b")
	if err != nil || !d.Allowed || d.Reason != DecisionNewSlot || d.ActiveSlots != 1 {
		t.Fatalf("post-expiry acquire = %#v, err=%v", d, err)
	}

	snapshot, err := c.Snapshot(context.Background(), testClientGuid)
	if err != nil || len(snapshot) != 1 || snapshot[0].IP != "192.0.2.11" {
		t.Fatalf("snapshot = %#v, err=%v", snapshot, err)
	}
}

func TestCoordinatorReleaseKeepsSharedSlotUntilFinalHolder(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c := NewCoordinator(db, time.Minute)
	ctx := context.Background()

	_, _ = c.Acquire(ctx, testClientGuid, "203.0.113.50", "node-a")
	_, _ = c.Acquire(ctx, testClientGuid, "203.0.113.50", "node-b")

	removed, err := c.Release(ctx, testClientGuid, "203.0.113.50", "node-a")
	if err != nil || !removed {
		t.Fatalf("release first holder: removed=%v err=%v", removed, err)
	}
	if d, err := c.Acquire(ctx, testClientGuid, "203.0.113.51", "node-c"); err != nil || d.Allowed {
		t.Fatalf("second IP while node-b still holds slot = %#v err=%v", d, err)
	}

	removed, err = c.Release(ctx, testClientGuid, "203.0.113.50", "node-b")
	if err != nil || !removed {
		t.Fatalf("release final holder: removed=%v err=%v", removed, err)
	}
	if d, err := c.Acquire(ctx, testClientGuid, "203.0.113.51", "node-c"); err != nil || !d.Allowed {
		t.Fatalf("second IP after final release = %#v err=%v", d, err)
	}
}

func TestCoordinatorRestartRecoveryUsesPersistedHolders(t *testing.T) {
	db, path := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c1 := NewCoordinator(db, time.Minute)
	if d, err := c1.Acquire(context.Background(), testClientGuid, "198.51.100.20", "node-a"); err != nil || !d.Allowed {
		t.Fatalf("first acquire = %#v err=%v", d, err)
	}

	if err := database.CloseDB(); err != nil {
		t.Fatalf("CloseDB: %v", err)
	}
	if err := database.InitDB(path); err != nil {
		t.Fatalf("reopen InitDB: %v", err)
	}
	db = database.GetDB()
	c2 := NewCoordinator(db, time.Minute)
	d, err := c2.Acquire(context.Background(), testClientGuid, "198.51.100.21", "node-b")
	if err != nil {
		t.Fatalf("post-restart acquire: %v", err)
	}
	if d.Allowed || d.Reason != DecisionLimitReached || d.ActiveSlots != 1 {
		t.Fatalf("post-restart decision = %#v", d)
	}
}

func TestCoordinatorUnlimitedClientDoesNotRetainLeaseState(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c := NewCoordinator(db, time.Minute)
	ctx := context.Background()

	if d, err := c.Acquire(ctx, testClientGuid, "192.0.2.90", "node-a"); err != nil || !d.Allowed {
		t.Fatalf("limited acquire = %#v err=%v", d, err)
	}
	if err := db.Model(&model.ClientRecord{}).Where("client_guid = ?", testClientGuid).Update("limit_ip", 0).Error; err != nil {
		t.Fatalf("set unlimited: %v", err)
	}
	d, err := c.Acquire(ctx, testClientGuid, "192.0.2.91", "node-b")
	if err != nil || !d.Allowed || d.Reason != DecisionUnlimited || d.ActiveSlots != 0 {
		t.Fatalf("unlimited acquire = %#v err=%v", d, err)
	}
	var count int64
	if err := db.Model(&model.ClientIPLeaseHolder{}).Where("client_guid = ?", testClientGuid).Count(&count).Error; err != nil {
		t.Fatalf("count holders: %v", err)
	}
	if count != 0 {
		t.Fatalf("unlimited client retained %d holder rows", count)
	}
}

func TestCoordinatorUnknownGuidFailsClosed(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	c := NewCoordinator(db, time.Minute)
	_, err := c.Acquire(context.Background(), testOtherGuid, "203.0.113.1", "node-a")
	if !errors.Is(err, ErrClientNotFound) {
		t.Fatalf("Acquire error = %v, want ErrClientNotFound", err)
	}
}

func TestCoordinatorLimitTwoAllowsTwoDistinctIPsAndRejectsThird(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 2)
	c := NewCoordinator(db, time.Minute)
	ctx := context.Background()

	for _, ip := range []string{"203.0.113.101", "203.0.113.102"} {
		if d, err := c.Acquire(ctx, testClientGuid, ip, "holder-"+ip); err != nil || !d.Allowed {
			t.Fatalf("Acquire(%s) = %#v err=%v", ip, d, err)
		}
	}
	d, err := c.Acquire(ctx, testClientGuid, "203.0.113.103", "node-c")
	if err != nil || d.Allowed || d.ActiveSlots != 2 || d.Reason != DecisionLimitReached {
		t.Fatalf("third acquire = %#v err=%v", d, err)
	}
}

func TestCoordinatorAmbiguousGuidFailsClosed(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	if err := db.Create(&model.ClientRecord{
		Email:      "duplicate-guid@example.invalid",
		ClientGuid: testClientGuid,
		LimitIP:    1,
		Enable:     true,
	}).Error; err != nil {
		t.Fatalf("seed duplicate guid: %v", err)
	}
	c := NewCoordinator(db, time.Minute)
	_, err := c.Acquire(context.Background(), testClientGuid, "203.0.113.1", "node-a")
	if !errors.Is(err, ErrAmbiguousClientGuid) {
		t.Fatalf("Acquire error = %v, want ErrAmbiguousClientGuid", err)
	}
}

func TestCoordinatorRejectsNonPositiveTTL(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c := NewCoordinator(db, 0)
	_, err := c.Acquire(context.Background(), testClientGuid, "203.0.113.1", "node-a")
	if !errors.Is(err, ErrInvalidLeaseTTL) {
		t.Fatalf("Acquire error = %v, want ErrInvalidLeaseTTL", err)
	}
}

func TestCoordinatorCanonicalizesIPv4MappedIPv6(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 1)
	c := NewCoordinator(db, time.Minute)
	ctx := context.Background()

	if d, err := c.Acquire(ctx, testClientGuid, "::ffff:203.0.113.77", "node-a"); err != nil || !d.Allowed {
		t.Fatalf("mapped acquire = %#v err=%v", d, err)
	}
	d, err := c.Acquire(ctx, testClientGuid, "203.0.113.77", "node-b")
	if err != nil || !d.Allowed || !d.ExistingSlot || d.ActiveSlots != 1 {
		t.Fatalf("canonical acquire = %#v err=%v", d, err)
	}
}

func TestCoordinatorLimitReductionConvergesToOldestSlot(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 2)
	c := NewCoordinator(db, time.Minute)
	now := time.Unix(1_800_000_000, 0).UTC()
	c.now = func() time.Time { return now }
	ctx := context.Background()

	if d, err := c.Acquire(ctx, testClientGuid, "203.0.113.20", "node-old"); err != nil || !d.Allowed {
		t.Fatalf("oldest acquire = %#v err=%v", d, err)
	}
	now = now.Add(time.Millisecond)
	if d, err := c.Acquire(ctx, testClientGuid, "203.0.113.10", "node-new"); err != nil || !d.Allowed {
		t.Fatalf("newer acquire = %#v err=%v", d, err)
	}
	if err := db.Model(&model.ClientRecord{}).
		Where("client_guid = ?", testClientGuid).
		Update("limit_ip", 1).Error; err != nil {
		t.Fatalf("lower limit: %v", err)
	}

	loser, err := c.Acquire(ctx, testClientGuid, "203.0.113.10", "node-new")
	if err != nil {
		t.Fatalf("loser renew: %v", err)
	}
	if loser.Allowed || loser.Reason != DecisionLimitReached || loser.ActiveSlots != 1 || loser.Limit != 1 {
		t.Fatalf("loser renewal after reduction = %#v", loser)
	}

	winner, err := c.Acquire(ctx, testClientGuid, "203.0.113.20", "node-old")
	if err != nil || !winner.Allowed || winner.Reason != DecisionExistingSlot || winner.ActiveSlots != 1 {
		t.Fatalf("winner renewal after reduction = %#v err=%v", winner, err)
	}

	snapshot, err := c.Snapshot(ctx, testClientGuid)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snapshot) != 1 || snapshot[0].IP != "203.0.113.20" {
		t.Fatalf("snapshot after reduction = %#v", snapshot)
	}
}

func TestCoordinatorLimitReductionUsesIPTieBreak(t *testing.T) {
	db, _ := initCoordinatorDB(t)
	seedLimitedClient(t, db, testClientGuid, 2)
	c := NewCoordinator(db, time.Minute)
	now := time.Unix(1_800_000_000, 0).UTC()
	c.now = func() time.Time { return now }
	ctx := context.Background()

	// Same millisecond means age is tied; lexical IP order must decide.
	if d, err := c.Acquire(ctx, testClientGuid, "203.0.113.20", "node-b"); err != nil || !d.Allowed {
		t.Fatalf("first acquire = %#v err=%v", d, err)
	}
	if d, err := c.Acquire(ctx, testClientGuid, "203.0.113.10", "node-a"); err != nil || !d.Allowed {
		t.Fatalf("second acquire = %#v err=%v", d, err)
	}
	if err := db.Model(&model.ClientRecord{}).
		Where("client_guid = ?", testClientGuid).
		Update("limit_ip", 1).Error; err != nil {
		t.Fatalf("lower limit: %v", err)
	}

	// Trigger convergence from the lexically larger IP. It must lose.
	d, err := c.Acquire(ctx, testClientGuid, "203.0.113.20", "node-b")
	if err != nil {
		t.Fatalf("renew after reduction: %v", err)
	}
	if d.Allowed || d.ActiveSlots != 1 || d.Reason != DecisionLimitReached {
		t.Fatalf("tie-break loser = %#v", d)
	}

	snapshot, err := c.Snapshot(ctx, testClientGuid)
	if err != nil || len(snapshot) != 1 || snapshot[0].IP != "203.0.113.10" {
		t.Fatalf("tie-break snapshot = %#v err=%v", snapshot, err)
	}
}
