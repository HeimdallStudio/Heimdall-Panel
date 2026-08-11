package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/session"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func notifyClientsChanged() {
	websocket.BroadcastInvalidate(websocket.MessageTypeClients)
}

func parseInboundIdsQuery(raw string) []int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]int, 0, len(parts))
	for _, p := range parts {
		if id, err := strconv.Atoi(strings.TrimSpace(p)); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

type ClientController struct {
	clientService   service.ClientService
	activityService service.ClientActivityService
	inboundService  service.InboundService
	xrayService     service.XrayService
	settingService  service.SettingService
}

func (a *ClientController) loginUser(c *gin.Context) *model.User {
	var user *model.User
	func() {
		defer func() {
			if recover() != nil {
				user = nil
			}
		}()
		user = session.GetLoginUser(c)
	}()
	return user
}

func (a *ClientController) clientScope(c *gin.Context, permission string) service.ClientAccessScope {
	user := a.loginUser(c)

	// Some controller unit tests mount ClientController directly without the
	// session middleware that exists on the real /panel/api router. Preserve those
	// tests as full-access callers; production requests still go through auth.
	if user == nil && gin.Mode() == gin.TestMode {
		return service.ClientAccessScope{Mode: service.ClientAccessAll}
	}

	return a.clientService.ClientAccessScopeForAdmin(user, permission)
}

func (a *ClientController) requireClientPermission(c *gin.Context, email string, permission string) (*model.ClientRecord, bool) {
	rec, err := a.clientService.RequireClientForScopeByEmail(a.clientScope(c, permission), email)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return nil, false
	}

	return rec, true
}

func (a *ClientController) requireVisibleClient(c *gin.Context, email string) (*model.ClientRecord, bool) {
	return a.requireClientPermission(c, email, "view")
}

func (a *ClientController) requireAllClientScope(c *gin.Context, permission string) bool {
	if a.clientScope(c, permission).Mode != service.ClientAccessAll {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), fmt.Errorf("client %s permission requires all scope", permission))
		return false
	}
	return true
}

func NewClientController(g *gin.RouterGroup) *ClientController {
	a := &ClientController{}
	a.initRouter(g)
	return a
}

func (a *ClientController) initRouter(g *gin.RouterGroup) {
	g.GET("/list", a.list)
	g.GET("/list/paged", a.listPaged)
	g.GET("/get/:email", a.get)
	g.GET("/traffic/:email", a.getTrafficByEmail)
	g.GET("/subLinks/:subId", a.getSubLinks)
	g.GET("/links/:email", a.getClientLinks)
	g.POST("/activity/node-sync", a.syncNodeActivity)
	g.GET("/:email/activity", a.getActivity)
	g.GET("/:email/activity/export", a.exportActivity)
	g.GET("/:email/activity/status", a.getActivityStatus)

	g.POST("/add", a.create)
	g.POST("/update/:email", a.update)
	g.POST("/del/:email", a.delete)
	g.POST("/:email/attach", a.attach)
	g.POST("/:email/detach", a.detach)
	g.POST("/:email/activity/start", a.startActivityMonitoring)
	g.POST("/:email/activity/stop", a.stopActivityMonitoring)
	g.POST("/:email/activity/reset", a.resetActivityData)
	g.POST("/:email/externalLinks", a.setExternalLinks)
	g.GET("/export", a.export)
	g.POST("/import", a.importClients)
	g.POST("/delOrphans", a.delOrphans)
	g.POST("/resetAllTraffics", a.resetAllTraffics)
	g.POST("/delDepleted", a.delDepleted)
	g.POST("/bulkAdjust", a.bulkAdjust)
	g.POST("/bulkEnable", a.bulkEnable)
	g.POST("/bulkDisable", a.bulkDisable)
	g.POST("/bulkDel", a.bulkDelete)
	g.POST("/bulkCreate", a.bulkCreate)
	g.POST("/bulkAttach", a.bulkAttach)
	g.POST("/bulkDetach", a.bulkDetach)
	g.POST("/bulkResetTraffic", a.bulkResetTraffic)
	g.POST("/resetTraffic/:email", a.resetTrafficByEmail)
	g.POST("/updateTraffic/:email", a.updateTrafficByEmail)
	g.POST("/ips/:email", a.getIps)
	g.POST("/clearIps/:email", a.clearIps)
	g.POST("/onlines", a.onlines)
	g.POST("/onlinesByGuid", a.onlinesByGuid)
	g.POST("/clientIpsByGuid", a.clientIpsByGuid)
	g.POST("/activeInbounds", a.activeInbounds)
	g.POST("/lastOnline", a.lastOnline)
}

