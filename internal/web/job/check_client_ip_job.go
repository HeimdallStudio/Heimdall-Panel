package job

import (
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"

	"gorm.io/gorm"
)

// IPWithTimestamp tracks an IP address with its last seen timestamp
type IPWithTimestamp struct {
	IP        string `json:"ip"`
	Timestamp int64  `json:"timestamp"`
}

// CheckClientIpJob records online client IP observations for panel display
// and cross-node attribution. Simultaneous-IP enforcement remains entirely
// inside Heimdall's custom Xray Core.
type CheckClientIpJob struct {
	xrayService service.XrayService
}

var job *CheckClientIpJob

const ipScanChunk = 400

// NewCheckClientIpJob creates a new client IP monitoring job instance.
func NewCheckClientIpJob() *CheckClientIpJob {
	job = new(CheckClientIpJob)
	return job
}

func (j *CheckClientIpJob) Run() {
	observed, apiMode := j.collectFromOnlineAPI()
	if !apiMode {
		logger.Debug("[LimitIP] online-stats API unavailable this run; skipping")
		return
	}

	// Heimdall's custom Xray Core enforces simultaneous-IP limits directly
	// from client-ip-limits.json. This compatibility job only records online
	// IPs for panel display and node attribution.
	j.processObserved(observed, true)
}

// collectFromOnlineAPI builds per-email IP observations (email -> ip ->
// last-seen unix seconds) from the core's online-stats API. ok=false means the
// shared snapshot is unavailable or stale, and the caller skips the run.
// ClientPresenceJob owns the direct RPC poll.
func (j *CheckClientIpJob) collectFromOnlineAPI() (map[string]map[string]int64, bool) {
	onlineUsers, ok := j.xrayService.GetCachedOnlineUsers()
	if !ok {
		return nil, false
	}
	now := time.Now().Unix()
	observed := make(map[string]map[string]int64, len(onlineUsers))
	for _, user := range onlineUsers {
		for _, entry := range user.IPs {
			// No localhost guard needed here: the core's OnlineMap.AddIP drops
			// 127.0.0.1/[::1] itself, so they never reach this list.
			ts := entry.LastSeen
			if ts <= 0 {
				ts = now
			}
			if _, exists := observed[user.Email]; !exists {
				observed[user.Email] = make(map[string]int64)
			}
			if existing, seen := observed[user.Email][entry.IP]; !seen || ts > existing {
				observed[user.Email][entry.IP] = ts
			}
		}
	}
	return observed, true
}

func (j *CheckClientIpJob) resolveObservedRuntimeEmails(
	observed map[string]map[string]int64,
) map[string]map[string]int64 {
	if len(observed) == 0 {
		return observed
	}

	emails := make([]string, 0, len(observed))
	for email := range observed {
		emails = append(emails, email)
	}
	sort.Strings(emails)

	logicalByRuntime := make(map[string]string, len(emails))
	for _, batch := range chunkEmails(emails, ipScanChunk) {
		var rows []struct {
			StatEmail string `gorm:"column:stat_email"`
			Email     string `gorm:"column:email"`
		}

		if err := database.GetDB().
			Model(&model.ClientInboundTraffic{}).
			Select("stat_email, email").
			Where("stat_email IN ?", batch).
			Find(&rows).
			Error; err != nil {
			logger.Debug(
				"[LimitIP] resolve runtime observed emails failed:",
				err,
			)
			return observed
		}

		for _, row := range rows {
			if row.StatEmail == "" || row.Email == "" {
				continue
			}
			logicalByRuntime[row.StatEmail] = row.Email
		}
	}

	if len(logicalByRuntime) == 0 {
		return observed
	}

	resolved := make(
		map[string]map[string]int64,
		len(observed),
	)

	for runtimeEmail, ipTimestamps := range observed {
		logicalEmail := runtimeEmail
		if mapped, ok := logicalByRuntime[runtimeEmail]; ok {
			logicalEmail = mapped
		}

		if _, exists := resolved[logicalEmail]; !exists {
			resolved[logicalEmail] = make(
				map[string]int64,
				len(ipTimestamps),
			)
		}

		for ip, timestamp := range ipTimestamps {
			current, seen := resolved[logicalEmail][ip]
			if !seen || timestamp > current {
				resolved[logicalEmail][ip] = timestamp
			}
		}
	}

	return resolved
}

