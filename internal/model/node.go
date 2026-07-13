package model

import (
	"time"

	"gorm.io/datatypes"
)

// NodeOnlineWindow is how recently a node must have polled (LastSeenAt)
// to be considered online. Shared by stats.OverviewStats.NodeOnline and the
// per-node Online() computation.
const NodeOnlineWindow = 5 * time.Minute

// Node is a vgate server instance the manager controls. The manager issues a
// public ID (ULID) and a secret Token; the node presents both on each poll.
// Transport/security config is stored as JSON columns and materialized into a
// wire.Config when the node fetches it.
type Node struct {
	ID            string          `gorm:"primaryKey;size:26" json:"id"`              // ULID
	Name          string          `gorm:"size:128;not null" json:"name"`             // display name → share-link tag
	ParentID      *string         `gorm:"size:26;index" json:"parent_id,omitempty"`  // nil = real node; non-nil = virtual child pointing to parent Node.ID (inherits its transport config)
	ParentName    string          `gorm:"-" json:"parent_name,omitempty"`            // server-populated display name of ParentID; not persisted
	Address       string          `gorm:"size:255;not null" json:"address"`          // host:port for share links
	Port          int             `gorm:"not null" json:"port"`                      // server listen port
	Token         string          `gorm:"size:64;uniqueIndex;not null" json:"token"` // crypto-random secret
	Network       string          `gorm:"size:16;default:'tcp'" json:"network"`      // tcp|ws|xhttp
	Security      string          `gorm:"size:16;not null" json:"security"`          // none|tls|reality
	Settings      datatypes.JSON  `gorm:"type:json" json:"settings"`                 // transport settings (path, x_padding_bytes, ...)
	TLSConfig     *datatypes.JSON `gorm:"type:json" json:"tls_settings,omitempty"`
	RealityConfig *datatypes.JSON `gorm:"type:json" json:"reality_settings,omitempty"`
	VLESS         *datatypes.JSON `gorm:"type:json" json:"vless,omitempty"`         // v2 decryption config
	Flow          *string         `gorm:"size:32;default:''" json:"flow,omitempty"` // "" | xtls-rprx-vision
	Level         int             `gorm:"default:0" json:"level"`                   // access tier; a user may use the node only if user.level >= node.level (unless explicitly overridden)
	AllowInsecure bool            `gorm:"default:false" json:"allow_insecure"`      // toggles allowInsecure=1 in TLS links
	// TrafficMultiplier scales the bytes reported by this node's users when the
	// manager aggregates them (only applied on the manager side). 1 = no change;
	// >1 inflates reported traffic (e.g. for billing), <1 deflates it. Virtual
	// child nodes inherit their parent's multiplier.
	TrafficMultiplier float64    `gorm:"default:1" json:"traffic_multiplier"`
	LastSeenAt        *time.Time `gorm:"index" json:"last_seen_at,omitempty"` // liveness (updated each poll)
	Online            bool       `gorm:"-" json:"online"`                     // server-computed: LastSeenAt within NodeOnlineWindow
	Enabled           bool       `gorm:"default:true" json:"enabled"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

// IsOnline reports whether the node polled within NodeOnlineWindow.
func (n *Node) IsOnline() bool {
	return n.LastSeenAt != nil && time.Since(*n.LastSeenAt) <= NodeOnlineWindow
}