func (a *ClientController) list(c *gin.Context) {
	rows, err := a.clientService.ListForScope(a.clientScope(c, "view"))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.obtain"), err)
		return
	}
	jsonObj(c, rows, nil)
}

func (a *ClientController) listPaged(c *gin.Context) {
	var params service.ClientPageParams
	if err := c.ShouldBindQuery(&params); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.obtain"), err)
		return
	}
	params.Scope = a.clientScope(c, "view")
	params.AllowOwnerFilter = a.clientService.CanFilterClientOwnersForAdmin(a.loginUser(c))
	resp, err := a.clientService.ListPaged(&a.inboundService, &a.settingService, params)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.obtain"), err)
		return
	}
	jsonObj(c, resp, nil)
}

func (a *ClientController) get(c *gin.Context) {
	email := c.Param("email")

	rec, ok := a.requireVisibleClient(c, email)
	if !ok {
		return
	}

	inboundIds, err := a.clientService.GetInboundIdsForRecord(rec.Id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	externalLinks, err := a.clientService.GetExternalLinksForRecord(rec.Id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}
	flow, err := a.clientService.EffectiveFlow(nil, rec.Id)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "get"), err)
		return
	}

	rec.Flow = flow
	// Consumed bytes (up+down, including cross-node global overlay) so API
	// consumers can pair usage with the client's totalGB quota (#4973).
	// Best-effort: a traffic lookup failure must not break the client fetch.
	var usedTraffic int64
	if t, tErr := a.inboundService.GetClientTrafficByEmail(email); tErr == nil && t != nil {
		usedTraffic = t.Up + t.Down
	}
	jsonObj(c, gin.H{"client": rec, "inboundIds": inboundIds, "externalLinks": externalLinks, "usedTraffic": usedTraffic}, nil)
}

