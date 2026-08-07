package job

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mhsanaei/3x-ui/v3/internal/config"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
)

const (
	clientIPLimitsFileName             = "client-ip-limits.json"
	clientIPLimitsPathEnv              = "XRAY_CLIENT_IP_LIMITS_FILE"
	clientIPLimitReleaseSecondsEnv     = "XUI_IP_LIMIT_RELEASE_SECONDS"
	defaultClientIPLimitReleaseSeconds = 60
	maxClientIPLimitReleaseSeconds     = 24 * 60 * 60
)

type clientIPLimitsFile struct {
	Version        int               `json:"version"`
	ReleaseSeconds int               `json:"releaseSeconds"`
	SocketPath     string            `json:"socketPath,omitempty"`
	Clients        map[string]int    `json:"clients"`
	ClientGuids    map[string]string `json:"clientGuids,omitempty"`
}

type clientIPLimitRow struct {
	Email        string         `gorm:"column:email"`
	LogicalEmail string         `gorm:"column:logical_email"`
	ClientGuid   string         `gorm:"column:client_guid"`
	LimitIP      int            `gorm:"column:limit_ip"`
	InboundID    int            `gorm:"column:inbound_id"`
	Protocol     model.Protocol `gorm:"column:protocol"`
}

// SyncClientIPLimitsJob keeps the Xray core-level IP-limit file synchronized
// with the clients table. Xray reloads this file on its own, so changing a
// client's limit does not require an Xray restart.
type SyncClientIPLimitsJob struct {
	mu   sync.Mutex
	path string
}

func NewSyncClientIPLimitsJob() *SyncClientIPLimitsJob {
	return &SyncClientIPLimitsJob{path: resolveClientIPLimitsPath()}
}

func (j *SyncClientIPLimitsJob) Run() {
	if err := j.Sync(); err != nil {
		logger.Warning("sync core IP limits failed:", err)
	}
}

// Sync writes a deterministic, atomically replaced JSON file. It is exported
// so startup can force an initial synchronization before Xray is launched.
func (j *SyncClientIPLimitsJob) Sync() error {
	j.mu.Lock()
	defer j.mu.Unlock()

	rows, err := loadClientIPLimitRows()
	if err != nil {
		return err
	}

	data, err := buildClientIPLimitsJSON(rows, clientIPLimitReleaseSeconds())
	if err != nil {
		return err
	}

	changed, err := writeFileAtomicallyIfChanged(j.path, data, 0o600)
	if err != nil {
		return err
	}

	if changed {
		logger.Infof("core IP limits synchronized: %d limited clients -> %s", len(rows), j.path)
	}

	return nil
}

func loadClientIPLimitRows() ([]clientIPLimitRow, error) {
	db := database.GetDB()
	if db == nil {
		return nil, errors.New("database is not initialized")
	}

	var rows []clientIPLimitRow
	err := db.Table("clients AS clients").
		Select("client_inbound_traffics.stat_email AS email, clients.email AS logical_email, clients.client_guid, clients.limit_ip, inbounds.id AS inbound_id, inbounds.protocol AS protocol").
		Joins("JOIN client_inbounds ON client_inbounds.client_id = clients.id").
		Joins("JOIN inbounds ON inbounds.id = client_inbounds.inbound_id").
		Joins("LEFT JOIN client_inbound_traffics ON client_inbound_traffics.client_id = clients.id AND client_inbound_traffics.inbound_id = client_inbounds.inbound_id").
		Where("clients.enable = ? AND inbounds.enable = ? AND clients.limit_ip > ?", true, true, 0).
		Order("email ASC").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	for i := range rows {
		rows[i].Email = runtimeEmailForClientIPLimitRow(rows[i])
	}

	return normalizeClientIPLimitRows(rows), nil
}

func runtimeEmailForClientIPLimitRow(row clientIPLimitRow) string {
	if email := strings.TrimSpace(row.Email); email != "" {
		return email
	}
	inbound := model.Inbound{Id: row.InboundID, Protocol: row.Protocol}
	return model.RuntimeClientEmailForInbound(&inbound, strings.TrimSpace(row.LogicalEmail))
}

