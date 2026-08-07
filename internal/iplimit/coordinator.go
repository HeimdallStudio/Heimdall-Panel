// Package iplimit contains the root-authoritative Strict IP Limit state engine.
// It deliberately does not depend on Presence or traffic activity: admission is
// based only on stable ClientGuid identities and explicit source-IP lease holders.
package iplimit

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const lockStripeCount = 256

var (
	ErrInvalidClientGuid   = errors.New("invalid client guid")
	ErrInvalidSourceIP     = errors.New("invalid source ip")
	ErrInvalidHolderKey    = errors.New("invalid lease holder key")
	ErrInvalidLeaseTTL     = errors.New("invalid lease ttl")
	ErrClientNotFound      = errors.New("client guid not found")
	ErrAmbiguousClientGuid = errors.New("client guid is not unique")
	ErrInvalidLimit        = errors.New("client has invalid negative ip limit")

	clientLocks [lockStripeCount]sync.Mutex
)

type DecisionReason string

const (
	DecisionUnlimited    DecisionReason = "unlimited"
	DecisionExistingSlot DecisionReason = "existing_slot"
	DecisionNewSlot      DecisionReason = "new_slot"
	DecisionLimitReached DecisionReason = "limit_reached"
)

// Decision is the authoritative result for one admission request. ActiveSlots
// is the count of distinct unexpired source IPs after the decision.
type Decision struct {
	Allowed      bool
	Reason       DecisionReason
	Limit        int
	ActiveSlots  int
	ExistingSlot bool
	ExpiresAt    int64
}

// HolderSnapshot is persisted coordinator state for one source-IP holder.
type HolderSnapshot struct {
	IP        string
	HolderKey string
	ExpiresAt int64
}

// Coordinator owns Strict-B lease decisions for the authoritative panel. Its
// TTL is root policy; callers cannot extend their own leases arbitrarily.
type Coordinator struct {
	db  *gorm.DB
	ttl time.Duration
	now func() time.Time
}

func NewCoordinator(db *gorm.DB, ttl time.Duration) *Coordinator {
	return &Coordinator{db: db, ttl: ttl, now: time.Now}
}

func normalizeClientGuid(v string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(v))
	if err != nil {
		return "", ErrInvalidClientGuid
	}
	return parsed.String(), nil
}

func normalizeHolderKey(v string) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" || len(v) > 128 {
		return "", ErrInvalidHolderKey
	}
	return v, nil
}

func normalizeSourceIP(v string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(v))
	if err != nil || addr.Zone() != "" {
		return "", ErrInvalidSourceIP
	}
	return addr.Unmap().String(), nil
}

func lockForClient(guid string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(guid))
	return &clientLocks[h.Sum32()%lockStripeCount]
}

func (c *Coordinator) dbOrError() (*gorm.DB, error) {
	if c == nil || c.db == nil {
		return nil, errors.New("ip-limit coordinator database is nil")
	}
	return c.db, nil
}

