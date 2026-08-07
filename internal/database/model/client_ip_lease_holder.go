package model

// ClientIPLeaseHolder is one node/local-agent claim on a logical client's
// source-IP slot. Multiple holders may reference the same ClientGuid+IP; that
// still consumes exactly one logical IP slot. ExpiresAt is Unix milliseconds
// and makes coordinator state survive panel-process restarts without turning a
// crashed node into a permanent lease.
type ClientIPLeaseHolder struct {
	Id         int    `json:"id" gorm:"primaryKey;autoIncrement"`
	ClientGuid string `json:"clientGuid" gorm:"column:client_guid;size:36;not null;uniqueIndex:uidx_client_ip_lease_holder,priority:1;index:idx_client_ip_lease_guid_ip,priority:1"`
	IP         string `json:"ip" gorm:"column:ip;size:45;not null;uniqueIndex:uidx_client_ip_lease_holder,priority:2;index:idx_client_ip_lease_guid_ip,priority:2"`
	HolderKey  string `json:"holderKey" gorm:"column:holder_key;size:128;not null;uniqueIndex:uidx_client_ip_lease_holder,priority:3"`
	ExpiresAt  int64  `json:"expiresAt" gorm:"column:expires_at;not null;index:idx_client_ip_lease_expiry"`
	CreatedAt  int64  `json:"createdAt" gorm:"autoCreateTime:milli"`
	UpdatedAt  int64  `json:"updatedAt" gorm:"autoUpdateTime:milli"`
}

func (ClientIPLeaseHolder) TableName() string { return "client_ip_lease_holders" }
