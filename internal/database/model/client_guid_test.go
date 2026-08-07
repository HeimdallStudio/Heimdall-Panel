package model

import "testing"

func TestLegacyClientGuidForEmailDeterministic(t *testing.T) {
	a := LegacyClientGuidForEmail(" Alice@Example.COM ")
	b := LegacyClientGuidForEmail("alice@example.com")
	if a == "" || b == "" {
		t.Fatal("legacy client guid must not be empty")
	}
	if a != b {
		t.Fatalf("case/space normalization changed guid: %q != %q", a, b)
	}
	if got := LegacyClientGuidForEmail("bob@example.com"); got == a {
		t.Fatalf("different logical emails collided: %q", got)
	}
}

func TestClientGuidRoundTripAndMergePreservesCanonical(t *testing.T) {
	const guid = "11111111-2222-4333-8444-555555555555"
	client := Client{Email: "alice@example.com", ClientGuid: guid}
	rec := client.ToRecord()
	if rec.ClientGuid != guid {
		t.Fatalf("ToRecord ClientGuid = %q, want %q", rec.ClientGuid, guid)
	}
	if got := rec.ToClient().ClientGuid; got != guid {
		t.Fatalf("ToClient ClientGuid = %q, want %q", got, guid)
	}

	existing := &ClientRecord{Email: "alice@example.com", ClientGuid: guid}
	incoming := &ClientRecord{Email: "alice@example.com", ClientGuid: "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"}
	MergeClientRecord(existing, incoming)
	if existing.ClientGuid != guid {
		t.Fatalf("merge rotated canonical ClientGuid to %q", existing.ClientGuid)
	}

	legacy := &ClientRecord{Email: "legacy@example.com"}
	MergeClientRecord(legacy, &ClientRecord{Email: "legacy@example.com", ClientGuid: guid})
	if legacy.ClientGuid != guid {
		t.Fatalf("legacy empty ClientGuid did not adopt incoming value: %q", legacy.ClientGuid)
	}
}