func (a *ClientController) create(c *gin.Context) {
	var payload service.ClientCreatePayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if service.IsHiddenClientEmail(payload.Client.Email) {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), fmt.Errorf("client not found"))
		return
	}
	needRestart, err := a.clientService.CreateForAdmin(&a.inboundService, &payload, a.loginUser(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsgObj(c, I18nWeb(c, "pages.inbounds.toasts.inboundClientAddSuccess"), pendingNodeObj(a.inboundService.AnyNodePending(payload.InboundIds)), nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

func (a *ClientController) update(c *gin.Context) {
	email := c.Param("email")
	if _, ok := a.requireClientPermission(c, email, "update"); !ok {
		return
	}
	var updated model.Client
	if err := c.ShouldBindJSON(&updated); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if service.IsHiddenClientEmail(updated.Email) {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), fmt.Errorf("client not found"))
		return
	}
	if !service.ClientGroupAllowedForScope(a.clientScope(c, "update"), updated.Group) {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), fmt.Errorf("client group access denied"))
		return
	}
	if err := a.clientService.ValidateClientLimitsForAdmin(a.loginUser(c), updated, false); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	inboundFilter := parseInboundIdsQuery(c.Query("inboundIds"))
	needRestart, err := a.clientService.UpdateByEmail(&a.inboundService, email, updated, inboundFilter...)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsgObj(c, I18nWeb(c, "pages.inbounds.toasts.inboundClientUpdateSuccess"), pendingNodeObj(a.clientService.HasPendingNode(&a.inboundService, email)), nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

func clientDeleteScopeAllowsOrphanCleanup(scope service.ClientAccessScope) bool {
	return scope.Mode == service.ClientAccessAll &&
		(!scope.RestrictGroups || scope.AllowAllGroups) &&
		(!scope.RestrictInbounds || scope.AllowAllInbounds)
}

func (a *ClientController) delete(c *gin.Context) {
	email := c.Param("email")
	scope := a.clientScope(c, "delete")
	if _, err := a.clientService.RequireClientForScopeByEmail(scope, email); err != nil {
		// A previous buggy delete may have already removed the central ClientRecord
		// while leaving the canonical record on one or more nodes. In that state
		// normal record-based RBAC cannot resolve an owner. Permit the idempotent
		// cleanup path only to an unrestricted all-client administrator; delegated
		// or inbound/group-restricted roles must not be able to target arbitrary
		// historical emails. DeleteByEmail performs the node-history lookup and
		// still fails closed if no orphan evidence exists.
		if !errors.Is(err, gorm.ErrRecordNotFound) ||
			!clientDeleteScopeAllowsOrphanCleanup(scope) {
			jsonMsg(c, I18nWeb(c, "get"), err)
			return
		}
	}
	keepTraffic := c.Query("keepTraffic") == "1"
	needRestart, err := a.clientService.DeleteByEmail(&a.inboundService, email, keepTraffic)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.inboundClientDeleteSuccess"), nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

type attachDetachBody struct {
	InboundIds []int `json:"inboundIds"`
}

type externalLinksBody struct {
	ExternalLinks []service.ExternalLinkInput `json:"externalLinks"`
}

func (a *ClientController) attach(c *gin.Context) {
	email := c.Param("email")
	if _, ok := a.requireClientPermission(c, email, "update"); !ok {
		return
	}
	var body attachDetachBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.clientService.ValidateInboundAccessForAdmin(a.loginUser(c), "update", body.InboundIds); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	needRestart, err := a.clientService.AttachByEmail(&a.inboundService, email, body.InboundIds)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsgObj(c, I18nWeb(c, "pages.inbounds.toasts.inboundClientAddSuccess"), pendingNodeObj(a.inboundService.AnyNodePending(body.InboundIds)), nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

func (a *ClientController) setExternalLinks(c *gin.Context) {
	email := c.Param("email")
	if _, ok := a.requireClientPermission(c, email, "update"); !ok {
		return
	}
	var body externalLinksBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.clientService.SetExternalLinksByEmail(email, body.ExternalLinks); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.inboundClientUpdateSuccess"), nil)
	notifyClientsChanged()
}

func (a *ClientController) resetAllTraffics(c *gin.Context) {
	if !a.requireAllClientScope(c, "resetUsage") {
		return
	}
	if err := a.clientService.ValidateAdminRoleFeatureForAdmin(session.GetLoginUser(c), "can_use_reset_strategy"); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	needRestart, err := a.clientService.ResetAllTraffics()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.resetAllClientTrafficSuccess"), nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

type bulkAdjustRequest struct {
	Emails   []string `json:"emails"`
	AddDays  int      `json:"addDays"`
	AddBytes int64    `json:"addBytes"`
	Flow     string   `json:"flow"`
}

func (a *ClientController) bulkAdjust(c *gin.Context) {
	var req bulkAdjustRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	req.Emails = a.clientService.FilterClientEmailsForScope(a.clientScope(c, "update"), req.Emails)
	result, needRestart, err := a.clientService.BulkAdjustForAdmin(&a.inboundService, req.Emails, req.AddDays, req.AddBytes, req.Flow, a.loginUser(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

type bulkDeleteRequest struct {
	Emails      []string `json:"emails"`
	KeepTraffic bool     `json:"keepTraffic"`
}

type bulkAttachRequest struct {
	Emails     []string `json:"emails"`
	InboundIds []int    `json:"inboundIds"`
}

func (a *ClientController) bulkAttach(c *gin.Context) {
	var req bulkAttachRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	req.Emails = a.clientService.FilterClientEmailsForScope(a.clientScope(c, "update"), req.Emails)
	if err := a.clientService.ValidateInboundAccessForAdmin(a.loginUser(c), "update", req.InboundIds); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	result, needRestart, err := a.clientService.BulkAttach(&a.inboundService, req.Emails, req.InboundIds)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

type bulkDetachRequest struct {
	Emails     []string `json:"emails"`
	InboundIds []int    `json:"inboundIds"`
}

func (a *ClientController) bulkDetach(c *gin.Context) {
	var req bulkDetachRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	req.Emails = a.clientService.FilterClientEmailsForScope(a.clientScope(c, "update"), req.Emails)
	if err := a.clientService.ValidateInboundAccessForAdmin(a.loginUser(c), "update", req.InboundIds); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	result, needRestart, err := a.clientService.BulkDetach(&a.inboundService, req.Emails, req.InboundIds)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

func (a *ClientController) bulkDelete(c *gin.Context) {
	var req bulkDeleteRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	req.Emails = a.clientService.FilterClientEmailsForScope(a.clientScope(c, "delete"), req.Emails)
	result, needRestart, err := a.clientService.BulkDelete(&a.inboundService, req.Emails, req.KeepTraffic)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

type bulkEnableRequest struct {
	Emails []string `json:"emails"`
}

func (a *ClientController) bulkEnable(c *gin.Context) {
	a.bulkSetEnable(c, true)
}

func (a *ClientController) bulkDisable(c *gin.Context) {
	a.bulkSetEnable(c, false)
}

func (a *ClientController) bulkSetEnable(c *gin.Context, enable bool) {
	var req bulkEnableRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	result, needRestart, err := a.clientService.BulkSetEnable(&a.inboundService, req.Emails, enable)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

func (a *ClientController) bulkCreate(c *gin.Context) {
	var payloads []service.ClientCreatePayload
	if err := c.ShouldBindJSON(&payloads); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}

	result, needRestart, err := a.clientService.BulkCreateForAdmin(&a.inboundService, payloads, a.loginUser(c))
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}

	jsonObj(c, result, nil)

	if needRestart {
		a.xrayService.SetToNeedRestart()
	}

	notifyClientsChanged()
}

func (a *ClientController) delDepleted(c *gin.Context) {
	if !a.requireAllClientScope(c, "delete") {
		return
	}
	deleted, needRestart, err := a.clientService.DelDepleted(&a.inboundService)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"deleted": deleted}, nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

// export returns every client as a {client, inboundIds} list in the standard
// envelope. The frontend renders it in a read-only CodeMirror viewer (Copy /
// Download), so this hands back data rather than streaming a file attachment.
func (a *ClientController) export(c *gin.Context) {
	if !a.requireAllClientScope(c, "view") {
		return
	}
	items, err := a.clientService.ExportAll()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, items, nil)
}

type importClientsRequest struct {
	Data string `json:"data"`
}

// importClients accepts the pasted export text as a JSON body { "data": "..." },
// mirroring the inbound import flow. The data string is itself a JSON-encoded
// []ClientCreatePayload, so it is unmarshalled in a second step.
func (a *ClientController) importClients(c *gin.Context) {
	if !a.requireAllClientScope(c, "create") {
		return
	}
	var req importClientsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	var items []service.ClientCreatePayload
	if err := json.Unmarshal([]byte(req.Data), &items); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	result, needRestart, err := a.clientService.ImportClients(&a.inboundService, items)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, result, nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

func (a *ClientController) delOrphans(c *gin.Context) {
	if !a.requireAllClientScope(c, "delete") {
		return
	}
	deleted, err := a.clientService.DeleteOrphans()
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"deleted": deleted}, nil)
	notifyClientsChanged()
}

func (a *ClientController) resetTrafficByEmail(c *gin.Context) {
	email := c.Param("email")
	if _, ok := a.requireClientPermission(c, email, "resetUsage"); !ok {
		return
	}
	if err := a.clientService.ValidateAdminRoleFeatureForAdmin(session.GetLoginUser(c), "can_use_reset_strategy"); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	needRestart, err := a.clientService.ResetTrafficByEmail(&a.inboundService, email)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.resetInboundClientTrafficSuccess"), nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

type trafficUpdateRequest struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
}

func (a *ClientController) updateTrafficByEmail(c *gin.Context) {
	email := c.Param("email")

	if _, ok := a.requireClientPermission(c, email, "resetUsage"); !ok {
		return
	}
	if err := a.clientService.ValidateAdminRoleFeatureForAdmin(session.GetLoginUser(c), "can_use_reset_strategy"); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}

	var req trafficUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}

	if err := a.inboundService.UpdateClientTrafficByEmail(email, req.Upload, req.Download); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}

	jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.inboundClientUpdateSuccess"), nil)
	notifyClientsChanged()
}

func (a *ClientController) getIps(c *gin.Context) {
	email := c.Param("email")
	if _, ok := a.requireVisibleClient(c, email); !ok {
		return
	}
	infos, err := a.inboundService.GetClientIpsWithNodes(email)
	jsonObj(c, infos, err)
}

func (a *ClientController) clientIpsByGuid(c *gin.Context) {
	data, err := a.inboundService.GetClientIpsByGuid()
	if err != nil {
		jsonObj(c, data, err)
		return
	}

	emails := make([]string, 0)
	for _, byEmail := range data {
		for email := range byEmail {
			emails = append(emails, email)
		}
	}

	visible := a.clientService.FilterClientEmailsForScope(a.clientScope(c, "view"), emails)
	allowed := make(map[string]struct{}, len(visible))
	for _, email := range visible {
		allowed[email] = struct{}{}
	}

	for guid, byEmail := range data {
		for email := range byEmail {
			if _, ok := allowed[email]; !ok {
				delete(byEmail, email)
			}
		}
		if len(byEmail) == 0 {
			delete(data, guid)
		}
	}

	jsonObj(c, data, nil)
}

func (a *ClientController) clearIps(c *gin.Context) {
	email := c.Param("email")
	if _, ok := a.requireClientPermission(c, email, "update"); !ok {
		return
	}
	if err := a.inboundService.ClearClientIps(email); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.updateSuccess"), err)
		return
	}
	jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.logCleanSuccess"), nil)
}

func (a *ClientController) onlines(c *gin.Context) {
	emails := a.inboundService.GetOnlineClients()
	jsonObj(c, a.clientService.FilterClientEmailsForScope(a.clientScope(c, "view"), emails), nil)
}

func (a *ClientController) onlinesByGuid(c *gin.Context) {
	data := a.inboundService.GetOnlineClientsByGuid()

	for guid, emails := range data {
		visible := a.clientService.FilterClientEmailsForScope(a.clientScope(c, "view"), emails)

		if len(visible) == 0 {
			delete(data, guid)
			continue
		}

		data[guid] = visible
	}

	jsonObj(c, data, nil)
}

func (a *ClientController) activeInbounds(c *gin.Context) {
	jsonObj(c, a.inboundService.GetActiveInboundsByGuid(), nil)
}

func (a *ClientController) lastOnline(c *gin.Context) {
	data, err := a.inboundService.GetClientsLastOnline()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}

	emails := make([]string, 0, len(data))
	for email := range data {
		emails = append(emails, email)
	}
	visible := a.clientService.FilterClientEmailsForScope(a.clientScope(c, "view"), emails)
	allowed := make(map[string]struct{}, len(visible))
	for _, email := range visible {
		allowed[email] = struct{}{}
	}
	for email := range data {
		if _, ok := allowed[email]; !ok {
			delete(data, email)
		}
	}

	jsonObj(c, data, nil)
}

func (a *ClientController) getTrafficByEmail(c *gin.Context) {
	email := c.Param("email")
	if _, ok := a.requireVisibleClient(c, email); !ok {
		return
	}
	traffic, err := a.inboundService.GetClientTrafficByEmail(email)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.trafficGetError"), err)
		return
	}
	jsonObj(c, traffic, nil)
}

func (a *ClientController) getSubLinks(c *gin.Context) {
	subID := c.Param("subId")

	if _, err := a.clientService.RequireClientForScopeBySubID(a.clientScope(c, "view"), subID); err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.obtain"), err)
		return
	}

	links, err := a.inboundService.GetSubLinks(resolveHost(c), subID)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.obtain"), err)
		return
	}

	jsonObj(c, links, nil)
}

