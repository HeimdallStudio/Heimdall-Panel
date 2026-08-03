package service

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
)

func runtimeProfileInbound(port int, profilePort int) *model.Inbound {
	return &model.Inbound{
		Enable:   true,
		Listen:   "0.0.0.0",
		Port:     port,
		Protocol: model.VLESS,
		Settings: `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{
            "network":"tcp",
            "security":"none",
            "tcpSettings":{"header":{"type":"none"}},
            "externalProxy":[{
                "enabled":true,
                "dest":"",
                "port":` + strconv.Itoa(profilePort) + `,
                "network":"grpc",
                "security":"none",
                "grpcSettings":{"serviceName":"mobile"},
                "runtime":{"id":"mobile-grpc"}
            }]
        }`,
		Sniffing: `{"enabled":false}`,
	}
}

func TestAddInboundRejectsRuntimeProfileSocketConflictBeforePersist(t *testing.T) {
	setupConflictDB(t)
	seedInboundConflict(
		t,
		"in-2087-tcp",
		"0.0.0.0",
		2087,
		model.VLESS,
		`{"network":"tcp","security":"none","tcpSettings":{"header":{"type":"none"}}}`,
		`{"clients":[],"decryption":"none"}`,
	)

	candidate := runtimeProfileInbound(22937, 2087)
	_, _, err := (&InboundService{}).AddInbound(candidate)
	if err == nil || !strings.Contains(err.Error(), "runtime socket") {
		t.Fatalf("AddInbound error = %v, want runtime socket conflict", err)
	}

	var count int64
	if dbErr := database.GetDB().Model(&model.Inbound{}).Where("port = ?", 22937).Count(&count).Error; dbErr != nil {
		t.Fatal(dbErr)
	}
	if count != 0 {
		t.Fatalf("rejected inbound was persisted, count=%d", count)
	}
}

func TestAddInboundRejectsParentSocketOwnedByExistingRuntimeProfile(t *testing.T) {
	setupConflictDB(t)
	existing := runtimeProfileInbound(22937, 2087)
	existing.Tag = "in-22937-tcp"
	if err := database.GetDB().Create(existing).Error; err != nil {
		t.Fatal(err)
	}

	candidate := &model.Inbound{
		Enable:         true,
		Listen:         "0.0.0.0",
		Port:           2087,
		Protocol:       model.VLESS,
		Settings:       `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{"network":"tcp","security":"none","tcpSettings":{"header":{"type":"none"}}}`,
		Sniffing:       `{"enabled":false}`,
	}
	_, _, err := (&InboundService{}).AddInbound(candidate)
	if err == nil || !strings.Contains(err.Error(), "hm-profile-") {
		t.Fatalf("AddInbound error = %v, want conflict with existing synthetic profile", err)
	}
}

func TestAddInboundAllowsRuntimeSocketUsedOnlyByMarkerlessStructuredSubscription(t *testing.T) {
	setupConflictDB(t)
	existing := &model.Inbound{
		Enable:   true,
		Listen:   "0.0.0.0",
		Port:     22937,
		Protocol: model.VLESS,
		Tag:      "pre-automatic-topology",
		Settings: `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{
			"network":"tcp",
			"security":"none",
			"externalProxy":[{
				"enabled":true,
				"port":1995,
				"network":"grpc",
				"grpcSettings":{"serviceName":"legacy-subscription"}
			}]
		}`,
		Sniffing: `{"enabled":false}`,
	}
	if err := database.GetDB().Create(existing).Error; err != nil {
		t.Fatal(err)
	}

	candidate := runtimeProfileInbound(24443, 1995)
	candidate.Tag = "automatic-runtime-owner"
	created, _, err := (&InboundService{}).AddInbound(candidate)
	if err != nil {
		t.Fatalf("markerless structured subscription profile claimed runtime socket 1995: %v", err)
	}
	if created.Id == 0 {
		t.Fatal("created automatic runtime owner has no database id")
	}
}

