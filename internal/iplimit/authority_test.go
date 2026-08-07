package iplimit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAuthorityTokenRoundTripAndTamper(t *testing.T) {
	secret := []byte("01234567890123456789012345678901")
	guid := "11111111-1111-4111-8111-111111111111"
	token, err := MintAuthorityToken(secret, guid)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := VerifyAuthorityToken(secret, token)
	if !ok || got != guid {
		t.Fatalf("VerifyAuthorityToken() = %q,%v", got, ok)
	}
	if syntaxGuid, ok := VerifyAuthorityTokenSyntax(token); !ok || syntaxGuid != guid {
		t.Fatalf("VerifyAuthorityTokenSyntax() = %q,%v", syntaxGuid, ok)
	}
	tampered := token[:len(token)-1] + "0"
	if _, ok := VerifyAuthorityToken(secret, tampered); ok {
		t.Fatal("tampered authority token must be rejected")
	}
}

func TestRelayHolderKeyAnchorsDirectChild(t *testing.T) {
	childA := "11111111-1111-4111-8111-111111111111"
	childB := "22222222-2222-4222-8222-222222222222"
	downstream := "local:33333333-3333-4333-8333-333333333333"
	a1, err := RelayHolderKey(childA, downstream)
	if err != nil {
		t.Fatal(err)
	}
	a2, _ := RelayHolderKey(childA, downstream)
	b, _ := RelayHolderKey(childB, downstream)
	if a1 != a2 {
		t.Fatal("relay holder must be deterministic")
	}
	if a1 == b {
		t.Fatal("different authenticated child must produce a different root holder")
	}
	if !strings.HasPrefix(a1, "path:") || len(a1) > 128 {
		t.Fatalf("unexpected holder format %q", a1)
	}
}

func TestForwardLease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(AuthorityHeader) != "token" {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		var req LeaseRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
			return
		}
		if req.Operation != LeaseAcquire || req.ClientGuid == "" || req.IP == "" {
			t.Errorf("unexpected request: %#v", req)
		}
		_ = json.NewEncoder(w).Encode(LeaseResponse{Allowed: true, Limit: 1, ExpiresAt: 12345})
	}))
	defer srv.Close()

	resp, err := ForwardLease(context.Background(), srv.Client(), srv.URL, "token", LeaseRequest{
		Operation:  LeaseAcquire,
		ClientGuid: "11111111-1111-4111-8111-111111111111",
		IP:         "198.51.100.10",
		HolderKey:  "local:test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Allowed || resp.Limit != 1 || resp.ExpiresAt != 12345 {
		t.Fatalf("response = %#v", resp)
	}
}