func (a *ClientController) getClientLinks(c *gin.Context) {
	email := c.Param("email")

	if _, ok := a.requireVisibleClient(c, email); !ok {
		return
	}

	links, err := a.inboundService.GetAllClientLinks(resolveHost(c), email)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "pages.inbounds.toasts.obtain"), err)
		return
	}

	jsonObj(c, links, nil)
}

func (a *ClientController) detach(c *gin.Context) {
	email := c.Param("email")
	if _, ok := a.requireClientPermission(c, email, "update"); !ok {
		return
	}
	var body attachDetachBody
	if err := c.ShouldBindJSON(&body); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.clientService.ValidateInboundAccessForAdmin(a.loginUser(c), "update", body.InboundIds); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	needRestart, err := a.clientService.DetachByEmailMany(&a.inboundService, email, body.InboundIds)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonMsgObj(c, I18nWeb(c, "pages.inbounds.toasts.inboundClientDeleteSuccess"), pendingNodeObj(a.inboundService.AnyNodePending(body.InboundIds)), nil)
	if needRestart {
		a.xrayService.SetToNeedRestart()
	}
	notifyClientsChanged()
}

type bulkResetRequest struct {
	Emails []string `json:"emails"`
}

func (a *ClientController) bulkResetTraffic(c *gin.Context) {
	var req bulkResetRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	if err := a.clientService.ValidateAdminRoleFeatureForAdmin(session.GetLoginUser(c), "can_use_reset_strategy"); err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	req.Emails = a.clientService.FilterClientEmailsForScope(a.clientScope(c, "resetUsage"), req.Emails)
	affected, err := a.clientService.BulkResetTraffic(&a.inboundService, req.Emails)
	if err != nil {
		jsonMsg(c, I18nWeb(c, "somethingWentWrong"), err)
		return
	}
	jsonObj(c, gin.H{"affected": affected}, nil)
	a.xrayService.SetToNeedRestart()
	notifyClientsChanged()
}

