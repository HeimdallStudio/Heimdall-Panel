package job

import (
	"encoding/json"

	"github.com/mhsanaei/3x-ui/v3/internal/logger"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service/outbound"
	"github.com/mhsanaei/3x-ui/v3/internal/web/websocket"
	"github.com/mhsanaei/3x-ui/v3/internal/xray"

	"github.com/valyala/fasthttp"
)

// XrayTrafficJob collects and processes traffic statistics from Xray, updating the database and optionally informing external APIs.
type XrayTrafficJob struct {
	settingService  service.SettingService
	xrayService     service.XrayService
	inboundService  service.InboundService
	outboundService outbound.OutboundService
}

// clientStatsSnapshotMaxClients caps how many client_traffics rows the job
// ships as a full websocket snapshot per poll (same spirit as the
// controller's broadcastInboundsUpdateClientLimit). Above it, a snapshot
// would blow past the hub's payload cap and be dropped wholesale, so the job
// broadcasts only this poll's active rows and the UI leans on its 5s REST
// refetch for the rest.
const clientStatsSnapshotMaxClients = 5000

// NewXrayTrafficJob creates a new traffic collection job instance.
func NewXrayTrafficJob() *XrayTrafficJob {
	return new(XrayTrafficJob)
}

// Run collects traffic statistics from Xray, updates the database, and pushes
// real-time updates over WebSocket using compact delta payloads — no REST
// fallback, scales to 10k–20k+ clients per inbound.
func (j *XrayTrafficJob) Run() {
	if !j.xrayService.IsXrayRunning() {
		return
	}
	traffics, clientTraffics, err := j.xrayService.GetXrayTraffic()
	if err != nil {
		return
	}
	needRestart0, clientsDisabled, err := j.inboundService.AddTraffic(traffics, clientTraffics)
	if err != nil {
		logger.Warning("add inbound traffic failed:", err)

		if needRestart0 {
			if repairErr := j.xrayService.RestartXray(true); repairErr != nil {
				logger.Warning(
					"repair xray after rolled-back disable transaction failed:",
					repairErr,
				)
				j.xrayService.SetToNeedRestart()
			} else {
				needRestart0 = false
			}
		}
	}
	err, needRestart1 := j.outboundService.AddTraffic(traffics, clientTraffics)
	if err != nil {
		logger.Warning("add outbound traffic failed:", err)
	}
	if clientsDisabled {
		restartOnDisable, settingErr := j.settingService.GetRestartXrayOnClientDisable()
		if settingErr != nil {
			logger.Warning("get RestartXrayOnClientDisable failed:", settingErr)
		}
		if restartOnDisable {
			// RemoveUser has already updated the running HandlerService. Reconcile
			// immediately so the Process config snapshot matches the real runtime;
			// this normally hot-applies only the affected inbound and leaves the
			// node-egress SOCKS listeners running.
			if err := j.xrayService.ReconcileXray(); err != nil {
				logger.Warning(
					"reconcile xray after disabling clients failed:",
					err,
				)
			}
		}
		websocket.BroadcastInvalidate(websocket.MessageTypeInbounds)
	}
	if ExternalTrafficInformEnable, err := j.settingService.GetExternalTrafficInformEnable(); ExternalTrafficInformEnable {
		j.informTrafficToExternalAPI(traffics, clientTraffics)
	} else if err != nil {
		logger.Warning("get ExternalTrafficInformEnable failed:", err)
	}
	if needRestart0 || needRestart1 {
		j.xrayService.SetToNeedRestart()
	}

	// Canonicalize runtime hmstat identities once per poll and reuse the result
	// for presence, idle last-online maintenance, large-install row selection,
	// and the websocket speed payload.
	canonicalClientTraffics, canonicalErr := j.inboundService.CanonicalClientTrafficDeltas(clientTraffics)
	if canonicalErr != nil {
		logger.Warning("canonicalize client traffic deltas failed:", canonicalErr)
		canonicalClientTraffics = clientTraffics
	}
	activeEmails, deltaActive := activeClientTrafficEmails(canonicalClientTraffics)

	// The traffic path only bumps last_online on a non-zero delta. In exact mode
	// compare canonical deltas only with the exact Xray set, excluding MTProto
	// and other auxiliary clients. On a mapping error, skip the idle write rather
	// than treating every connected client as idle.
	if canonicalErr == nil {
		if exactOnline, fresh := j.xrayService.GetFreshExactXrayOnlineClients(); fresh {
			idleOnline := idleExactXrayClients(exactOnline, deltaActive)
			if err := j.inboundService.BumpClientsLastOnline(idleOnline); err != nil {
				logger.Warning("bump last online for connected clients failed:", err)
			}
		}
	}

	// Pair the email signal with the inbound tags that moved bytes this poll.
	// Xray's user>>>email counter aggregates across every inbound a client is
	// attached to, so an online email alone can't say which inbound it used.
	activeInboundTags := make([]string, 0, len(traffics))
	for _, tr := range traffics {
		if tr != nil && tr.IsInbound && tr.Up+tr.Down > 0 {
			activeInboundTags = append(activeInboundTags, tr.Tag)
		}
	}
	if canonicalErr == nil {
		j.inboundService.RefreshCanonicalLegacyXrayOnlineClients(
			activeEmails,
			activeInboundTags,
		)
	} else {
		rawActiveEmails, _ := activeClientTrafficEmails(clientTraffics)
		j.inboundService.RefreshLegacyXrayOnlineClients(
			rawActiveEmails,
			activeInboundTags,
		)
	}

	if !websocket.HasClients() {
		return
	}

	clientSpeedTraffics := canonicalClientTraffics

	// Small installs broadcast the full snapshot (see GetAllClientTraffics for
	// why deltas alone left UI rows stale). Above the threshold the snapshot
	// would be dropped by the hub's payload cap anyway, so ship this poll's
	// active rows instead and scope last-online to them; the initial full map
	// still arrives over REST.
	snapshot := true
	if total, countErr := j.inboundService.CountClientTraffics(); countErr != nil {
		logger.Warning("count client traffics for websocket failed:", countErr)
	} else if total > clientStatsSnapshotMaxClients {
		snapshot = false
	}

	var stats []*xray.ClientTraffic
	var statsErr error
	if snapshot {
		stats, statsErr = j.inboundService.GetAllClientTraffics()
	} else {
		stats, statsErr = j.inboundService.GetActiveClientTraffics(activeEmails)
	}
	if statsErr != nil {
		logger.Warning("get client traffics for websocket failed:", statsErr)
	}

	var lastOnlineMap map[string]int64
	if snapshot {
		if lastOnlineMap, err = j.inboundService.GetClientsLastOnline(); err != nil {
			logger.Warning("get clients last online failed:", err)
		}
	} else {
		lastOnlineMap = make(map[string]int64, len(stats))
		for _, ct := range stats {
			if ct != nil {
				lastOnlineMap[ct.Email] = ct.LastOnline
			}
		}
	}
	if lastOnlineMap == nil {
		lastOnlineMap = make(map[string]int64)
	}
	onlineClients := j.inboundService.GetOnlineClients()
	if onlineClients == nil {
		onlineClients = []string{}
	}
	websocket.BroadcastTraffic(map[string]any{
		"traffics":       traffics,
		"clientTraffics": clientSpeedTraffics,
		"onlineClients":  onlineClients,
		"onlineByGuid":   j.inboundService.GetOnlineClientsByGuid(),
		"activeInbounds": j.inboundService.GetActiveInboundsByGuid(),
		"lastOnlineMap":  lastOnlineMap,
	})

	clientStatsPayload := map[string]any{"snapshot": snapshot}
	if len(stats) > 0 {
		clientStatsPayload["clients"] = stats
	}
	if inboundSummary, err := j.inboundService.GetInboundsTrafficSummary(); err != nil {
		logger.Warning("get inbounds traffic summary for websocket failed:", err)
	} else if len(inboundSummary) > 0 {
		clientStatsPayload["inbounds"] = inboundSummary
	}
	if len(clientStatsPayload) > 1 {
		websocket.BroadcastClientStats(clientStatsPayload)
	}

	if updatedOutbounds, err := j.outboundService.GetOutboundsTraffic(); err == nil && updatedOutbounds != nil {
		websocket.BroadcastOutbounds(updatedOutbounds)
	} else if err != nil {
		logger.Warning("get all outbounds for websocket failed:", err)
	}
}