// Acquire grants or renews one holder on sourceIP. A source IP already held by
// any node is always the same logical slot, so adding another holder never
// consumes another LimitIP slot. A genuinely new IP is granted only when the
// root's current ClientRecord limit has room.
func (c *Coordinator) Acquire(
	ctx context.Context,
	clientGuid string,
	sourceIP string,
	holderKey string,
) (Decision, error) {
	var zero Decision
	db, err := c.dbOrError()
	if err != nil {
		return zero, err
	}
	if c.ttl <= 0 {
		return zero, ErrInvalidLeaseTTL
	}
	guid, err := normalizeClientGuid(clientGuid)
	if err != nil {
		return zero, err
	}
	ip, err := normalizeSourceIP(sourceIP)
	if err != nil {
		return zero, err
	}
	holder, err := normalizeHolderKey(holderKey)
	if err != nil {
		return zero, err
	}

	mu := lockForClient(guid)
	mu.Lock()
	defer mu.Unlock()

	now := c.now().UTC()
	nowMS := now.UnixMilli()
	expiresAt := now.Add(c.ttl).UnixMilli()
	decision := zero

	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		client, err := loadAuthoritativeClient(tx, guid)
		if err != nil {
			return err
		}
		decision.Limit = client.LimitIP
		if client.LimitIP < 0 {
			return ErrInvalidLimit
		}

		if client.LimitIP == 0 {
			if err := tx.Where("client_guid = ?", guid).Delete(&model.ClientIPLeaseHolder{}).Error; err != nil {
				return fmt.Errorf("clear unlimited client leases: %w", err)
			}
			decision.Allowed = true
			decision.Reason = DecisionUnlimited
			return nil
		}

		if err := pruneClientExpired(tx, guid, nowMS); err != nil {
			return err
		}

		// A lowered LimitIP must converge instead of letting every previously
		// active slot renew forever. Keep the oldest distinct slots, with IP as
		// a stable tie-breaker, and evict holders for the excess slots before
		// evaluating this request.
		slots, err := convergeClientLimit(tx, guid, client.LimitIP, nowMS)
		if err != nil {
			return err
		}

		exists, err := activeIPExists(tx, guid, ip, nowMS)
		if err != nil {
			return err
		}
		if exists {
			if err := upsertHolder(tx, guid, ip, holder, expiresAt, nowMS); err != nil {
				return err
			}
			decision.Allowed = true
			decision.Reason = DecisionExistingSlot
			decision.ActiveSlots = slots
			decision.ExistingSlot = true
			decision.ExpiresAt = expiresAt
			return nil
		}

		if slots >= client.LimitIP {
			decision.Allowed = false
			decision.Reason = DecisionLimitReached
			decision.ActiveSlots = slots
			return nil
		}

		if err := upsertHolder(tx, guid, ip, holder, expiresAt, nowMS); err != nil {
			return err
		}
		decision.Allowed = true
		decision.Reason = DecisionNewSlot
		decision.ActiveSlots = slots + 1
		decision.ExpiresAt = expiresAt
		return nil
	})
	if err != nil {
		return zero, err
	}
	return decision, nil
}

// Release removes one holder without disturbing another node holding the same
// ClientGuid+IP slot. The slot disappears only after its final holder is gone.
func (c *Coordinator) Release(
	ctx context.Context,
	clientGuid string,
	sourceIP string,
	holderKey string,
) (bool, error) {
	db, err := c.dbOrError()
	if err != nil {
		return false, err
	}
	guid, err := normalizeClientGuid(clientGuid)
	if err != nil {
		return false, err
	}
	ip, err := normalizeSourceIP(sourceIP)
	if err != nil {
		return false, err
	}
	holder, err := normalizeHolderKey(holderKey)
	if err != nil {
		return false, err
	}

	mu := lockForClient(guid)
	mu.Lock()
	defer mu.Unlock()

	result := db.WithContext(ctx).
		Where("client_guid = ? AND ip = ? AND holder_key = ?", guid, ip, holder).
		Delete(&model.ClientIPLeaseHolder{})
	return result.RowsAffected > 0, result.Error
}

// Snapshot returns only unexpired holders for one logical client and prunes its
// stale rows first. It is a repair/debug primitive, not Presence state.
func (c *Coordinator) Snapshot(ctx context.Context, clientGuid string) ([]HolderSnapshot, error) {
	db, err := c.dbOrError()
	if err != nil {
		return nil, err
	}
	guid, err := normalizeClientGuid(clientGuid)
	if err != nil {
		return nil, err
	}

	mu := lockForClient(guid)
	mu.Lock()
	defer mu.Unlock()

	nowMS := c.now().UTC().UnixMilli()
	var out []HolderSnapshot
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := pruneClientExpired(tx, guid, nowMS); err != nil {
			return err
		}
		var rows []model.ClientIPLeaseHolder
		if err := tx.Where("client_guid = ? AND expires_at > ?", guid, nowMS).
			Order("ip ASC, holder_key ASC").Find(&rows).Error; err != nil {
			return fmt.Errorf("load client lease holders: %w", err)
		}
		out = make([]HolderSnapshot, 0, len(rows))
		for _, row := range rows {
			out = append(out, HolderSnapshot{IP: row.IP, HolderKey: row.HolderKey, ExpiresAt: row.ExpiresAt})
		}
		return nil
	})
	return out, err
}

