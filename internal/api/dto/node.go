package dto

import (
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/wire"
)

// --- Node management ---

// NodeRequest is the create/update body for a node. Enabled is a pointer so
// "false" is distinguishable from omitted (defaults to true on create).
type NodeRequest struct {
	Name            string              `json:"name" binding:"required"`
	ParentID        *string             `json:"parent_id"` // set to create a virtual child node that inherits the parent's transport config
	Address         string              `json:"address" binding:"required"`
	Port            int                 `json:"port"`
	Network         string              `json:"network"`
	Security        string              `json:"security"`
	Settings        map[string]any      `json:"settings"`
	TLSSettings     *wire.TLSConfig     `json:"tls_settings"`
	RealitySettings *wire.RealityConfig `json:"reality_settings"`
	VLESS           *wire.VLESS         `json:"vless"`
	Flow            string              `json:"flow"`
	Level           int                 `json:"level"`
	AllowInsecure   bool                `json:"allow_insecure"`
	// TrafficMultiplier scales reported traffic at aggregation time (1 = no
	// change). Omit/0 on create to use the default of 1. Virtual child nodes
	// inherit their parent's multiplier and ignore this value.
	TrafficMultiplier float64 `json:"traffic_multiplier"`
	// SpeedLimitUpBps / SpeedLimitDownBps cap the node's aggregate upload /
	// download throughput in bytes/sec (0 = unlimited). Ignored by virtual
	// child nodes (they inherit the parent's limit).
	SpeedLimitUpBps   int64 `json:"speed_limit_up_bps" binding:"gte=0"`
	SpeedLimitDownBps int64 `json:"speed_limit_down_bps" binding:"gte=0"`
	Enabled           *bool `json:"enabled"`
}

// NodeWithToken is the create/regenerate response: the node plus its secret
// token (which model.Node itself never serializes, via json:"-").
type NodeWithToken struct {
	*model.Node
	Token string `json:"token"`
}

type RealityKeyResponse struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
}
