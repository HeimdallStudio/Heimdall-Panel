package service

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	coreiplimit "github.com/mhsanaei/3x-ui/v3/internal/iplimit"
	"github.com/mhsanaei/3x-ui/v3/internal/web/runtime"
)

const (
	strictIPLimitParentSettingKey = "strictIPLimitParent"
	strictIPLimitAuthorityURLEnv  = "XUI_STRICT_IP_AUTHORITY_URL"
	strictIPLimitLeaseTTL         = 90 * time.Second
)

type StrictIPLimitParentConfig struct {
	URL              string `json:"url"`
	Token            string `json:"token"`
	ParentGuid       string `json:"parentGuid"`
	TLSVerifyMode    string `json:"tlsVerifyMode,omitempty"`
	PinnedCertSha256 string `json:"pinnedCertSha256,omitempty"`
}

type StrictIPLimitService struct {
	settingService SettingService
	serverService  ServerService
}

func (s *StrictIPLimitService) parentConfig() (StrictIPLimitParentConfig, error) {
	var cfg StrictIPLimitParentConfig
	raw, err := s.settingService.getString(strictIPLimitParentSettingKey)
	if err != nil {
		return cfg, err
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cfg, nil
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return StrictIPLimitParentConfig{}, err
	}
	cfg.URL = strings.TrimSpace(cfg.URL)
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.ParentGuid = strings.TrimSpace(cfg.ParentGuid)
	cfg.TLSVerifyMode = strings.TrimSpace(cfg.TLSVerifyMode)
	cfg.PinnedCertSha256 = strings.ToLower(strings.TrimSpace(cfg.PinnedCertSha256))
	return cfg, nil
}

func (s *StrictIPLimitService) SetParentConfig(cfg StrictIPLimitParentConfig) error {
	cfg.URL = strings.TrimSpace(cfg.URL)
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.ParentGuid = strings.TrimSpace(cfg.ParentGuid)
	cfg.TLSVerifyMode = strings.TrimSpace(cfg.TLSVerifyMode)
	cfg.PinnedCertSha256 = strings.ToLower(strings.TrimSpace(cfg.PinnedCertSha256))
	if cfg.URL == "" || cfg.Token == "" || cfg.ParentGuid == "" {
		return errors.New("strict ip-limit parent configuration is incomplete")
	}
	parsedURL, err := url.Parse(cfg.URL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return errors.New("strict ip-limit parent URL is invalid")
	}
	if cfg.TLSVerifyMode != "" && cfg.TLSVerifyMode != "system" && cfg.TLSVerifyMode != "pin" {
		return errors.New("strict ip-limit parent TLS verify mode is invalid")
	}
	if cfg.TLSVerifyMode == "pin" {
		decoded, err := hex.DecodeString(cfg.PinnedCertSha256)
		if err != nil || len(decoded) != sha256.Size {
			return errors.New("strict ip-limit parent certificate pin is invalid")
		}
	}
	selfGuid, err := s.settingService.GetPanelGuid()
	if err != nil {
		return err
	}
	tokenGuid, ok := coreiplimit.VerifyAuthorityTokenSyntax(cfg.Token)
	if !ok || tokenGuid != selfGuid {
		return errors.New("strict ip-limit parent token is not addressed to this panel")
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	if current, getErr := s.settingService.getString(strictIPLimitParentSettingKey); getErr == nil && strings.TrimSpace(current) == string(data) {
		return nil
	}
	return s.settingService.setString(strictIPLimitParentSettingKey, string(data))
}

func (s *StrictIPLimitService) ClearParentConfig() error {
	return s.settingService.setString(strictIPLimitParentSettingKey, "")
}

func (s *StrictIPLimitService) SelfAuthorityURL() (string, error) {
	if explicit := strings.TrimSpace(os.Getenv(strictIPLimitAuthorityURLEnv)); explicit != "" {
		u, err := url.Parse(explicit)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", fmt.Errorf("invalid %s", strictIPLimitAuthorityURLEnv)
		}
		return explicit, nil
	}

	host, err := s.publicAuthorityHost()
	if err != nil {
		return "", err
	}
	port, err := s.settingService.GetPort()
	if err != nil {
		return "", err
	}
	basePath, err := s.settingService.GetBasePath()
	if err != nil {
		return "", err
	}

	scheme := "http"
	certFile, _ := s.settingService.GetCertFile()
	keyFile, _ := s.settingService.GetKeyFile()
	if certFile != "" && keyFile != "" {
		if _, err := tls.LoadX509KeyPair(certFile, keyFile); err == nil {
			scheme = "https"
		}
	}

	authorityHost := net.JoinHostPort(host, strconv.Itoa(port))
	if (scheme == "http" && port == 80) || (scheme == "https" && port == 443) {
		authorityHost = host
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			authorityHost = "[" + host + "]"
		}
	}
	u := &url.URL{
		Scheme: scheme,
		Host:   authorityHost,
		Path:   strings.TrimSuffix(basePath, "/") + "/panel/ip-limit/v1/lease",
	}
	return u.String(), nil
}

