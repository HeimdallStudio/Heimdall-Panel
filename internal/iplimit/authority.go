package iplimit

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	AuthorityHeader         = "X-Heimdall-IPLimit-Auth"
	authorityTokenVersion   = "v1"
	defaultAuthorityTimeout = 3 * time.Second
)

type LeaseOperation string

const (
	LeaseAcquire LeaseOperation = "acquire"
	LeaseRelease LeaseOperation = "release"
)

type LeaseRequest struct {
	Operation  LeaseOperation `json:"operation"`
	ClientGuid string         `json:"clientGuid"`
	IP         string         `json:"ip"`
	HolderKey  string         `json:"holderKey,omitempty"`
}

type LeaseResponse struct {
	Allowed        bool           `json:"allowed"`
	Reason         DecisionReason `json:"reason,omitempty"`
	Limit          int            `json:"limit,omitempty"`
	ActiveSlots    int            `json:"activeSlots,omitempty"`
	ExpiresAt      int64          `json:"expiresAt,omitempty"`
	LeaseTTLMillis int64          `json:"leaseTtlMillis,omitempty"`
	Released       bool           `json:"released,omitempty"`
	Error          string         `json:"error,omitempty"`
}

func MintAuthorityToken(secret []byte, childGuid string) (string, error) {
	guid, err := normalizeClientGuid(childGuid)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("heimdall-strict-ip-limit:" + guid))
	return authorityTokenVersion + "." + guid + "." + hex.EncodeToString(mac.Sum(nil)), nil
}

func VerifyAuthorityTokenSyntax(token string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != authorityTokenVersion {
		return "", false
	}
	parsed, err := uuid.Parse(parts[1])
	if err != nil {
		return "", false
	}
	if _, err := hex.DecodeString(parts[2]); err != nil || len(parts[2]) != sha256.Size*2 {
		return "", false
	}
	return parsed.String(), true
}

func VerifyAuthorityToken(secret []byte, token string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 || parts[0] != authorityTokenVersion {
		return "", false
	}
	parsed, err := uuid.Parse(parts[1])
	if err != nil {
		return "", false
	}
	guid := parsed.String()
	got, err := hex.DecodeString(parts[2])
	if err != nil || len(got) != sha256.Size {
		return "", false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte("heimdall-strict-ip-limit:" + guid))
	if !hmac.Equal(got, mac.Sum(nil)) {
		return "", false
	}
	return guid, true
}

func RelayHolderKey(directChildGuid, downstream string) (string, error) {
	parsed, err := uuid.Parse(strings.TrimSpace(directChildGuid))
	if err != nil {
		return "", ErrInvalidClientGuid
	}
	downstream = strings.TrimSpace(downstream)
	if downstream == "" || len(downstream) > 128 {
		return "", ErrInvalidHolderKey
	}
	sum := sha256.Sum256([]byte(parsed.String() + "\x00" + downstream))
	return "path:" + hex.EncodeToString(sum[:]), nil
}

func ForwardLease(ctx context.Context, client *http.Client, targetURL, token string, req LeaseRequest) (LeaseResponse, error) {
	var out LeaseResponse
	targetURL = strings.TrimSpace(targetURL)
	token = strings.TrimSpace(token)
	if targetURL == "" || token == "" {
		return out, errors.New("strict ip-limit parent authority is not configured")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return out, err
	}
	cctx, cancel := context.WithTimeout(ctx, defaultAuthorityTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(cctx, http.MethodPost, targetURL, bytes.NewReader(body))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	httpReq.Header.Set(AuthorityHeader, token)
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return out, err
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("strict ip-limit authority HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, err
	}
	if out.Error != "" {
		return out, errors.New(out.Error)
	}
	return out, nil
}
