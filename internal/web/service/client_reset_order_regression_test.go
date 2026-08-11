package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

// A quota reset must never expose a state where an exhausted client is enabled
// while its old counters are still at/above the quota.
//
// The traffic poll serializes its DB mutation through the traffic writer, but
// ResetTrafficByEmail historically re-enabled the client before submitting the
// counter reset. That creates an interleaving window:
//
//	enable=true, up+down>=total
//
// where the next traffic poll can immediately disable the client again.
//
// The trigger below makes that transient state observable deterministically
// instead of relying on scheduler timing.
func TestResetTrafficByEmail_ZeroesQuotaBeforeReenable(t *testing.T) {
	setupBulkDB(t)

	clientSvc := &ClientService{}
	inboundSvc := &InboundService{}

	const (
		email = "reset-order-local@x"
		total = int64(1000)
	)

	client := model.Client{
		Email:   email,
		ID:      "11111111-1111-1111-1111-111111111111",
		SubID:   email,
		Enable:  false,
		TotalGB: total,
	}

	// Match the reported shape: one logical client attached to multiple local
	// inbounds on the main panel, with no Node involved.
	inbounds := make([]*model.Inbound, 0, 3)
	for i, port := range []int{53110, 53111, 53112} {
		c := client
		c.ID = []string{
			"11111111-1111-1111-1111-111111111111",
			"22222222-2222-2222-2222-222222222222",
			"33333333-3333-3333-3333-333333333333",
		}[i]

		ib := mkInbound(
			t,
			port,
			model.VLESS,
			clientsSettings(t, []model.Client{c}),
		)
		if err := clientSvc.SyncInbound(nil, ib.Id, []model.Client{c}); err != nil {
			t.Fatalf("seed linkage inbound %d: %v", ib.Id, err)
		}
		inbounds = append(inbounds, ib)
	}

	// Quota has been exceeded and enforcement already disabled the logical
	// client.
	mkTraffic(t, inbounds[0].Id, email, 600, 500, total, 0, false)
	forceRecordDisabled(t, clientSvc, email)

	// Probe any DB write which exposes the exact unsafe intermediate state.
	if err := database.GetDB().Exec(`
		CREATE TABLE reset_order_probe (
			hits INTEGER NOT NULL DEFAULT 0
		)
	`).Error; err != nil {
		t.Fatalf("create reset_order_probe: %v", err)
	}
	if err := database.GetDB().Exec(`
		INSERT INTO reset_order_probe(hits) VALUES (0)
	`).Error; err != nil {
		t.Fatalf("seed reset_order_probe: %v", err)
	}

	if err := database.GetDB().Exec(`
		CREATE TRIGGER detect_depleted_reenable
		BEFORE UPDATE OF enable ON client_traffics
		WHEN
			NEW.email = 'reset-order-local@x'
			AND NEW.enable = 1
			AND NEW.total > 0
			AND (NEW.up + NEW.down) >= NEW.total
		BEGIN
			UPDATE reset_order_probe SET hits = hits + 1;
		END
	`).Error; err != nil {
		t.Fatalf("create detect_depleted_reenable trigger: %v", err)
	}

	if _, err := clientSvc.ResetTrafficByEmail(inboundSvc, email); err != nil {
		t.Fatalf("ResetTrafficByEmail: %v", err)
	}

	var hits int64
	if err := database.GetDB().
		Table("reset_order_probe").
		Select("hits").
		Scan(&hits).Error; err != nil {
		t.Fatalf("read reset_order_probe: %v", err)
	}

	if hits != 0 {
		t.Fatalf(
			"traffic reset exposed exhausted client as enabled before counters were zeroed: hits=%d",
			hits,
		)
	}

	tr := trafficOf(t, email)
	if tr.Up != 0 || tr.Down != 0 {
		t.Fatalf(
			"traffic after reset = up:%d down:%d, want 0/0",
			tr.Up,
			tr.Down,
		)
	}
	if !tr.Enable {
		t.Fatal("client_traffics.enable=false after reset, want true")
	}

	if !recordEnableOf(t, clientSvc, email) {
		t.Fatal("clients.enable=false after reset, want true")
	}

	for _, ib := range inbounds {
		if !jsonClientEnable(t, inboundSvc, ib.Id, email) {
			t.Fatalf(
				"inbound %d settings client remains disabled after reset",
				ib.Id,
			)
		}
	}
}
