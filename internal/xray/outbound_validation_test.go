package xray

import (
	"errors"
	"strings"
	"testing"
)

const testVLESSID = "b831381d-6324-4d53-ad4f-8cda48b30811"

func TestValidateOutboundConfig_AllowsCustomCorePublicPlaintextVLESS(t *testing.T) {
	publicPlaintext := `{
		"protocol": "vless",
		"settings": {"address": "1.2.3.4", "port": 443, "id": "` + testVLESSID + `", "encryption": "none"},
		"streamSettings": {"network": "tcp", "security": "none"}
	}`
	if !OutboundRequiresExternalCoreRestart([]byte(publicPlaintext)) {
		t.Fatal("public plaintext VLESS must force a full restart through the external custom core")
	}
	if err := ValidateOutboundConfig([]byte(publicPlaintext)); err != nil {
		t.Fatalf("custom-core-compatible public plaintext VLESS was rejected: %v", err)
	}

	privatePlaintext := `{
		"protocol": "vless",
		"settings": {"address": "10.0.0.1", "port": 443, "id": "` + testVLESSID + `", "encryption": "none"},
		"streamSettings": {"network": "tcp", "security": "none"}
	}`
	if OutboundRequiresExternalCoreRestart([]byte(privatePlaintext)) {
		t.Fatal("private plaintext VLESS is accepted by the embedded builder and must remain hot-applicable")
	}
	if err := ValidateOutboundConfig([]byte(privatePlaintext)); err != nil {
		t.Fatalf("private plaintext VLESS must stay valid: %v", err)
	}

	publicTLS := `{
		"protocol": "vless",
		"settings": {"address": "1.2.3.4", "port": 443, "id": "` + testVLESSID + `", "encryption": "none"},
		"streamSettings": {"network": "tcp", "security": "tls", "tlsSettings": {"serverName": "example.com"}}
	}`
	if OutboundRequiresExternalCoreRestart([]byte(publicTLS)) {
		t.Fatal("TLS-secured public VLESS must remain hot-applicable")
	}
	if err := ValidateOutboundConfig([]byte(publicTLS)); err != nil {
		t.Fatalf("TLS-secured public VLESS must stay valid: %v", err)
	}
}

func TestValidateOutboundConfig_DoesNotMaskOtherFailures(t *testing.T) {
	if err := ValidateOutboundConfig([]byte(`{"protocol":`)); err == nil {
		t.Fatal("malformed outbound JSON must still be rejected")
	}

	unknown := []byte(`{"protocol":"definitely-not-a-protocol","settings":{},"tag":"bad"}`)
	if err := ValidateOutboundConfig(unknown); err == nil {
		t.Fatal("unknown outbound protocol must still be rejected")
	}

	guardErr := errors.New(legacyPublicPlaintextVLESSGuard)
	if isLegacyPublicPlaintextVLESSGuard([]byte(`{"protocol":"trojan"}`), guardErr) {
		t.Fatal("the VLESS compatibility exception must never apply to Trojan")
	}
	if !strings.Contains(guardErr.Error(), "vless without TLS") {
		t.Fatal("test guard text unexpectedly changed")
	}
}