func activeClientTrafficEmails(
	traffics []*xray.ClientTraffic,
) ([]string, map[string]struct{}) {
	seen := make(map[string]struct{}, len(traffics))
	active := make([]string, 0, len(traffics))
	for _, traffic := range traffics {
		if traffic == nil || traffic.Up+traffic.Down <= 0 || traffic.Email == "" {
			continue
		}
		if _, duplicate := seen[traffic.Email]; duplicate {
			continue
		}
		seen[traffic.Email] = struct{}{}
		active = append(active, traffic.Email)
	}
	return active, seen
}

func idleExactXrayClients(
	exactOnline []string,
	deltaActive map[string]struct{},
) []string {
	idle := make([]string, 0, len(exactOnline))
	for _, email := range exactOnline {
		if email == "" {
			continue
		}
		if _, active := deltaActive[email]; !active {
			idle = append(idle, email)
		}
	}
	return idle
}

func (j *XrayTrafficJob) informTrafficToExternalAPI(inboundTraffics []*xray.Traffic, clientTraffics []*xray.ClientTraffic) {
	informURL, err := j.settingService.GetExternalTrafficInformURI()
	if err != nil {
		logger.Warning("get ExternalTrafficInformURI failed:", err)
		return
	}
	informURL, err = service.SanitizePublicHTTPURL(informURL, false)
	if err != nil {
		logger.Warning("ExternalTrafficInformURI blocked:", err)
		return
	}
	requestBody, err := json.Marshal(map[string]any{"clientTraffics": clientTraffics, "inboundTraffics": inboundTraffics})
	if err != nil {
		logger.Warning("parse client/inbound traffic failed:", err)
		return
	}
	request := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(request)
	request.Header.SetMethod("POST")
	request.Header.SetContentType("application/json; charset=UTF-8")
	request.SetBody(requestBody)
	request.SetRequestURI(informURL)
	response := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(response)
	if err := fasthttp.Do(request, response); err != nil {
		logger.Warning("POST ExternalTrafficInformURI failed:", err)
	}
}
