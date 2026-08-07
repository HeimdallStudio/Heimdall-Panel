package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestClientGuidStableAcrossAttachmentsAndRename(t *testing.T) {
	setupBulkDB(t)
	svc := &ClientService{}
	inboundSvc := &InboundService{}

	ibA := mkInbound(t, 39101, model.VLESS, `{"clients":[]}`)
	ibB := mkInbound(t, 39102, model.VLESS, `{"clients":[]}`)

	const email = "guid-stable@x"
	const protocolUUID = "aaaaaaaa-1111-4222-8333-bbbbbbbbbbbb"
	if _, err := svc.Create(inboundSvc, &ClientCreatePayload{
		Client:     model.Client{Email: email, ID: protocolUUID, SubID: "guid-stable-sub", Enable: true},
		InboundIds: []int{ibA.Id},
	}); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	first := lookupClientRecord(t, email)
	if first.ClientGuid == "" {
		t.Fatal("new client did not receive ClientGuid")
	}
	if _, err := uuid.Parse(first.ClientGuid); err != nil {
		t.Fatalf("ClientGuid is not a UUID: %q: %v", first.ClientGuid, err)
	}
	if first.ClientGuid == first.UUID {
		t.Fatalf("ClientGuid reused protocol UUID %q", first.UUID)
	}

	if _, err := svc.Create(inboundSvc, &ClientCreatePayload{
		Client:     model.Client{Email: email, SubID: "guid-stable-sub", Enable: true},
		InboundIds: []int{ibB.Id},
	}); err != nil {
		t.Fatalf("attach Create: %v", err)
	}
	second := lookupClientRecord(t, email)
	if second.ClientGuid != first.ClientGuid {
		t.Fatalf("attach rotated ClientGuid: %q -> %q", first.ClientGuid, second.ClientGuid)
	}
	for _, id := range []int{ibA.Id, ibB.Id} {
		ib, err := inboundSvc.GetInbound(id)
		if err != nil {
			t.Fatalf("GetInbound %d: %v", id, err)
		}
		clients, err := inboundSvc.GetClients(ib)
		if err != nil || len(clients) != 1 {
			t.Fatalf("inbound %d clients: %+v err=%v", id, clients, err)
		}
		if clients[0].ClientGuid != first.ClientGuid {
			t.Fatalf("inbound %d ClientGuid = %q, want %q", id, clients[0].ClientGuid, first.ClientGuid)
		}
	}

	updated := *second.ToClient()
	updated.Email = "guid-renamed@x"
	if _, err := svc.UpdateByEmail(inboundSvc, email, updated); err != nil {
		t.Fatalf("rename UpdateByEmail: %v", err)
	}
	renamed := lookupClientRecord(t, updated.Email)
	if renamed.ClientGuid != first.ClientGuid {
		t.Fatalf("rename rotated ClientGuid: %q -> %q", first.ClientGuid, renamed.ClientGuid)
	}
}

func TestCopyInboundClientMintsDistinctClientGuid(t *testing.T) {
	setupBulkDB(t)
	inboundSvc := &InboundService{}
	svc := &ClientService{}

	source := mkInbound(t, 39201, model.VLESS, `{"clients":[]}`)
	target := mkInbound(t, 39202, model.VLESS, `{"clients":[]}`)
	if _, err := svc.Create(inboundSvc, &ClientCreatePayload{
		Client:     model.Client{Email: "copy-guid@x", ID: uuid.NewString(), SubID: "copy-guid-sub", Enable: true},
		InboundIds: []int{source.Id},
	}); err != nil {
		t.Fatalf("Create source client: %v", err)
	}
	sourceRec := lookupClientRecord(t, "copy-guid@x")

	result, _, err := inboundSvc.CopyInboundClients(target.Id, source.Id, []string{"copy-guid@x"}, "")
	if err != nil {
		t.Fatalf("CopyInboundClients: %v", err)
	}
	if len(result.Added) != 1 {
		t.Fatalf("copy added = %v", result.Added)
	}
	copyRec := lookupClientRecord(t, result.Added[0])
	if copyRec.ClientGuid == "" || copyRec.ClientGuid == sourceRec.ClientGuid {
		t.Fatalf("copied logical client reused source ClientGuid: src=%q copy=%q", sourceRec.ClientGuid, copyRec.ClientGuid)
	}
}

