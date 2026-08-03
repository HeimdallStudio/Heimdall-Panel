package wirecodec

import "testing"

func TestHasCapabilityUsesWholeTokens(t *testing.T) {
	raw := "zstd, runtime-profiles-v1\tother"
	if !HasCapability(raw, CapZstd) {
		t.Fatal("zstd capability was not found")
	}
	if !HasCapability(raw, CapRuntimeProfilesV1) {
		t.Fatal("runtime profile capability was not found")
	}
	if HasCapability(raw, "runtime") {
		t.Fatal("substring must not be treated as a capability token")
	}
	if HasCapability("", CapZstd) {
		t.Fatal("empty header must not advertise capabilities")
	}
}
