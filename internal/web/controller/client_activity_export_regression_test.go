package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

type activityExportEnvelope struct {
	Success bool `json:"success"`
	Obj     struct {
		Enabled    bool  `json:"enabled"`
		Generation int64 `json:"generation"`
		DataEpoch  int64 `json:"dataEpoch"`
		Total      int64 `json:"total"`
		Items      []struct {
			Destination   string `json:"destination"`
			SourceIP      string `json:"sourceIp"`
			UploadBytes   int64  `json:"uploadBytes"`
			DownloadBytes int64  `json:"downloadBytes"`
		} `json:"items"`
	} `json:"obj"`
}

func decodeActivityExport(
	t *testing.T,
	recorder *httptest.ResponseRecorder,
) activityExportEnvelope {
	t.Helper()

	if recorder.Code != http.StatusOK {
		t.Fatalf(
			"export status=%d want=%d body=%s",
			recorder.Code,
			http.StatusOK,
			recorder.Body.String(),
		)
	}

	var envelope activityExportEnvelope

	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf(
			"decode export API response: %v; body=%s",
			err,
			recorder.Body.String(),
		)
	}

	if !envelope.Success {
		t.Fatalf(
			"export API success=false; body=%s",
			recorder.Body.String(),
		)
	}

	return envelope
}

func TestClientActivityExportReturnsEveryCurrentEpochRecord(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(
		t.TempDir(),
		"client-activity-full-export.db",
	)

	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() {
		_ = database.CloseDB()
	})

	db := database.GetDB()

	client := model.ClientRecord{
		Email:  "activity-full-export-client",
		Enable: true,
	}
	if err := db.Create(&client).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}

	const (
		dataEpoch   = int64(7)
		generation  = int64(11)
		recordCount = 450
	)

	setting := model.ClientActivitySetting{
		ClientID:   client.Id,
		Enabled:    true,
		Generation: generation,
		DataEpoch:  dataEpoch,
	}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatalf("create setting: %v", err)
	}

	rows := make([]model.ClientActivityDestination, 0, recordCount)

	var wantUpload int64
	var wantDownload int64

	for i := 0; i < recordCount; i++ {
		upload := int64(i + 1)
		download := upload * 2

		wantUpload += upload
		wantDownload += download

		rows = append(rows, model.ClientActivityDestination{
			ClientID:      client.Id,
			DataEpoch:     dataEpoch,
			SourceIP:      fmt.Sprintf("203.0.%d.%d", i/250, (i%250)+1),
			Destination:   fmt.Sprintf("destination-%03d.example", i),
			UploadBytes:   upload,
			DownloadBytes: download,
			LastSeen:      int64(1_000_000 + i),
		})
	}

	if err := db.CreateInBatches(&rows, 100).Error; err != nil {
		t.Fatalf("seed activity rows: %v", err)
	}

	router := gin.New()
	NewClientController(router.Group("/clients"))

	request := httptest.NewRequest(
		http.MethodGet,
		"/clients/"+client.Email+"/activity/export",
		nil,
	)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	envelope := decodeActivityExport(t, recorder)

	if !envelope.Obj.Enabled {
		t.Fatal("export enabled=false, want true")
	}

	if envelope.Obj.Generation != generation {
		t.Fatalf(
			"generation=%d want=%d",
			envelope.Obj.Generation,
			generation,
		)
	}

	if envelope.Obj.DataEpoch != dataEpoch {
		t.Fatalf(
			"dataEpoch=%d want=%d",
			envelope.Obj.DataEpoch,
			dataEpoch,
		)
	}

	if envelope.Obj.Total != recordCount {
		t.Fatalf(
			"total=%d want=%d",
			envelope.Obj.Total,
			recordCount,
		)
	}

	if len(envelope.Obj.Items) != recordCount {
		t.Fatalf(
			"items=%d want=%d",
			len(envelope.Obj.Items),
			recordCount,
		)
	}

	var gotUpload int64
	var gotDownload int64
	seen := make(map[string]bool, recordCount)

	for _, row := range envelope.Obj.Items {
		gotUpload += row.UploadBytes
		gotDownload += row.DownloadBytes

		if row.Destination == "" {
			t.Fatal("export contains empty destination")
		}

		if seen[row.Destination] {
			t.Fatalf(
				"duplicate exported destination %q",
				row.Destination,
			)
		}

		seen[row.Destination] = true
	}

	if gotUpload != wantUpload {
		t.Fatalf(
			"total upload=%d want=%d",
			gotUpload,
			wantUpload,
		)
	}

	if gotDownload != wantDownload {
		t.Fatalf(
			"total download=%d want=%d",
			gotDownload,
			wantDownload,
		)
	}

	for i := 0; i < recordCount; i++ {
		want := fmt.Sprintf("destination-%03d.example", i)

		if !seen[want] {
			t.Fatalf(
				"missing stored activity record %q",
				want,
			)
		}
	}
}