func TestDirectUpdateInboundClientPreservesClientGuidWhenPayloadOmitsIt(t *testing.T) {
	setupBulkDB(t)
	svc := &ClientService{}
	inboundSvc := &InboundService{}

	ib := mkInbound(t, 39301, model.VLESS, `{"clients":[]}`)
	if _, err := svc.Create(inboundSvc, &ClientCreatePayload{
		Client:     model.Client{Email: "direct-guid@x", ID: uuid.NewString(), SubID: "direct-guid-sub", Enable: true},
		InboundIds: []int{ib.Id},
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	before := lookupClientRecord(t, "direct-guid@x")

	payload := &model.Inbound{
		Id: ib.Id,
		Settings: clientsSettings(t, []model.Client{{
			Email:   "direct-guid@x",
			ID:      before.UUID,
			SubID:   before.SubID,
			Enable:  true,
			Comment: "updated without guid",
		}}),
	}
	if _, err := svc.UpdateInboundClient(inboundSvc, payload, "direct-guid@x"); err != nil {
		t.Fatalf("UpdateInboundClient: %v", err)
	}

	after := lookupClientRecord(t, "direct-guid@x")
	if after.ClientGuid != before.ClientGuid {
		t.Fatalf("direct update rotated ClientGuid: %q -> %q", before.ClientGuid, after.ClientGuid)
	}
	got, err := inboundSvc.GetInbound(ib.Id)
	if err != nil {
		t.Fatalf("GetInbound: %v", err)
	}
	clients, err := inboundSvc.GetClients(got)
	if err != nil || len(clients) != 1 {
		t.Fatalf("GetClients: clients=%+v err=%v", clients, err)
	}
	if clients[0].ClientGuid != before.ClientGuid {
		t.Fatalf("stored settings lost ClientGuid: %q != %q", clients[0].ClientGuid, before.ClientGuid)
	}
}

func TestDirectUpdateInboundClientLegacyGuidBackfillAloneRemainsNoop(t *testing.T) {
	setupBulkDB(t)
	inboundSvc := &InboundService{}
	svc := &ClientService{}

	client := model.Client{ID: uuid.NewString(), Email: "legacy-guid-noop@x", SubID: "legacy-guid-noop-sub", Enable: true, CreatedAt: 111, UpdatedAt: 222}
	ib := mkInbound(t, 31091, model.VLESS, clientsSettings(t, []model.Client{client}))
	if err := svc.SyncInbound(nil, ib.Id, []model.Client{client}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}

	var rec model.ClientRecord
	if err := database.GetDB().Where("email = ?", client.Email).First(&rec).Error; err != nil {
		t.Fatalf("load canonical client: %v", err)
	}
	if strings.TrimSpace(rec.ClientGuid) == "" {
		t.Fatal("canonical client did not receive ClientGuid")
	}

	before, err := inboundSvc.GetInbound(ib.Id)
	if err != nil {
		t.Fatalf("GetInbound before: %v", err)
	}
	needRestart, err := svc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: ib.Id, Protocol: model.VLESS, Settings: clientsSettings(t, []model.Client{client})}, client.Email)
	if err != nil {
		t.Fatalf("UpdateInboundClient: %v", err)
	}
	if needRestart {
		t.Fatal("legacy guid-only no-op unexpectedly requested restart")
	}
	after, err := inboundSvc.GetInbound(ib.Id)
	if err != nil {
		t.Fatalf("GetInbound after: %v", err)
	}
	if after.Settings != before.Settings {
		t.Fatal("legacy guid-only no-op rewrote inbound settings")
	}
}

func TestDirectUpdateInboundClientLegacyRealEditPersistsClientGuid(t *testing.T) {
	setupBulkDB(t)
	inboundSvc := &InboundService{}
	svc := &ClientService{}

	client := model.Client{ID: uuid.NewString(), Email: "legacy-guid-edit@x", SubID: "legacy-guid-edit-sub", Enable: true, CreatedAt: 111, UpdatedAt: 222}
	ib := mkInbound(t, 31092, model.VLESS, clientsSettings(t, []model.Client{client}))
	if err := svc.SyncInbound(nil, ib.Id, []model.Client{client}); err != nil {
		t.Fatalf("SyncInbound: %v", err)
	}

	var rec model.ClientRecord
	if err := database.GetDB().Where("email = ?", client.Email).First(&rec).Error; err != nil {
		t.Fatalf("load canonical client: %v", err)
	}
	if strings.TrimSpace(rec.ClientGuid) == "" {
		t.Fatal("canonical client did not receive ClientGuid")
	}

	edited := client
	edited.LimitIP = 2
	if _, err := svc.UpdateInboundClient(inboundSvc, &model.Inbound{Id: ib.Id, Protocol: model.VLESS, Settings: clientsSettings(t, []model.Client{edited})}, client.Email); err != nil {
		t.Fatalf("UpdateInboundClient: %v", err)
	}
	stored, err := inboundSvc.GetInbound(ib.Id)
	if err != nil {
		t.Fatalf("GetInbound: %v", err)
	}
	clients, err := inboundSvc.GetClients(stored)
	if err != nil {
		t.Fatalf("GetClients: %v", err)
	}
	if len(clients) != 1 || clients[0].ClientGuid != rec.ClientGuid {
		t.Fatalf("real legacy edit did not persist canonical ClientGuid: %+v want %q", clients, rec.ClientGuid)
	}
}
