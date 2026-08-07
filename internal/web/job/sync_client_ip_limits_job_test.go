package job

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestBuildClientIPLimitsJSON(t *testing.T) {
	data, err := buildClientIPLimitsJSON([]clientIPLimitRow{
		{Email: " beta@example.com ", LimitIP: 2},
		{Email: "alpha@example.com", LimitIP: 1},
		{Email: "disabled@example.com", LimitIP: 0},
		{Email: "alpha@example.com", LimitIP: 3},
		{Email: "", LimitIP: 9},
	}, 75)
	if err != nil {
		t.Fatal(err)
	}

	var got clientIPLimitsFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, data)
	}

	wantClients := map[string]int{
		"alpha@example.com": 3,
		"beta@example.com":  2,
	}
	if got.Version != 2 {
		t.Fatalf("version = %d, want 2", got.Version)
	}
	if got.SocketPath != defaultStrictIPLimitSocketPath {
		t.Fatalf("socketPath = %q, want %q", got.SocketPath, defaultStrictIPLimitSocketPath)
	}
	if got.ReleaseSeconds != 75 {
		t.Fatalf("releaseSeconds = %d, want 75", got.ReleaseSeconds)
	}
	if !reflect.DeepEqual(got.Clients, wantClients) {
		t.Fatalf("clients = %#v, want %#v", got.Clients, wantClients)
	}
}

func TestBuildClientIPLimitsJSONCarriesStableClientGuid(t *testing.T) {
	const guid = "11111111-1111-4111-8111-111111111111"
	data, err := buildClientIPLimitsJSON([]clientIPLimitRow{{Email: "hmstat_1_test", ClientGuid: guid, LimitIP: 1}}, 60)
	if err != nil {
		t.Fatal(err)
	}
	var got clientIPLimitsFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Clients["hmstat_1_test"] != 1 {
		t.Fatalf("clients = %#v", got.Clients)
	}
	if got.ClientGuids["hmstat_1_test"] != guid {
		t.Fatalf("clientGuids = %#v", got.ClientGuids)
	}
}

func TestBuildClientIPLimitsJSONUsesDefaultReleaseSeconds(t *testing.T) {
	data, err := buildClientIPLimitsJSON(nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	var got clientIPLimitsFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.ReleaseSeconds != defaultClientIPLimitReleaseSeconds {
		t.Fatalf("releaseSeconds = %d, want %d", got.ReleaseSeconds, defaultClientIPLimitReleaseSeconds)
	}
	if got.Clients == nil {
		t.Fatal("clients must be encoded as an empty object, not null")
	}
}

func TestWriteFileAtomicallyIfChanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", clientIPLimitsFileName)
	first := []byte("first\n")
	second := []byte("second\n")

	changed, err := writeFileAtomicallyIfChanged(path, first, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("first write must report changed")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}

	changed, err = writeFileAtomicallyIfChanged(path, first, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("identical data must not rewrite the file")
	}

	changed, err = writeFileAtomicallyIfChanged(path, second, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("different data must report changed")
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(second) {
		t.Fatalf("content = %q, want %q", got, second)
	}
}

func TestRuntimeEmailForClientIPLimitRowUsesDeterministicFallback(t *testing.T) {
	row := clientIPLimitRow{
		LogicalEmail: " logical@example.com ",
		InboundID:    42,
		Protocol:     model.VLESS,
	}
	want := model.RuntimeClientEmailForInbound(&model.Inbound{Id: 42, Protocol: model.VLESS}, "logical@example.com")
	if got := runtimeEmailForClientIPLimitRow(row); got != want {
		t.Fatalf("VLESS fallback runtime email=%q want %q", got, want)
	}

	row.Email = " hmstat_existing "
	if got := runtimeEmailForClientIPLimitRow(row); got != "hmstat_existing" {
		t.Fatalf("existing stat email=%q want hmstat_existing", got)
	}

	row.Email = ""
	row.Protocol = model.WireGuard
	if got := runtimeEmailForClientIPLimitRow(row); got != "logical@example.com" {
		t.Fatalf("WireGuard fallback runtime email=%q want logical@example.com", got)
	}
}