func TestAddInboundRuntimeProfileRequestsFullReconcile(t *testing.T) {
	setupConflictDB(t)
	candidate := runtimeProfileInbound(22937, 2087)

	created, needRestart, err := (&InboundService{}).AddInbound(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if created.Id == 0 {
		t.Fatal("created inbound has no database id")
	}
	if !needRestart {
		t.Fatal("runtime profile add must request a full Xray reconcile")
	}
}

func TestAddInboundRejectsRuntimeProfileOnLegacyRemoteNode(t *testing.T) {
	setupConflictDB(t)
	node := &model.Node{
		Name:            "legacy-node",
		Address:         "legacy.example.test",
		Port:            443,
		Enable:          true,
		Status:          "online",
		Capabilities:    wirecodec.CapZstd,
		InboundSyncMode: "all",
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}
	candidate := runtimeProfileInbound(22937, 2087)
	candidate.NodeID = &node.Id

	_, _, err := (&InboundService{}).AddInbound(candidate)
	if err == nil || !strings.Contains(err.Error(), wirecodec.CapRuntimeProfilesV1) {
		t.Fatalf("AddInbound error = %v, want remote runtime-profile capability error", err)
	}

	var count int64
	if dbErr := database.GetDB().Model(&model.Inbound{}).Count(&count).Error; dbErr != nil {
		t.Fatal(dbErr)
	}
	if count != 0 {
		t.Fatalf("capability-rejected inbound was persisted, count=%d", count)
	}
}

func TestAddInboundAllowsRuntimeProfileOnCapableOfflineRemoteNode(t *testing.T) {
	setupConflictDB(t)
	node := &model.Node{
		Name:            "capable-node",
		Address:         "capable.example.test",
		Port:            443,
		Enable:          true,
		Status:          "offline",
		Capabilities:    wirecodec.AdvertisedCapabilities,
		InboundSyncMode: "all",
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}

	candidate := runtimeProfileInbound(22937, 2087)
	candidate.NodeID = &node.Id
	created, needRestart, err := (&InboundService{}).AddInbound(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if created.Id == 0 {
		t.Fatal("remote runtime-profile inbound has no database id")
	}
	if needRestart {
		t.Fatal("remote runtime-profile save must not restart the master Xray")
	}

	var storedNode model.Node
	if err := database.GetDB().First(&storedNode, node.Id).Error; err != nil {
		t.Fatal(err)
	}
	if !storedNode.ConfigDirty {
		t.Fatal("offline capable node must be marked dirty for later reconcile")
	}
	if !strings.Contains(created.StreamSettings, `"id":"mobile-grpc"`) {
		t.Fatalf("runtime profile metadata was not preserved: %s", created.StreamSettings)
	}
}

func TestAddInboundRemoteRuntimeTLSPathsAreValidatedOnNodeNotMaster(t *testing.T) {
	setupConflictDB(t)
	node := &model.Node{
		Name:            "tls-node",
		Address:         "tls.example.test",
		Port:            443,
		Enable:          true,
		Status:          "offline",
		Capabilities:    wirecodec.AdvertisedCapabilities,
		InboundSyncMode: "all",
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatal(err)
	}

	candidate := runtimeProfileInbound(22937, 2087)
	candidate.NodeID = &node.Id
	var stream map[string]any
	if err := json.Unmarshal([]byte(candidate.StreamSettings), &stream); err != nil {
		t.Fatal(err)
	}
	profile := stream["externalProxy"].([]any)[0].(map[string]any)
	profile["security"] = "tls"
	runtimeMetadata := profile["runtime"].(map[string]any)
	runtimeMetadata["tlsSettings"] = map[string]any{
		"certificates": []any{map[string]any{
			"certificateFile": "/etc/remote-only/fullchain.pem",
			"keyFile":         "/etc/remote-only/private.key",
		}},
	}
	raw, err := json.Marshal(stream)
	if err != nil {
		t.Fatal(err)
	}
	candidate.StreamSettings = string(raw)

	created, _, err := (&InboundService{}).AddInbound(candidate)
	if err != nil {
		t.Fatalf("master incorrectly required remote certificate files locally: %v", err)
	}
	if created.Id == 0 {
		t.Fatal("remote TLS runtime-profile inbound was not persisted")
	}
}

func TestInboundHasEnabledRuntimeProfiles(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"empty", `{}`, false},
		{"legacy", `{"externalProxy":[{"enabled":true,"dest":"cdn.example.test","port":443}]}`, false},
		{"runtime disabled metadata", `{"externalProxy":[{"enabled":true,"network":"grpc","runtime":{"enabled":false}}]}`, true},
		{"empty runtime marker", `{"externalProxy":[{"enabled":true,"network":"grpc","runtime":{}}]}`, true},
		{"markerless structured profile", `{"externalProxy":[{"enabled":true,"network":"grpc"}]}`, false},
		{"profile disabled", `{"externalProxy":[{"enabled":false,"network":"grpc","runtime":{"enabled":true}}]}`, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := inboundHasEnabledRuntimeProfiles(test.raw); got != test.want {
				t.Fatalf("got %v, want %v", got, test.want)
			}
		})
	}
}

func TestNormalizeAutomaticRuntimeProfilesForSavePersistsStableID(t *testing.T) {
	inbound := &model.Inbound{StreamSettings: `{
		"network":"tcp",
		"externalProxy":[{
			"enabled":true,
			"network":"grpc",
			"runtime":{"enabled":false,"mode":"shared","listen":"127.0.0.1","port":9443}
		}]
	}`}
	changed, err := normalizeAutomaticRuntimeProfilesForSave(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("save normalization reported no change")
	}
	var stream map[string]any
	if err := json.Unmarshal([]byte(inbound.StreamSettings), &stream); err != nil {
		t.Fatal(err)
	}
	profile := stream["externalProxy"].([]any)[0].(map[string]any)
	runtime := profile["runtime"].(map[string]any)
	if runtime["id"] != "auto-profile-1" {
		t.Fatalf("persisted id = %#v", runtime["id"])
	}
	for _, key := range automaticRuntimeTopologyFields {
		if _, exists := runtime[key]; exists {
			t.Fatalf("obsolete key %q persisted: %#v", key, runtime)
		}
	}
	first := inbound.StreamSettings
	changed, err = normalizeAutomaticRuntimeProfilesForSave(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if changed || inbound.StreamSettings != first {
		t.Fatal("save normalization is not stable")
	}
}

func TestNormalizeAutomaticRuntimeProfilesForSaveLeavesMarkerlessStructuredProfileUntouched(t *testing.T) {
	inbound := &model.Inbound{StreamSettings: `{"network":"tcp","externalProxy":[{"enabled":true,"port":1995,"network":"grpc"}]}`}
	before := inbound.StreamSettings
	changed, err := normalizeAutomaticRuntimeProfilesForSave(inbound)
	if err != nil {
		t.Fatal(err)
	}
	if changed || inbound.StreamSettings != before {
		t.Fatalf("markerless structured profile was migrated implicitly: changed=%v stream=%s", changed, inbound.StreamSettings)
	}
}

func TestAddInboundRejectsReservedRuntimeProfileTagPrefix(t *testing.T) {
	setupConflictDB(t)
	candidate := runtimeProfileInbound(22937, 2087)
	candidate.Tag = "hm-profile-manual"

	_, _, err := (&InboundService{}).AddInbound(candidate)
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("AddInbound error = %v, want reserved runtime-profile tag rejection", err)
	}
}