func normalizeClientIPLimitRows(rows []clientIPLimitRow) []clientIPLimitRow {
	byEmail := make(map[string]clientIPLimitRow, len(rows))
	for _, row := range rows {
		email := strings.TrimSpace(row.Email)
		guid := strings.TrimSpace(row.ClientGuid)
		if email == "" || row.LimitIP <= 0 {
			continue
		}

		row.Email = email
		row.ClientGuid = guid
		previous, exists := byEmail[email]
		if !exists || row.LimitIP > previous.LimitIP {
			byEmail[email] = row
		}
	}

	normalized := make([]clientIPLimitRow, 0, len(byEmail))
	for _, row := range byEmail {
		normalized = append(normalized, row)
	}

	sort.Slice(normalized, func(i, k int) bool {
		return normalized[i].Email < normalized[k].Email
	})

	return normalized
}

func buildClientIPLimitsJSON(rows []clientIPLimitRow, releaseSeconds int) ([]byte, error) {
	if releaseSeconds <= 0 {
		releaseSeconds = defaultClientIPLimitReleaseSeconds
	}

	clients := make(map[string]int, len(rows))
	clientGuids := make(map[string]string, len(rows))
	for _, row := range normalizeClientIPLimitRows(rows) {
		clients[row.Email] = row.LimitIP
		if row.ClientGuid != "" {
			clientGuids[row.Email] = row.ClientGuid
		}
	}

	payload := clientIPLimitsFile{
		Version:        2,
		ReleaseSeconds: releaseSeconds,
		SocketPath:     resolveStrictIPLimitSocketPath(),
		Clients:        clients,
		ClientGuids:    clientGuids,
	}

	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}

	return append(data, '\n'), nil
}

func resolveClientIPLimitsPath() string {
	if configured := strings.TrimSpace(os.Getenv(clientIPLimitsPathEnv)); configured != "" {
		return filepath.Clean(configured)
	}

	binFolder := config.GetBinFolderPath()
	if !filepath.IsAbs(binFolder) {
		// Resolve a relative XUI_BIN_FOLDER from the panel executable rather
		// than the caller's current directory. In the standard installation
		// this produces /usr/local/x-ui/bin even when x-ui is launched by a
		// wrapper or from a different shell directory.
		if executable, err := os.Executable(); err == nil {
			binFolder = filepath.Join(filepath.Dir(executable), binFolder)
		} else if absolute, absErr := filepath.Abs(binFolder); absErr == nil {
			binFolder = absolute
		}
	}

	return filepath.Join(filepath.Clean(binFolder), clientIPLimitsFileName)
}

func resolveStrictIPLimitSocketPath() string {
	if configured := strings.TrimSpace(os.Getenv(strictIPLimitSocketEnv)); configured != "" {
		return filepath.Clean(configured)
	}
	return defaultStrictIPLimitSocketPath
}

func clientIPLimitReleaseSeconds() int {
	raw := strings.TrimSpace(os.Getenv(clientIPLimitReleaseSecondsEnv))
	if raw == "" {
		return defaultClientIPLimitReleaseSeconds
	}

	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 || seconds > maxClientIPLimitReleaseSeconds {
		logger.Warningf(
			"invalid %s=%q; using %d seconds",
			clientIPLimitReleaseSecondsEnv,
			raw,
			defaultClientIPLimitReleaseSeconds,
		)
		return defaultClientIPLimitReleaseSeconds
	}

	return seconds
}

func writeFileAtomicallyIfChanged(path string, data []byte, mode os.FileMode) (bool, error) {
	path = filepath.Clean(path)
	if path == "." || path == string(filepath.Separator) {
		return false, fmt.Errorf("invalid client IP limits path: %q", path)
	}

	if current, err := os.ReadFile(path); err == nil {
		if bytes.Equal(current, data) {
			return false, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}

	temp, err := os.CreateTemp(dir, ".client-ip-limits-*.tmp")
	if err != nil {
		return false, err
	}
	tempPath := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()

	if err := temp.Chmod(mode); err != nil {
		_ = temp.Close()
		return false, err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, err
	}
	if err := temp.Close(); err != nil {
		return false, err
	}

	if err := os.Rename(tempPath, path); err != nil {
		return false, err
	}
	removeTemp = false

	return true, nil
}
