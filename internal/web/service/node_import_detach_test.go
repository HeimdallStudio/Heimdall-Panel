package service

import (
	"testing"

	"github.com/mhsanaei/3x-ui/v3/internal/database"
	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func nodeImportEditPayload(node *model.Node, mode string, tags []string) *model.Node {
	return &model.Node{
		Name:                node.Name,
		Remark:              node.Remark,
		Scheme:              node.Scheme,
		Address:             node.Address,
		Port:                node.Port,
		BasePath:            node.BasePath,
		ApiToken:            node.ApiToken,
		Enable:              node.Enable,
		AllowPrivateAddress: node.AllowPrivateAddress,
		TlsVerifyMode:       node.TlsVerifyMode,
		PinnedCertSha256:    node.PinnedCertSha256,
		InboundSyncMode:     mode,
		InboundTags:         tags,
		OutboundTag:         node.OutboundTag,
	}
}

func createImportDetachNode(t *testing.T, mode string, tags []string, adoptedAt int64) *model.Node {
	t.Helper()
	node := &model.Node{
		Name:              "detach-node",
		Scheme:            "https",
		Address:           "127.0.0.1",
		Port:              2096,
		BasePath:          "/",
		ApiToken:          "tok",
		Enable:            true,
		TlsVerifyMode:     "verify",
		InboundSyncMode:   mode,
		InboundTags:       tags,
		InboundsAdoptedAt: adoptedAt,
		Status:            "online",
	}
	if err := database.GetDB().Create(node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	return node
}

func createImportDetachInbound(t *testing.T, nodeID int, tag string, port int) *model.Inbound {
	t.Helper()
	id := nodeID
	inbound := &model.Inbound{
		UserId:   1,
		NodeID:   &id,
		Tag:      tag,
		Remark:   tag,
		Enable:   false,
		Port:     port,
		Protocol: model.VLESS,
		Settings: `{"clients":[]}`,
	}
	if err := database.GetDB().Create(inbound).Error; err != nil {
		t.Fatalf("create inbound %q: %v", tag, err)
	}
	return inbound
}

func TestNodeServiceUpdateDetachesUnselectedImportedInboundsLocally(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	node := createImportDetachNode(t, "all", nil, 200)

	prefixedKeepTag := nodeTagPrefix(&node.Id) + "keep"
	keep := createImportDetachInbound(t, node.Id, prefixedKeepTag, 16033)
	drop := createImportDetachInbound(t, node.Id, "drop", 40575)

	client := &model.ClientRecord{
		Email:  "drop@example.com",
		Enable: true,
	}
	if err := db.Create(client).Error; err != nil {
		t.Fatalf("create client: %v", err)
	}
	if err := db.Create(&model.ClientInbound{
		ClientId:  client.Id,
		InboundId: drop.Id,
	}).Error; err != nil {
		t.Fatalf("create client link: %v", err)
	}
	if err := db.Create(&model.ClientInboundTraffic{
		ClientID:  client.Id,
		InboundID: drop.Id,
		Email:     client.Email,
		StatEmail: "drop-stat@example.com",
	}).Error; err != nil {
		t.Fatalf("create detailed traffic mapping: %v", err)
	}
	if err := db.Create(&model.Host{
		InboundId: drop.Id,
		Remark:    "drop-host",
	}).Error; err != nil {
		t.Fatalf("create host: %v", err)
	}
	fallbacks := []model.InboundFallback{
		{MasterId: drop.Id, ChildId: keep.Id},
		{MasterId: keep.Id, ChildId: drop.Id},
	}
	if err := db.Create(&fallbacks).Error; err != nil {
		t.Fatalf("create fallbacks: %v", err)
	}

	svc := &NodeService{}
	if err := svc.Update(
		node.Id,
		nodeImportEditPayload(
			node,
			"selected",
			[]string{"keep"},
		),
	); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var attached []model.Inbound
	if err := db.
		Where("node_id = ?", node.Id).
		Order("id ASC").
		Find(&attached).Error; err != nil {
		t.Fatalf("load attached inbounds: %v", err)
	}
	if len(attached) != 1 || attached[0].Id != keep.Id {
		t.Fatalf(
			"attached inbounds = %#v, want only keep id=%d",
			attached,
			keep.Id,
		)
	}

	checks := []struct {
		name  string
		model any
		where string
		args  []any
		want  int64
	}{
		{
			"dropped inbound",
			&model.Inbound{},
			"id = ?",
			[]any{drop.Id},
			0,
		},
		{
			"client link",
			&model.ClientInbound{},
			"inbound_id = ?",
			[]any{drop.Id},
			0,
		},
		{
			"detailed traffic",
			&model.ClientInboundTraffic{},
			"inbound_id = ?",
			[]any{drop.Id},
			0,
		},
		{
			"host",
			&model.Host{},
			"inbound_id = ?",
			[]any{drop.Id},
			0,
		},
		{
			"fallback",
			&model.InboundFallback{},
			"master_id = ? OR child_id = ?",
			[]any{drop.Id, drop.Id},
			0,
		},
		{
			"client record",
			&model.ClientRecord{},
			"id = ?",
			[]any{client.Id},
			1,
		},
	}

	for _, check := range checks {
		var count int64
		if err := db.
			Model(check.model).
			Where(check.where, check.args...).
			Count(&count).Error; err != nil {
			t.Fatalf("count %s: %v", check.name, err)
		}
		if count != check.want {
			t.Fatalf(
				"%s count = %d, want %d",
				check.name,
				count,
				check.want,
			)
		}
	}

	var stored model.Node
	if err := db.First(&stored, node.Id).Error; err != nil {
		t.Fatalf("reload node: %v", err)
	}
	if stored.InboundSyncMode != "selected" ||
		len(stored.InboundTags) != 1 ||
		stored.InboundTags[0] != "keep" {
		t.Fatalf(
			"stored selection = %q %#v, want selected [keep]",
			stored.InboundSyncMode,
			stored.InboundTags,
		)
	}
	if stored.InboundsAdoptedAt != 0 {
		t.Fatalf(
			"InboundsAdoptedAt = %d, want 0",
			stored.InboundsAdoptedAt,
		)
	}
	if !stored.ConfigDirty {
		t.Fatal("node update must remain config-dirty")
	}
}

func TestNodeServiceUpdateSelectedEmptyRepairsStaleAttachmentsAndAllowsDelete(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	node := createImportDetachNode(
		t,
		"selected",
		[]string{},
		200,
	)

	createImportDetachInbound(
		t,
		node.Id,
		"in-16033-tcp",
		16033,
	)
	createImportDetachInbound(
		t,
		node.Id,
		"in-40575-tcp",
		40575,
	)

	svc := &NodeService{}
	if err := svc.Update(
		node.Id,
		nodeImportEditPayload(
			node,
			"selected",
			[]string{},
		),
	); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var attached int64
	if err := db.
		Model(&model.Inbound{}).
		Where("node_id = ?", node.Id).
		Count(&attached).Error; err != nil {
		t.Fatalf("count attached: %v", err)
	}
	if attached != 0 {
		t.Fatalf(
			"attached inbounds = %d, want 0",
			attached,
		)
	}

	if err := svc.Delete(node.Id); err != nil {
		t.Fatalf(
			"Delete after selected-empty repair: %v",
			err,
		)
	}

	var nodes int64
	if err := db.
		Model(&model.Node{}).
		Where("id = ?", node.Id).
		Count(&nodes).Error; err != nil {
		t.Fatalf("count node: %v", err)
	}
	if nodes != 0 {
		t.Fatalf("node count = %d, want 0", nodes)
	}
}

func TestNodeServiceUpdateAllModeKeepsAttachedInbounds(t *testing.T) {
	setupConflictDB(t)
	db := database.GetDB()
	node := createImportDetachNode(
		t,
		"selected",
		[]string{"a"},
		200,
	)

	createImportDetachInbound(t, node.Id, "a", 16033)
	createImportDetachInbound(t, node.Id, "b", 40575)

	svc := &NodeService{}
	if err := svc.Update(
		node.Id,
		nodeImportEditPayload(node, "all", nil),
	); err != nil {
		t.Fatalf("Update: %v", err)
	}

	var attached int64
	if err := db.
		Model(&model.Inbound{}).
		Where("node_id = ?", node.Id).
		Count(&attached).Error; err != nil {
		t.Fatalf("count attached: %v", err)
	}
	if attached != 2 {
		t.Fatalf(
			"attached inbounds = %d, want 2",
			attached,
		)
	}
}
