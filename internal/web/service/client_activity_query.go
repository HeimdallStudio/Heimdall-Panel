package service

import (
	"errors"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
)

const (
	defaultClientActivityPageSize = 100
	maxClientActivityPageSize     = 200
	maxClientActivityPage         = 100000
)

type ClientActivityListItem struct {
	Destination   string `json:"destination"`
	SourceIP      string `json:"sourceIp"`
	UploadBytes   int64  `json:"uploadBytes"`
	DownloadBytes int64  `json:"downloadBytes"`
}

type ClientActivityListResponse struct {
	Enabled    bool                     `json:"enabled"`
	Generation int64                    `json:"generation"`
	DataEpoch  int64                    `json:"dataEpoch"`
	Items      []ClientActivityListItem `json:"items"`
	Total      int64                    `json:"total"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"pageSize"`
}

func normalizeClientActivityPage(
	page int,
	pageSize int,
) (int, int) {
	if page < 1 {
		page = 1
	}
	if page > maxClientActivityPage {
		page = maxClientActivityPage
	}

	if pageSize < 1 {
		pageSize = defaultClientActivityPageSize
	}
	if pageSize > maxClientActivityPageSize {
		pageSize = maxClientActivityPageSize
	}

	return page, pageSize
}

// ListByClientID returns only the client's current data epoch. Rows left from
// an older Reset epoch can therefore never appear in the API response even if
// delayed cleanup or a transaction rollback has preserved them temporarily.
func (s *ClientActivityService) ListByClientID(
	clientID int,
	page int,
	pageSize int,
) (*ClientActivityListResponse, error) {
	page, pageSize = normalizeClientActivityPage(
		page,
		pageSize,
	)

	response := &ClientActivityListResponse{
		DataEpoch: 1,
		Items:     []ClientActivityListItem{},
		Page:      page,
		PageSize:  pageSize,
	}

	if clientID <= 0 {
		return response, nil
	}

	db := database.GetDB()

	var setting model.ClientActivitySetting

	err := db.
		Where("client_id = ?", clientID).
		First(&setting).
		Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return response, nil
	}
	if err != nil {
		return nil, err
	}

	response.Enabled = setting.Enabled
	response.Generation = setting.Generation
	response.DataEpoch = setting.DataEpoch

	baseSQL := `
		FROM (
			SELECT destination, source_ip, upload_bytes, download_bytes, last_seen
			FROM client_activity_destinations
			WHERE client_id = ? AND data_epoch = ?
			UNION ALL
			SELECT destination, source_ip, upload_bytes, download_bytes, last_seen
			FROM client_activity_remote_destinations
			WHERE client_id = ? AND data_epoch = ?
		) AS activity_rows
	`

	countSQL := `SELECT COUNT(*) FROM (` +
		`SELECT destination, source_ip ` + baseSQL +
		` GROUP BY destination, source_ip) AS grouped_activity`

	if err := db.Raw(
		countSQL,
		clientID,
		setting.DataEpoch,
		clientID,
		setting.DataEpoch,
	).Scan(&response.Total).Error; err != nil {
		return nil, err
	}

	offset := (page - 1) * pageSize
	listSQL := `
		SELECT
			destination,
			source_ip,
			SUM(upload_bytes) AS upload_bytes,
			SUM(download_bytes) AS download_bytes
	` + baseSQL + `
		GROUP BY destination, source_ip
		ORDER BY MAX(last_seen) DESC, destination ASC, source_ip ASC
		LIMIT ? OFFSET ?
	`

	if err := db.Raw(
		listSQL,
		clientID,
		setting.DataEpoch,
		clientID,
		setting.DataEpoch,
		pageSize,
		offset,
	).Scan(&response.Items).Error; err != nil {
		return nil, err
	}

	if response.Items == nil {
		response.Items = []ClientActivityListItem{}
	}

	return response, nil
}

// ClientActivityExportResponse is the unpaginated Activity snapshot used only
// by the explicit JSON export path. The normal Activity list remains bounded
// and paginated independently.
type ClientActivityExportResponse struct {
	Enabled    bool                     `json:"enabled"`
	Generation int64                    `json:"generation"`
	DataEpoch  int64                    `json:"dataEpoch"`
	Items      []ClientActivityListItem `json:"items"`
	Total      int64                    `json:"total"`
}

// ExportByClientID returns every logical Activity row stored for the client's
// current data epoch.
//
// Unlike ListByClientID this method intentionally has no page, pageSize, LIMIT,
// or OFFSET. Local and remote rows use the same aggregation contract as the UI:
// equal destination + source_ip pairs are merged and their byte counters added.
func (s *ClientActivityService) ExportByClientID(
	clientID int,
) (*ClientActivityExportResponse, error) {
	response := &ClientActivityExportResponse{
		DataEpoch: 1,
		Items:     []ClientActivityListItem{},
	}

	if clientID <= 0 {
		return response, nil
	}

	db := database.GetDB()

	var setting model.ClientActivitySetting
	err := db.
		Where("client_id = ?", clientID).
		First(&setting).
		Error

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return response, nil
	}
	if err != nil {
		return nil, err
	}

	response.Enabled = setting.Enabled
	response.Generation = setting.Generation
	response.DataEpoch = setting.DataEpoch

	const exportSQL = `
		SELECT
			destination,
			source_ip,
			SUM(upload_bytes) AS upload_bytes,
			SUM(download_bytes) AS download_bytes
		FROM (
			SELECT
				destination,
				source_ip,
				upload_bytes,
				download_bytes,
				last_seen
			FROM client_activity_destinations
			WHERE client_id = ? AND data_epoch = ?

			UNION ALL

			SELECT
				destination,
				source_ip,
				upload_bytes,
				download_bytes,
				last_seen
			FROM client_activity_remote_destinations
			WHERE client_id = ? AND data_epoch = ?
		) AS activity_rows
		GROUP BY destination, source_ip
		ORDER BY
			MAX(last_seen) DESC,
			destination ASC,
			source_ip ASC
	`

	if err := db.Raw(
		exportSQL,
		clientID,
		setting.DataEpoch,
		clientID,
		setting.DataEpoch,
	).Scan(&response.Items).Error; err != nil {
		return nil, err
	}

	if response.Items == nil {
		response.Items = []ClientActivityListItem{}
	}

	response.Total = int64(len(response.Items))

	return response, nil
}
