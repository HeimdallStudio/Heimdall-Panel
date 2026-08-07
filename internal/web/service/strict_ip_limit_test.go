package service

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	coreiplimit "github.com/mhsanaei/3x-ui/v3/internal/iplimit"
)

func setupStrictIPLimitServiceDB(t *testing.T) {
	t.Helper()
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.CloseDB() })
}

func TestStrictIPLimitParentConfigTokenMustTargetSelf(t *testing.T) {
	setupStrictIPLimitServiceDB(t)
	svc := &StrictIPLimitService{}
	selfGuid, err := svc.settingService.GetPanelGuid()
	if err != nil {
		t.Fatal(err)
	}
	secret, err := svc.settingService.GetSecret()
	if err != nil {
		t.Fatal(err)
	}
	good, err := coreiplimit.MintAuthorityToken(secret, selfGuid)
	if err != nil {
		t.Fatal(err)
	}
	cfg := StrictIPLimitParentConfig{URL: "https://parent.example/panel/ip-limit/v1/lease", Token: good, ParentGuid: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", TLSVerifyMode: "system"}
	if err := svc.SetParentConfig(cfg); err != nil {
		t.Fatalf("valid parent config: %v", err)
	}
	stored, err := svc.parentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if stored.URL != cfg.URL || stored.Token != cfg.Token || stored.ParentGuid != cfg.ParentGuid {
		t.Fatalf("stored config = %#v want %#v", stored, cfg)
	}

	other, err := coreiplimit.MintAuthorityToken(secret, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.SetParentConfig(StrictIPLimitParentConfig{URL: cfg.URL, Token: other, ParentGuid: cfg.ParentGuid}); err == nil {
		t.Fatal("token addressed to a different panel must be rejected")
	}
}

func TestStrictIPLimitRootSameIPAcrossDirectChildrenOneSlot(t *testing.T) {
	setupStrictIPLimitServiceDB(t)
	const clientGuid = "11111111-1111-4111-8111-111111111111"
	if err := database.GetDB().Create(&model.ClientRecord{Email: "logical@example.com", ClientGuid: clientGuid, LimitIP: 1, Enable: true}).Error; err != nil {
		t.Fatal(err)
	}
	svc := &StrictIPLimitService{}
	req := coreiplimit.LeaseRequest{Operation: coreiplimit.LeaseAcquire, ClientGuid: clientGuid, IP: "198.51.100.10", HolderKey: "local:leaf"}

	first, err := svc.ResolveRelay(context.Background(), req, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa")
	if err != nil || !first.Allowed || first.ActiveSlots != 1 || first.LeaseTTLMillis <= 0 {
		t.Fatalf("first child = %#v err=%v", first, err)
	}
	second, err := svc.ResolveRelay(context.Background(), req, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err != nil || !second.Allowed || second.ActiveSlots != 1 {
		t.Fatalf("same IP second child = %#v err=%v", second, err)
	}

	req.IP = "203.0.113.20"
	third, err := svc.ResolveRelay(context.Background(), req, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	if third.Allowed || third.Reason != coreiplimit.DecisionLimitReached {
		t.Fatalf("second global IP = %#v", third)
	}
}

func TestStrictIPLimitDirectChildAuthenticationIsBoundToEnabledNode(t *testing.T) {
	setupStrictIPLimitServiceDB(t)
	svc := &StrictIPLimitService{}
	childGuid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	secret, err := svc.settingService.GetSecret()
	if err != nil {
		t.Fatal(err)
	}
	token, err := coreiplimit.MintAuthorityToken(secret, childGuid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateDirectChild(token); err == nil {
		t.Fatal("token without an enabled direct node must be rejected")
	}
	node := &model.Node{Name: "child", Scheme: "https", Address: "child.example", Port: 2053, ApiToken: "token", Enable: true, Guid: childGuid}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	got, err := svc.AuthenticateDirectChild(token)
	if err != nil || got != childGuid {
		t.Fatalf("AuthenticateDirectChild() = %q,%v", got, err)
	}
	if err := database.GetDB().Model(node).Update("enable", false).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateDirectChild(token); err == nil {
		t.Fatal("disabled direct node token must be rejected")
	}
}