// PruneExpired removes expired holder rows cluster-wide. Acquire and Snapshot
// also prune their own ClientGuid, so correctness does not depend on this sweep.
func (c *Coordinator) PruneExpired(ctx context.Context) (int64, error) {
	db, err := c.dbOrError()
	if err != nil {
		return 0, err
	}
	result := db.WithContext(ctx).
		Where("expires_at <= ?", c.now().UTC().UnixMilli()).
		Delete(&model.ClientIPLeaseHolder{})
	return result.RowsAffected, result.Error
}

func loadAuthoritativeClient(tx *gorm.DB, guid string) (*model.ClientRecord, error) {
	query := tx.Where("client_guid = ?", guid).Limit(2)
	if database.Dialect() == database.DialectPostgres {
		query = query.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	var rows []model.ClientRecord
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("load authoritative client: %w", err)
	}
	if len(rows) == 0 {
		return nil, ErrClientNotFound
	}
	if len(rows) != 1 {
		return nil, ErrAmbiguousClientGuid
	}
	return &rows[0], nil
}

func pruneClientExpired(tx *gorm.DB, guid string, nowMS int64) error {
	if err := tx.Where("client_guid = ? AND expires_at <= ?", guid, nowMS).
		Delete(&model.ClientIPLeaseHolder{}).Error; err != nil {
		return fmt.Errorf("prune expired client leases: %w", err)
	}
	return nil
}

// convergeClientLimit enforces a lowered LimitIP against already-active slots.
// The oldest distinct source-IP slots win; IP provides a deterministic tie-break
// when multiple slots were created in the same millisecond. Excess slot holders
// are deleted atomically in the same transaction as the admission decision.
func convergeClientLimit(tx *gorm.DB, guid string, limit int, nowMS int64) (int, error) {
	type slotAge struct {
		IP        string `gorm:"column:ip"`
		CreatedAt int64  `gorm:"column:created_at"`
	}

	var active []slotAge
	if err := tx.Model(&model.ClientIPLeaseHolder{}).
		Select("ip, MIN(created_at) AS created_at").
		Where("client_guid = ? AND expires_at > ?", guid, nowMS).
		Group("ip").
		Order("created_at ASC").
		Order("ip ASC").
		Scan(&active).Error; err != nil {
		return 0, fmt.Errorf("load active lease slots for convergence: %w", err)
	}
	if len(active) <= limit {
		return len(active), nil
	}

	excess := make([]string, 0, len(active)-limit)
	for _, slot := range active[limit:] {
		excess = append(excess, slot.IP)
	}
	if err := tx.Where("client_guid = ? AND ip IN ?", guid, excess).
		Delete(&model.ClientIPLeaseHolder{}).Error; err != nil {
		return 0, fmt.Errorf("evict excess lease slots after limit reduction: %w", err)
	}
	return limit, nil
}

func activeIPExists(tx *gorm.DB, guid, ip string, nowMS int64) (bool, error) {
	var count int64
	if err := tx.Model(&model.ClientIPLeaseHolder{}).
		Where("client_guid = ? AND ip = ? AND expires_at > ?", guid, ip, nowMS).
		Count(&count).Error; err != nil {
		return false, fmt.Errorf("check active lease slot: %w", err)
	}
	return count > 0, nil
}

func upsertHolder(tx *gorm.DB, guid, ip, holder string, expiresAt, updatedAt int64) error {
	row := model.ClientIPLeaseHolder{
		ClientGuid: guid,
		IP:         ip,
		HolderKey:  holder,
		ExpiresAt:  expiresAt,
		CreatedAt:  updatedAt,
		UpdatedAt:  updatedAt,
	}
	if err := tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "client_guid"},
			{Name: "ip"},
			{Name: "holder_key"},
		},
		DoUpdates: clause.Assignments(map[string]any{
			"expires_at": expiresAt,
			"updated_at": updatedAt,
		}),
	}).Create(&row).Error; err != nil {
		return fmt.Errorf("upsert client lease holder: %w", err)
	}
	return nil
}