func chunkEmails(items []string, size int) [][]string {
	if len(items) == 0 {
		return nil
	}
	if size <= 0 {
		return [][]string{items}
	}

	chunks := make(
		[][]string,
		0,
		(len(items)+size-1)/size,
	)

	for len(items) > size {
		chunks = append(chunks, items[:size])
		items = items[size:]
	}

	return append(chunks, items)
}

func (j *CheckClientIpJob) loadInboundsByEmails(
	emails []string,
) (map[string]*model.Inbound, error) {
	db := database.GetDB()

	minInboundByEmail := make(map[string]int, len(emails))

	for _, batch := range chunkEmails(emails, ipScanChunk) {
		var pairs []struct {
			Email     string
			InboundID int `gorm:"column:inbound_id"`
		}

		if err := db.Table("client_inbounds").
			Select(
				"clients.email AS email, "+
					"client_inbounds.inbound_id AS inbound_id",
			).
			Joins(
				"JOIN clients "+
					"ON clients.id = client_inbounds.client_id",
			).
			Where("clients.email IN ?", batch).
			Scan(&pairs).
			Error; err != nil {
			return nil, err
		}

		for _, pair := range pairs {
			current, exists := minInboundByEmail[pair.Email]
			if !exists || pair.InboundID < current {
				minInboundByEmail[pair.Email] = pair.InboundID
			}
		}
	}

	uniqueIDs := make(
		map[int]struct{},
		len(minInboundByEmail),
	)
	ids := make([]int, 0, len(minInboundByEmail))

	for _, id := range minInboundByEmail {
		if _, exists := uniqueIDs[id]; exists {
			continue
		}
		uniqueIDs[id] = struct{}{}
		ids = append(ids, id)
	}

	sort.Ints(ids)

	inboundsByID := make(map[int]*model.Inbound, len(ids))

	for start := 0; start < len(ids); start += ipScanChunk {
		end := start + ipScanChunk
		if end > len(ids) {
			end = len(ids)
		}

		var page []*model.Inbound
		if err := db.
			Where("id IN ?", ids[start:end]).
			Find(&page).
			Error; err != nil {
			return nil, err
		}

		for _, inbound := range page {
			inboundsByID[inbound.Id] = inbound
		}
	}

	result := make(
		map[string]*model.Inbound,
		len(minInboundByEmail),
	)

	for email, id := range minInboundByEmail {
		if inbound, exists := inboundsByID[id]; exists {
			result[email] = inbound
		}
	}

	return result, nil
}

func (j *CheckClientIpJob) loadClientIPRows(
	emails []string,
) (map[string]*model.InboundClientIps, error) {
	db := database.GetDB()
	result := make(
		map[string]*model.InboundClientIps,
		len(emails),
	)

	for _, batch := range chunkEmails(emails, ipScanChunk) {
		var rows []model.InboundClientIps

		if err := db.
			Where("client_email IN ?", batch).
			Find(&rows).
			Error; err != nil {
			return nil, err
		}

		for index := range rows {
			result[rows[index].ClientEmail] = &rows[index]
		}
	}

	return result, nil
}