func TestClientActivityExportMergesLocalAndRemoteCurrentEpoch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dbPath := filepath.Join(
		t.TempDir(),
		"client-activity-export-merge.db",
	)

	if err := database.InitDB(dbPath); err != nil {
		t.Fatalf("InitDB: %v", err)
	}
	t.Cleanup(func() {
		_ = database.CloseDB()
	})

	db := database.GetDB()

	client := model.ClientRecord{
		Email:  "activity-export-merge-client",
		Enable: true,
	}
	if err := db.Create(&client).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}

	const currentEpoch = int64(5)

	setting := model.ClientActivitySetting{
		ClientID:   client.Id,
		Enabled:    true,
		Generation: 9,
		DataEpoch:  currentEpoch,
	}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatalf("create setting: %v", err)
	}

	local := model.ClientActivityDestination{
		ClientID:      client.Id,
		DataEpoch:     currentEpoch,
		SourceIP:      "203.0.113.10",
		Destination:   "shared.example",
		UploadBytes:   100,
		DownloadBytes: 200,
		LastSeen:      100,
	}
	if err := db.Create(&local).Error; err != nil {
		t.Fatalf("create local row: %v", err)
	}

	remote := []model.ClientActivityRemoteDestination{
		{
			ClientID:      client.Id,
			DataEpoch:     currentEpoch,
			OriginGUID:    "node-a",
			SourceIP:      "203.0.113.10",
			Destination:   "shared.example",
			UploadBytes:   300,
			DownloadBytes: 400,
			LastSeen:      200,
		},
		{
			ClientID:      client.Id,
			DataEpoch:     currentEpoch,
			OriginGUID:    "node-a",
			SourceIP:      "203.0.113.11",
			Destination:   "remote-only.example",
			UploadBytes:   500,
			DownloadBytes: 600,
			LastSeen:      300,
		},
		{
			ClientID:      client.Id,
			DataEpoch:     currentEpoch - 1,
			OriginGUID:    "node-a",
			SourceIP:      "203.0.113.12",
			Destination:   "stale.example",
			UploadBytes:   9999,
			DownloadBytes: 9999,
			LastSeen:      999,
		},
	}

	if err := db.Create(&remote).Error; err != nil {
		t.Fatalf("create remote rows: %v", err)
	}

	router := gin.New()
	NewClientController(router.Group("/clients"))

	request := httptest.NewRequest(
		http.MethodGet,
		"/clients/"+client.Email+"/activity/export",
		nil,
	)
	recorder := httptest.NewRecorder()

	router.ServeHTTP(recorder, request)

	envelope := decodeActivityExport(t, recorder)

	if envelope.Obj.DataEpoch != currentEpoch {
		t.Fatalf(
			"dataEpoch=%d want=%d",
			envelope.Obj.DataEpoch,
			currentEpoch,
		)
	}

	if envelope.Obj.Total != 2 {
		t.Fatalf(
			"total=%d want=2",
			envelope.Obj.Total,
		)
	}

	if len(envelope.Obj.Items) != 2 {
		t.Fatalf(
			"items=%d want=2",
			len(envelope.Obj.Items),
		)
	}

	type totals struct {
		up   int64
		down int64
	}

	byDestination := make(map[string]totals)

	for _, item := range envelope.Obj.Items {
		byDestination[item.Destination] = totals{
			up:   item.UploadBytes,
			down: item.DownloadBytes,
		}
	}

	shared, ok := byDestination["shared.example"]
	if !ok {
		t.Fatal("shared.example missing")
	}

	if shared.up != 400 || shared.down != 600 {
		t.Fatalf(
			"shared totals=%d/%d want=400/600",
			shared.up,
			shared.down,
		)
	}

	remoteOnly, ok := byDestination["remote-only.example"]
	if !ok {
		t.Fatal("remote-only.example missing")
	}

	if remoteOnly.up != 500 || remoteOnly.down != 600 {
		t.Fatalf(
			"remote-only totals=%d/%d want=500/600",
			remoteOnly.up,
			remoteOnly.down,
		)
	}

	if _, found := byDestination["stale.example"]; found {
		t.Fatal("stale data epoch leaked into export")
	}
}