func (s *StrictIPLimitService) publicAuthorityHost() (string, error) {
	if domain, err := s.settingService.GetWebDomain(); err == nil && strings.TrimSpace(domain) != "" {
		return strings.TrimSpace(domain), nil
	}
	s.serverService.resolvePublicIPs()
	if ip := strings.TrimSpace(s.serverService.cachedIPv4); ip != "" && ip != "N/A" {
		return ip, nil
	}
	if ip := strings.TrimSpace(s.serverService.cachedIPv6); ip != "" && ip != "N/A" {
		return ip, nil
	}
	return "", errors.New("cannot determine a public authority host; set XUI_STRICT_IP_AUTHORITY_URL")
}

func (s *StrictIPLimitService) MintChildToken(childGuid string) (string, error) {
	secret, err := s.settingService.GetSecret()
	if err != nil {
		return "", err
	}
	return coreiplimit.MintAuthorityToken(secret, childGuid)
}

func (s *StrictIPLimitService) AuthenticateDirectChild(token string) (string, error) {
	secret, err := s.settingService.GetSecret()
	if err != nil {
		return "", err
	}
	childGuid, ok := coreiplimit.VerifyAuthorityToken(secret, token)
	if !ok {
		return "", errors.New("invalid strict ip-limit authority token")
	}
	var count int64
	if err := database.GetDB().Model(&model.Node{}).
		Where("guid = ? AND enable = ?", childGuid, true).
		Count(&count).Error; err != nil {
		return "", err
	}
	if count != 1 {
		return "", errors.New("strict ip-limit authority token is not bound to one enabled direct node")
	}
	return childGuid, nil
}

func (s *StrictIPLimitService) ResolveLocal(ctx context.Context, req coreiplimit.LeaseRequest) (coreiplimit.LeaseResponse, error) {
	guid, err := s.settingService.GetPanelGuid()
	if err != nil {
		return coreiplimit.LeaseResponse{}, err
	}
	req.HolderKey = "local:" + guid
	return s.resolve(ctx, req)
}

func (s *StrictIPLimitService) ResolveRelay(ctx context.Context, req coreiplimit.LeaseRequest, directChildGuid string) (coreiplimit.LeaseResponse, error) {
	holder, err := coreiplimit.RelayHolderKey(directChildGuid, req.HolderKey)
	if err != nil {
		return coreiplimit.LeaseResponse{}, err
	}
	req.HolderKey = holder
	return s.resolve(ctx, req)
}

