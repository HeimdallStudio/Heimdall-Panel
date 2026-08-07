package database

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestBackfillClientGuidsUpdatesCanonicalRowAndStoredInbound(t *testing.T) {
	dbDir := t.TempDir()
	t.Setenv("XUI_DB_FOLDER", dbDir)
	if err := InitDB(filepath.Join(dbDir, "x-ui.db")); err != nil {
		t.Fatalf("InitDB failed: %v", err)
	}
	t.Cleanup(func() { _ = CloseDB() })

	const email = "legacy-guid@example.com"
	rec := &model.ClientRecord{Email: email, SubID: "legacy-guid-sub", Enable: true}
	if err := db.Create(rec).Error; err != nil {
		t.Fatalf("create legacy client: %v", err)
	}
	settings, err := json.Marshal(map[string]any{
		"clients": []any{map[string]any{
			"id":     "aaaaaaaa-1111-4222-8333-bbbbbbbbbbbb",
			"email":  email,
			"subId":  "legacy-guid-sub",
			"enable": true,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ib := &model.Inbound{UserId: 1, Port: 39001, Protocol: model.VLESS, Tag: "legacy-guid-in", Settings: string(settings)}
	if err := db.Create(ib).Error; err != nil {
		t.Fatalf("create inbound: %v", err)
	}
	if err := db.Where("seeder_name = ?", "ClientGuidBackfill").Delete(&model.HistoryOfSeeders{}).Error; err != nil {
		t.Fatalf("clear ClientGuidBackfill history: %v", err)
	}

	if err := backfillClientGuids(); err != nil {
		t.Fatalf("backfillClientGuids: %v", err)
	}

	var got model.ClientRecord
	if err := db.First(&got, rec.Id).Error; err != nil {
		t.Fatalf("reload client: %v", err)
	}
	want := model.LegacyClientGuidForEmail(email)
	if got.ClientGuid != want {
		t.Fatalf("client_guid = %q, want %q", got.ClientGuid, want)
	}

	var gotInbound model.Inbound
	if err := db.First(&gotInbound, ib.Id).Error; err != nil {
		t.Fatalf("reload inbound: %v", err)
	}
	var parsed struct {
		Clients []model.Client `json:"clients"`
	}
	if err := json.Unmarshal([]byte(gotInbound.Settings), &parsed); err != nil {
		t.Fatalf("decode inbound settings: %v", err)
	}
	if len(parsed.Clients) != 1 || parsed.Clients[0].ClientGuid != want {
		t.Fatalf("stored settings ClientGuid = %+v, want %q", parsed.Clients, want)
	}

	var history int64
	if err := db.Model(&model.HistoryOfSeeders{}).Where("seeder_name = ?", "ClientGuidBackfill").Count(&history).Error; err != nil {
		t.Fatalf("count history: %v", err)
	}
	if history != 1 {
		t.Fatalf("ClientGuidBackfill history rows = %d, want 1", history)
	}
}