// syncNodeActivity is the parent-to-child Activity replication endpoint. It
// applies canonical monitoring state locally and returns an incremental,
// absolute snapshot containing this panel and any descendants already merged
// into it. Repeated requests are idempotent.
func (a *ClientController) syncNodeActivity(c *gin.Context) {
	var req model.ClientActivitySyncRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		jsonObj(c, nil, err)
		return
	}

	panelGUID, err := a.settingService.GetPanelGuid()
	if err != nil {
		jsonObj(c, nil, err)
		return
	}

	response, err := a.activityService.ApplyNodeSyncAndExport(
		panelGUID,
		&req,
	)
	jsonObj(c, response, err)
}

// getActivityStatus returns the current opt-in monitoring state for one visible
// client. It does not create a settings row when monitoring has never been
// enabled; the service returns the canonical disabled default instead.
func (a *ClientController) getActivityStatus(c *gin.Context) {
	email := c.Param("email")

	client, ok := a.requireVisibleClient(c, email)
	if !ok {
		return
	}

	status, err := a.activityService.StatusByClientID(client.Id)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}

	jsonObj(c, status, nil)
}

// startActivityMonitoring enables destination Activity collection without
// restarting Xray. The Core allowlist synchronization job publishes the new
// generation during its next synchronization cycle.
func (a *ClientController) startActivityMonitoring(c *gin.Context) {
	email := c.Param("email")

	client, ok := a.requireClientPermission(c, email, "update")
	if !ok {
		return
	}

	status, err := a.activityService.SetMonitoringByClientID(
		client.Id,
		true,
	)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}

	jsonObj(c, status, nil)
	notifyClientsChanged()
}

