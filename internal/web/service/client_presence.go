package service

import (
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

const (
	presenceLookupChunk     = 400
	presencePositiveTTL     = 5 * time.Minute
	presenceCacheMaxEntries = 100000
)

var presenceRuntimeEmailPattern = regexp.MustCompile(
	"^" + regexp.QuoteMeta(model.ClientInboundStatEmailPrefix) + `_[0-9]+_[a-z2-7]{16}$`,
)

type presenceCacheEntry struct {
	logical string
	expires time.Time
}

type presenceLookup func([]string) (map[string]string, error)

// PresenceEmailResolver maps hmstat runtime identifiers back to canonical client
// emails. Direct logical emails bypass the database. Positive mappings are
// cached, missing mappings are retried on the next presence tick, and entries
// that disappeared from the current core snapshot are removed immediately.
type PresenceEmailResolver struct {
	mu      sync.Mutex
	entries map[string]presenceCacheEntry
	lookup  presenceLookup
	now     func() time.Time
}

// NewPresenceEmailResolver creates a resolver backed by client_inbound_traffics.
func NewPresenceEmailResolver() *PresenceEmailResolver {
	return newPresenceEmailResolver(lookupRuntimePresenceEmails)
}

func newPresenceEmailResolver(lookup presenceLookup) *PresenceEmailResolver {
	return &PresenceEmailResolver{
		entries: make(map[string]presenceCacheEntry),
		lookup:  lookup,
		now:     time.Now,
	}
}

// Resolve returns sorted, deduplicated canonical emails.
func (r *PresenceEmailResolver) Resolve(runtimeEmails []string) ([]string, error) {
	input := normalizePresenceResolverEmails(runtimeEmails)
	if len(input) == 0 {
		r.mu.Lock()
		clear(r.entries)
		r.mu.Unlock()
		return []string{}, nil
	}

	now := r.now()
	resolved := make(map[string]struct{}, len(input))
	activeRuntime := make(map[string]struct{}, len(input))
	misses := make([]string, 0, len(input))

	for _, email := range input {
		if isPresenceRuntimeEmail(email) {
			activeRuntime[email] = struct{}{}
		} else {
			resolved[email] = struct{}{}
		}
	}

	r.mu.Lock()
	for runtimeEmail := range r.entries {
		if _, active := activeRuntime[runtimeEmail]; !active {
			delete(r.entries, runtimeEmail)
		}
	}
	for runtimeEmail := range activeRuntime {
		entry, found := r.entries[runtimeEmail]
		if !found || !now.Before(entry.expires) {
			if found {
				delete(r.entries, runtimeEmail)
			}
			misses = append(misses, runtimeEmail)
			continue
		}
		resolved[entry.logical] = struct{}{}
	}
	r.mu.Unlock()

	sort.Strings(misses)
	if len(misses) > 0 {
		mapped, err := r.lookup(misses)
		if err != nil {
			return nil, err
		}

		r.mu.Lock()
		for _, runtimeEmail := range misses {
			logical := strings.TrimSpace(mapped[runtimeEmail])
			if logical == "" {
				// Preserve visibility, but do not negative-cache the miss. Config
				// generation normally creates the mapping before the runtime user.
				resolved[runtimeEmail] = struct{}{}
				continue
			}
			r.entries[runtimeEmail] = presenceCacheEntry{
				logical: logical,
				expires: now.Add(presencePositiveTTL),
			}
			resolved[logical] = struct{}{}
		}
		r.pruneLocked(now)
		r.mu.Unlock()
	}

	out := make([]string, 0, len(resolved))
	for email := range resolved {
		out = append(out, email)
	}
	sort.Strings(out)
	return out, nil
}

func (r *PresenceEmailResolver) pruneLocked(now time.Time) {
	if len(r.entries) <= presenceCacheMaxEntries {
		return
	}
	for key, entry := range r.entries {
		if !now.Before(entry.expires) {
			delete(r.entries, key)
		}
	}
	for key := range r.entries {
		if len(r.entries) <= presenceCacheMaxEntries {
			break
		}
		delete(r.entries, key)
	}
}

func isPresenceRuntimeEmail(email string) bool {
	return presenceRuntimeEmailPattern.MatchString(email)
}

func normalizePresenceResolverEmails(emails []string) []string {
	seen := make(map[string]struct{}, len(emails))
	out := make([]string, 0, len(emails))
	for _, raw := range emails {
		email := strings.TrimSpace(raw)
		if email == "" {
			continue
		}
		if _, duplicate := seen[email]; duplicate {
			continue
		}
		seen[email] = struct{}{}
		out = append(out, email)
	}
	sort.Strings(out)
	return out
}

func lookupRuntimePresenceEmails(runtimeEmails []string) (map[string]string, error) {
	out := make(map[string]string)
	db := database.GetDB()

	for start := 0; start < len(runtimeEmails); start += presenceLookupChunk {
		end := start + presenceLookupChunk
		if end > len(runtimeEmails) {
			end = len(runtimeEmails)
		}

		var rows []struct {
			StatEmail string `gorm:"column:stat_email"`
			Email     string `gorm:"column:email"`
		}
		if err := db.
			Model(&model.ClientInboundTraffic{}).
			Select("stat_email, email").
			Where("stat_email IN ?", runtimeEmails[start:end]).
			Find(&rows).
			Error; err != nil {
			return nil, err
		}

		for _, row := range rows {
			statEmail := strings.TrimSpace(row.StatEmail)
			email := strings.TrimSpace(row.Email)
			if statEmail != "" && email != "" {
				out[statEmail] = email
			}
		}
	}
	return out, nil
}

// GetLocalOnlineClients returns the canonical local exact/grace snapshot only.
// Remote-node contributions are deliberately excluded.
func (s *InboundService) GetLocalOnlineClients() []string {
	lock.Lock()
	defer lock.Unlock()
	if p == nil {
		return []string{}
	}
	return p.GetLocalOnlineClients()
}
