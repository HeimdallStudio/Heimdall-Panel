package runtime

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/util/wirecodec"
)

func remoteRuntimeProfileInbound() *model.Inbound {
	return &model.Inbound{
		Tag:      "n7-vless-20206",
		Port:     20206,
		Protocol: model.VLESS,
		Settings: `{"clients":[],"decryption":"none"}`,
		StreamSettings: `{
			"network":"tcp",
			"security":"none",
			"externalProxy":[{
				"enabled":true,
				"port":24443,
				"network":"grpc",
				"security":"none",
				"grpcSettings":{"serviceName":"mobile"},
				"runtime":{"id":"mobile-grpc"}
			}]
		}`,
		Sniffing: `{"enabled":false}`,
	}
}

func TestRemoteRuntimeProfilesRequireAdvertisedCapability(t *testing.T) {
	legacy := NewRemote(&model.Node{
		Id:           7,
		Name:         "legacy",
		Capabilities: wirecodec.CapZstd,
	}, nil)
	if err := legacy.requireRuntimeProfileCapability(remoteRuntimeProfileInbound()); err == nil ||
		!strings.Contains(err.Error(), wirecodec.CapRuntimeProfilesV1) {
		t.Fatalf("legacy capability check error = %v", err)
	}

	capable := NewRemote(&model.Node{
		Id:           7,
		Name:         "capable",
		Capabilities: wirecodec.AdvertisedCapabilities,
	}, nil)
	if err := capable.requireRuntimeProfileCapability(remoteRuntimeProfileInbound()); err != nil {
		t.Fatalf("capable node rejected runtime profiles: %v", err)
	}
}

func TestWireInboundPreservesRemoteRuntimeProfileMetadata(t *testing.T) {
	payload := wireInbound(remoteRuntimeProfileInbound(), 7)
	var stream map[string]any
	if err := json.Unmarshal([]byte(payload.Get("streamSettings")), &stream); err != nil {
		t.Fatal(err)
	}
	entries, ok := stream["externalProxy"].([]any)
	if !ok || len(entries) != 1 {
		t.Fatalf("externalProxy = %#v", stream["externalProxy"])
	}
	profile := entries[0].(map[string]any)
	runtimeMetadata := profile["runtime"].(map[string]any)
	if runtimeMetadata["id"] != "mobile-grpc" {
		t.Fatalf("runtime metadata was changed: %#v", runtimeMetadata)
	}
	if payload.Get("tag") != "vless-20206" {
		t.Fatalf("remote tag = %q, want prefix stripped", payload.Get("tag"))
	}
}

func TestSanitizeStreamSettingsForRemoteCoversRuntimeProfileTLS(t *testing.T) {
	input := `{
		"network":"tcp",
		"externalProxy":[{
			"enabled":true,
			"network":"grpc",
			"runtime":{
				"id":"tls-profile",
				"tlsSettings":{
					"certificates":[{
						"certificateFile":"/master/fullchain.pem",
						"keyFile":"/master/private.key",
						"certificate":["CERT"],
						"key":["KEY"]
					}]
				}
			}
		}]
	}`
	got := sanitizeStreamSettingsForRemote(input)
	var stream map[string]any
	if err := json.Unmarshal([]byte(got), &stream); err != nil {
		t.Fatal(err)
	}
	profile := stream["externalProxy"].([]any)[0].(map[string]any)
	runtimeMetadata := profile["runtime"].(map[string]any)
	tlsSettings := runtimeMetadata["tlsSettings"].(map[string]any)
	certificate := tlsSettings["certificates"].([]any)[0].(map[string]any)
	if _, exists := certificate["certificateFile"]; exists {
		t.Fatal("runtime profile certificateFile was not stripped")
	}
	if _, exists := certificate["keyFile"]; exists {
		t.Fatal("runtime profile keyFile was not stripped")
	}
	if certificate["certificate"] == nil || certificate["key"] == nil {
		t.Fatal("inline runtime profile TLS material was removed")
	}
}