// stopActivityMonitoring stops accepting new Activity events while preserving
// all existing destination history.
func (a *ClientController) stopActivityMonitoring(c *gin.Context) {
	email := c.Param("email")

	client, ok := a.requireClientPermission(c, email, "update")
	if !ok {
		return
	}

	status, err := a.activityService.SetMonitoringByClientID(
		client.Id,
		false,
	)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}

	jsonObj(c, status, nil)
	notifyClientsChanged()
}

// resetActivityData clears destination history atomically while leaving the
// current enabled or disabled monitoring state unchanged.
func (a *ClientController) resetActivityData(c *gin.Context) {
	email := c.Param("email")

	client, ok := a.requireClientPermission(c, email, "update")
	if !ok {
		return
	}

	status, err := a.activityService.ResetByClientID(client.Id)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}

	jsonObj(c, status, nil)
	notifyClientsChanged()
}

// exportActivity returns every logical Activity row stored in the client's
// current data epoch. It deliberately bypasses UI pagination while preserving
// the same local+remote aggregation semantics.
func (a *ClientController) exportActivity(c *gin.Context) {
	email := c.Param("email")

	client, ok := a.requireVisibleClient(c, email)
	if !ok {
		return
	}

	result, err := a.activityService.ExportByClientID(client.Id)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}

	jsonObj(c, result, nil)
}

// getActivity returns the current Activity epoch as a bounded paginated list.
// The UI deliberately renders only destination, source IP, upload, and download.
func (a *ClientController) getActivity(c *gin.Context) {
	email := c.Param("email")

	client, ok := a.requireVisibleClient(c, email)
	if !ok {
		return
	}

	page, _ := strconv.Atoi(
		c.DefaultQuery("page", "1"),
	)
	pageSize, _ := strconv.Atoi(
		c.DefaultQuery("pageSize", "100"),
	)

	result, err := a.activityService.ListByClientID(
		client.Id,
		page,
		pageSize,
	)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}

	jsonObj(c, result, nil)
}
