package controller

import (
	"net/http"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/web/middleware"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/panel"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/tgbot"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"

	"github.com/gin-gonic/gin"
)

// APIController handles the main API routes for the 3x-ui panel, including inbounds and server management.
type APIController struct {
	BaseController
	inboundController     *InboundController
	serverController      *ServerController
	nodeController        *NodeController
	hostController        *HostController
	settingController     *SettingController
	xraySettingController *XraySettingController
	userService           panel.UserService
	apiTokenService       panel.ApiTokenService
	Tgbot                 tgbot.Tgbot
}

// NewAPIController creates a new APIController instance and initializes its routes.
func NewAPIController(g *gin.RouterGroup) *APIController {
	a := &APIController{}
	a.initRouter(g)
	return a
}

func parseBearerCredential(header string) (string, bool) {
	parts := strings.Fields(header)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func (a *APIController) setAuthenticatedAPIPrincipal(c *gin.Context, auth *panel.ApiTokenAuthentication) bool {
	if auth == nil {
		return false
	}
	user := auth.Subject
	if user == nil {
		var err error
		user, err = a.userService.GetFirstUser()
		if err != nil || user == nil {
			return false
		}
	}
	session.SetAPIAuthPrincipal(c, user, &session.APIAuthPrincipal{
		TokenId:   auth.TokenId,
		TokenName: auth.TokenName,
		Kind:      auth.Kind,
		Scopes:    auth.Scopes,
	})
	c.Set("api_authed", true)
	return true
}

func (a *APIController) checkAPIAuth(c *gin.Context) {
	// A verified client certificate (a completed mTLS handshake) authenticates
	// the caller as a trusted service principal. Fail closed if the panel has no
	// owner user instead of setting api_authed without a usable identity.
	if c.Request.TLS != nil && len(c.Request.TLS.VerifiedChains) > 0 {
		u, err := a.userService.GetFirstUser()
		if err != nil || u == nil {
			c.AbortWithStatus(http.StatusUnauthorized)
			return
		}
		session.SetAPIAuthPrincipal(c, u, &session.APIAuthPrincipal{
			Kind:   session.APIAuthPrincipalKindMTLS,
			Scopes: []string{"*"},
		})
		c.Set("api_authed", true)
		c.Next()
		return
	}

	// An explicit Authorization header always wins over a browser cookie. A
	// malformed, expired, revoked, or otherwise invalid Bearer credential must
	// not silently fall back to an authenticated browser session.
	authorization := c.GetHeader("Authorization")
	if authorization != "" {
		token, ok := parseBearerCredential(authorization)
		if ok {
			auth, err := a.apiTokenService.Authenticate(token)
			if err == nil && a.setAuthenticatedAPIPrincipal(c, auth) {
				c.Next()
				return
			}
		}
		c.AbortWithStatus(http.StatusUnauthorized)
		return
	}

	if !session.IsLogin(c) {
		if c.GetHeader("X-Requested-With") == "XMLHttpRequest" {
			c.AbortWithStatus(http.StatusUnauthorized)
		} else {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	c.Next()
}

// initRouter sets up the API routes for inbounds, server, and other endpoints.
func (a *APIController) initRouter(g *gin.RouterGroup) {
	a.initCustomPanelRouter(g)
	// Strict-B authority has its own node-bound HMAC authentication and must
	// remain reachable before the regular browser/API-token middleware.
	NewStrictIPLimitAuthorityController(g.Group("/panel/ip-limit/v1"))

	// Main API group
	api := g.Group("/panel/api")
	api.Use(a.checkAPIAuth)
	// Delegated tokens are default-deny and may reach only explicitly scoped
	// routes. Browser sessions, legacy service tokens, and mTLS keep their
	// existing behavior inside the middleware.
	api.Use(enforceDelegatedAPIScope())
	// Decode + verify the node config envelope (zstd + X-Config-Sha256) and
	// advertise support, before CSRF/handlers read the body.
	api.Use(middleware.ConfigEnvelopeMiddleware())
	api.Use(middleware.CSRFMiddleware())

	// Inbounds API
	inbounds := api.Group("/inbounds")
	a.inboundController = NewInboundController(inbounds)

	clients := api.Group("/clients")
	NewClientController(clients)
	NewGroupController(clients)

	admins := api.Group("/admins")
	NewAdminController(admins)

	adminRoles := api.Group("/admin-roles")
	NewAdminRoleController(adminRoles)

	// Server API
	server := api.Group("/server")
	a.serverController = NewServerController(server)

	// Nodes API — multi-panel management
	nodes := api.Group("/nodes")
	a.nodeController = NewNodeController(nodes)

	// Hosts API — per-inbound override endpoints for subscription links
	hosts := api.Group("/hosts")
	a.hostController = NewHostController(hosts)

	// Settings + Xray config management live under the API surface too, so the
	// same API token drives them. Paths are /panel/api/setting/* and
	// /panel/api/xray/*.
	a.settingController = NewSettingController(api)
	a.xraySettingController = NewXraySettingController(api)

	// Extra routes
	api.POST("/backuptotgbot", a.BackuptoTgbot)
}

// BackuptoTgbot sends a backup of the panel data to Telegram bot admins.
func (a *APIController) BackuptoTgbot(c *gin.Context) {
	a.Tgbot.SendBackupToAdmins()
}
