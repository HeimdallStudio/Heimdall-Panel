package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
)

func TestUpdateHeartbeatPersistsCapabilitiesAndKeepsThemWhileOffline(t *testing.T) {
	setupConflictDB(t)
	node := &model.Node{
		Name:    "cap-node",
		Address: "cap.example.test",
		Port:    443,
		Enable:  true,
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}

	svc := &NodeService{}
	if err := svc.UpdateHeartbeat(node.Id, HeartbeatPatch{
		Status:            "online",
		Capabilities:      wirecodec.AdvertisedCapabilities,
		CapabilitiesKnown: true,
	}); err != nil {
		t.Fatal(err)
	}

	var stored model.Node
	if err := database.GetDB().First(&stored, node.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !wirecodec.HasCapability(stored.Capabilities, wirecodec.CapRuntimeProfilesV1) {
		t.Fatalf("stored capabilities = %q", stored.Capabilities)
	}

	if err := svc.UpdateHeartbeat(node.Id, HeartbeatPatch{
		Status:            "offline",
		CapabilitiesKnown: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(&stored, node.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !wirecodec.HasCapability(stored.Capabilities, wirecodec.CapRuntimeProfilesV1) {
		t.Fatalf("offline heartbeat erased last known capability: %q", stored.Capabilities)
	}

	if err := svc.UpdateHeartbeat(node.Id, HeartbeatPatch{
		Status:            "online",
		Capabilities:      wirecodec.CapZstd,
		CapabilitiesKnown: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(&stored, node.Id).Error; err != nil {
		t.Fatal(err)
	}
	if wirecodec.HasCapability(stored.Capabilities, wirecodec.CapRuntimeProfilesV1) {
		t.Fatalf("successful legacy heartbeat did not clear stale capability: %q", stored.Capabilities)
	}
}

func TestProbeCapturesAdvertisedCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(wirecodec.CapsHeader, wirecodec.AdvertisedCapabilities)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"msg":"","obj":{"cpu":1,"mem":{"current":1,"total":2},"xray":{"version":"26.6.22","state":"running","errorMsg":""},"panelVersion":"1.5.0","panelGuid":"test-guid","uptime":10,"netIO":{"up":1,"down":2}}}`))
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host := parsed.Hostname()
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	node := &model.Node{
		Name:                "probe-cap-node",
		Scheme:              "http",
		Address:             host,
		Port:                port,
		BasePath:            "/",
		ApiToken:            "test-token",
		AllowPrivateAddress: true,
	}

	patch, err := (&NodeService{}).Probe(context.Background(), node)
	if err != nil {
		t.Fatal(err)
	}
	if !patch.CapabilitiesKnown {
		t.Fatal("successful probe did not mark capabilities as known")
	}
	if !wirecodec.HasCapability(patch.Capabilities, wirecodec.CapRuntimeProfilesV1) {
		t.Fatalf("probe capabilities = %q", patch.Capabilities)
	}
	if strings.TrimSpace(patch.PanelVersion) == "" {
		t.Fatal("probe did not decode the status envelope")
	}
}