// processObserved persists one online-IP scan using chunked reads and one
// write transaction. It never performs IP-limit enforcement or disconnects;
// those responsibilities belong exclusively to the custom Xray Core.
func (j *CheckClientIpJob) processObserved(
	observed map[string]map[string]int64,
	observedAreLive bool,
) bool {
	if len(observed) == 0 {
		return false
	}

	now := time.Now().Unix()
	observed = j.resolveObservedRuntimeEmails(observed)

	emails := make([]string, 0, len(observed))
	for email := range observed {
		emails = append(emails, email)
	}
	sort.Strings(emails)

	inboundByEmail, err := j.loadInboundsByEmails(emails)
	if err != nil {
		logger.Debug(
			"[LimitIP] batch inbound lookup failed; "+
				"using exact fallback:",
			err,
		)
		inboundByEmail = make(map[string]*model.Inbound)
	}

	ipRowByEmail, err := j.loadClientIPRows(emails)
	if err != nil {
		j.checkError(err)
		return false
	}

	attribution := make(
		map[string][]model.ClientIpEntry,
		len(observed),
	)

	db := database.GetDB()
	tx := db.Begin()
	if tx.Error != nil {
		j.checkError(tx.Error)
		return false
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback().Error
		}
	}()

	for _, email := range emails {
		if _, linked := inboundByEmail[email]; !linked {
			if _, lookupErr := j.getInboundByEmail(email); lookupErr != nil {
				if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
					logger.Debugf(
						"[LimitIP] skipping stale observed email %q "+
							"(renamed or deleted)",
						email,
					)

					if deleteErr := j.deleteInboundClientIPs(
						tx,
						email,
					); deleteErr != nil {
						j.checkError(deleteErr)
						return false
					}
				} else {
					j.checkError(lookupErr)
				}

				continue
			}
		}

		ipTimestamps := observed[email]
		ips := make([]string, 0, len(ipTimestamps))
		for ip := range ipTimestamps {
			ips = append(ips, ip)
		}
		sort.Strings(ips)

		ipsWithTime := make(
			[]IPWithTimestamp,
			0,
			len(ips),
		)
		attrEntries := make(
			[]model.ClientIpEntry,
			0,
			len(ips),
		)

		for _, ip := range ips {
			timestamp := ipTimestamps[ip]

			ipsWithTime = append(
				ipsWithTime,
				IPWithTimestamp{
					IP:        ip,
					Timestamp: timestamp,
				},
			)

			attributionTimestamp := timestamp
			if observedAreLive {
				attributionTimestamp = now
			}

			attrEntries = append(
				attrEntries,
				model.ClientIpEntry{
					IP:        ip,
					Timestamp: attributionTimestamp,
				},
			)
		}

		if len(attrEntries) > 0 {
			attribution[email] = attrEntries
		}

		encoded, encodeErr := json.Marshal(ipsWithTime)
		if encodeErr != nil {
			j.checkError(encodeErr)
			return false
		}

		record, exists := ipRowByEmail[email]
		if !exists {
			record = &model.InboundClientIps{
				ClientEmail: email,
			}
		}

		record.Ips = string(encoded)

		if saveErr := tx.Save(record).Error; saveErr != nil {
			j.checkError(saveErr)
			return false
		}
	}

	if err := tx.Commit().Error; err != nil {
		j.checkError(err)
		return false
	}
	committed = true

	j.recordLocalAttribution(attribution)
	return false
}

// recordLocalAttribution stores this scan's local observations under this panel's
// own guid so a parent panel can attribute each IP to the node it is on.
// Best-effort: attribution is advisory and must never block IP observation.
func (j *CheckClientIpJob) recordLocalAttribution(attribution map[string][]model.ClientIpEntry) {
	if len(attribution) == 0 {
		return
	}
	guid, err := (&service.SettingService{}).GetPanelGuid()
	if err != nil || guid == "" {
		return
	}
	if err := (&service.InboundService{}).RecordLocalClientIps(guid, attribution); err != nil {
		logger.Debug("[LimitIP] record local ip attribution failed:", err)
	}
}

func (j *CheckClientIpJob) checkError(e error) {
	if e != nil {
		logger.Warning("client ip job err:", e)
	}
}

func (j *CheckClientIpJob) deleteInboundClientIPs(
	tx *gorm.DB,
	clientEmail string,
) error {
	return tx.
		Where("client_email = ?", clientEmail).
		Delete(&model.InboundClientIps{}).
		Error
}

// getInboundByEmail resolves the inbound that owns a client email. It prefers
// the exact clients/client_inbounds relation; a substring "settings LIKE
// %email%" can match the wrong inbound (an email that is a substring of another,
// or text that merely appears elsewhere in the settings JSON). The LIKE + JSON
// scan stays only as a fallback for clients not yet present in the relation, so
// nothing regresses when the join finds no row.
func (j *CheckClientIpJob) getInboundByEmail(clientEmail string) (*model.Inbound, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}

	err := db.Model(&model.Inbound{}).
		Joins("JOIN client_inbounds ON client_inbounds.inbound_id = inbounds.id").
		Joins("JOIN clients ON clients.id = client_inbounds.client_id").
		Where("clients.email = ?", clientEmail).
		First(inbound).Error
	if err == nil {
		return inbound, nil
	}

	var candidates []model.Inbound
	if listErr := db.Model(&model.Inbound{}).Where("settings LIKE ?", "%"+clientEmail+"%").Find(&candidates).Error; listErr != nil {
		return nil, listErr
	}
	for i := range candidates {
		settings := map[string][]model.Client{}
		if jsonErr := json.Unmarshal([]byte(candidates[i].Settings), &settings); jsonErr != nil {
			continue
		}
		for _, client := range settings["clients"] {
			if client.Email == clientEmail {
				return &candidates[i], nil
			}
		}
	}

	return nil, err
}
