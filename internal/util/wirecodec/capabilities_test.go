package wirecodec

import "testing"

func TestHasCapabilityUsesWholeTokens(t *testing.T) {
	raw := "zstd, runtime-profiles-v1, strict-ip-limit-v1\tother"
	if !HasCapability(raw, CapZstd) {
		t.Fatal("zstd capability was not found")
	}
	if !HasCapability(raw, CapRuntimeProfilesV1) {
		t.Fatal("runtime profile capability was not found")
	}
	if !HasCapability(raw, CapStrictIPLimitV1) {
		t.Fatal("strict IP-limit capability was not found")
	}
	if !HasCapability(AdvertisedCapabilities, CapStrictIPLimitV1) {
		t.Fatal("strict IP-limit capability must be advertised by new panels")
	}
	if HasCapability(raw, "runtime") {
		t.Fatal("substring must not be treated as a capability token")
	}
	if HasCapability("", CapZstd) {
		t.Fatal("empty header must not advertise capabilities")
	}
}
