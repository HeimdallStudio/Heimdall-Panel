package model

import (
	"strings"

	"github.com/google/uuid"
)

// LegacyClientGuidForEmail deterministically assigns a stable logical-client
// identity to rows that predate client_guid. It is intentionally based on the
// normalized logical email only for migration/backfill convergence across
// already-linked panels; once persisted, the GUID must survive email renames.
func LegacyClientGuidForEmail(email string) string {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return ""
	}
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("heimdall-client:"+email)).String()
}