func (s *StrictIPLimitService) resolve(ctx context.Context, req coreiplimit.LeaseRequest) (coreiplimit.LeaseResponse, error) {
	parent, err := s.parentConfig()
	if err != nil {
		return coreiplimit.LeaseResponse{}, err
	}
	if parent.URL != "" || parent.Token != "" || parent.ParentGuid != "" {
		if parent.URL == "" || parent.Token == "" || parent.ParentGuid == "" {
			return coreiplimit.LeaseResponse{}, errors.New("strict ip-limit parent configuration is incomplete")
		}
		client, err := strictIPLimitParentHTTPClient(parent)
		if err != nil {
			return coreiplimit.LeaseResponse{}, err
		}
		return coreiplimit.ForwardLease(ctx, client, parent.URL, parent.Token, req)
	}

	coordinator := coreiplimit.NewCoordinator(database.GetDB(), strictIPLimitLeaseTTL)
	switch req.Operation {
	case coreiplimit.LeaseAcquire:
		decision, err := coordinator.Acquire(ctx, req.ClientGuid, req.IP, req.HolderKey)
		if err != nil {
			return coreiplimit.LeaseResponse{}, err
		}
		resp := coreiplimit.LeaseResponse{
			Allowed:     decision.Allowed,
			Reason:      decision.Reason,
			Limit:       decision.Limit,
			ActiveSlots: decision.ActiveSlots,
			ExpiresAt:   decision.ExpiresAt,
		}
		if decision.Allowed && decision.Reason != coreiplimit.DecisionUnlimited {
			// The Core may be on another machine whose wall clock is skewed from
			// the root. Carry a root-issued relative TTL so cache validity never
			// depends on synchronized clocks across nodes.
			resp.LeaseTTLMillis = int64(strictIPLimitLeaseTTL / time.Millisecond)
		}
		return resp, nil
	case coreiplimit.LeaseRelease:
		released, err := coordinator.Release(ctx, req.ClientGuid, req.IP, req.HolderKey)
		if err != nil {
			return coreiplimit.LeaseResponse{}, err
		}
		return coreiplimit.LeaseResponse{Allowed: true, Released: released}, nil
	default:
		return coreiplimit.LeaseResponse{}, errors.New("invalid strict ip-limit operation")
	}
}

func strictIPLimitParentHTTPClient(cfg StrictIPLimitParentConfig) (*http.Client, error) {
	transport := &http.Transport{}
	if cfg.TLSVerifyMode == "pin" {
		expected, err := hex.DecodeString(cfg.PinnedCertSha256)
		if err != nil || len(expected) != sha256.Size {
			return nil, errors.New("strict ip-limit parent certificate pin is invalid")
		}
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true, // leaf certificate is verified by SHA-256 pin below
			VerifyConnection: func(cs tls.ConnectionState) error {
				if len(cs.PeerCertificates) == 0 {
					return errors.New("strict ip-limit parent presented no certificate")
				}
				sum := sha256.Sum256(cs.PeerCertificates[0].Raw)
				if !strings.EqualFold(hex.EncodeToString(sum[:]), cfg.PinnedCertSha256) {
					return errors.New("strict ip-limit parent certificate pin mismatch")
				}
				return nil
			},
		}
	}
	return &http.Client{Transport: transport}, nil
}

func (s *StrictIPLimitService) ProvisionNodeParent(ctx context.Context, n *model.Node, childGuid string) error {
	if n == nil {
		return errors.New("node is nil")
	}
	childGuid = strings.TrimSpace(childGuid)
	if childGuid == "" {
		return errors.New("node guid is empty")
	}
	authorityURL, err := s.SelfAuthorityURL()
	if err != nil {
		return err
	}
	token, err := s.MintChildToken(childGuid)
	if err != nil {
		return err
	}
	parentGuid, err := s.settingService.GetPanelGuid()
	if err != nil {
		return err
	}
	cfg := StrictIPLimitParentConfig{URL: authorityURL, Token: token, ParentGuid: parentGuid, TLSVerifyMode: "system"}
	if strings.TrimSpace(os.Getenv(strictIPLimitAuthorityURLEnv)) == "" && strings.HasPrefix(authorityURL, "https://") {
		certFile, _ := s.settingService.GetCertFile()
		keyFile, _ := s.settingService.GetKeyFile()
		if certFile != "" && keyFile != "" {
			if pair, loadErr := tls.LoadX509KeyPair(certFile, keyFile); loadErr == nil && len(pair.Certificate) > 0 {
				sum := sha256.Sum256(pair.Certificate[0])
				cfg.TLSVerifyMode = "pin"
				cfg.PinnedCertSha256 = hex.EncodeToString(sum[:])
			}
		}
	}

	mgr := runtime.GetManager()
	var remote *runtime.Remote
	if mgr != nil {
		remote, err = mgr.RemoteFor(n)
	} else {
		remote = runtime.NewRemote(n, nil)
	}
	if err != nil {
		return err
	}
	return remote.ConfigureStrictIPLimitParent(ctx, runtime.StrictIPLimitParentConfig{URL: cfg.URL, Token: cfg.Token, ParentGuid: cfg.ParentGuid, TLSVerifyMode: cfg.TLSVerifyMode, PinnedCertSha256: cfg.PinnedCertSha256})
}
