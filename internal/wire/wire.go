// Package wire holds the wire-format DTOs exchanged with the vgate server
// (node). These types are copied verbatim from server/model/* and MUST keep
// identical JSON tags so the server can decode them.
package wire

import "time"

// Config is the node's inbound configuration served by the manager.
type Config struct {
	Port   int    `json:"port"`
	Stream Stream `json:"stream"`
	VLESS  VLESS  `json:"vless,omitempty"`
	// SpeedLimitUpBps / SpeedLimitDownBps cap the node's aggregate upload /
	// download throughput in bytes/sec (0 = unlimited). Enforced by the node.
	SpeedLimitUpBps   int64 `json:"speed_limit_up_bps"`
	SpeedLimitDownBps int64 `json:"speed_limit_down_bps"`
	// TrafficSIDs maps Reality short IDs to the virtual child node each one
	// belongs to, so the node can attribute reported traffic to the entry
	// point (real parent vs virtual child) the client actually used. SIDs not
	// listed here fall back to this (real) node.
	TrafficSIDs []TrafficSID `json:"traffic_sids,omitempty"`
}

// TrafficSID binds one Reality short ID to the virtual child node that owns it.
type TrafficSID struct {
	NodeID string `json:"node_id"` // virtual child node ID
	SID    string `json:"sid"`     // lowercase hex short ID (≤ 16 chars)
}

// VLESS holds VLESS-protocol inbound settings (v2 AEAD decryption).
type VLESS struct {
	Decryption  string `json:"decryption,omitempty"`
	XorMode     uint32 `json:"xor_mode"`
	SecondsFrom int64  `json:"seconds_from,omitempty"`
	SecondsTo   int64  `json:"seconds_to,omitempty"`
	Padding     string `json:"padding,omitempty"`
	Flow        string `json:"flow,omitempty"`
}

// Stream mirrors xray-core's streamSettings: transport + security layers.
type Stream struct {
	Network       string         `json:"network,omitempty"`
	Settings      map[string]any `json:"settings,omitempty"`
	Security      string         `json:"security"` // none|tls|reality (REQUIRED)
	TLSConfig     *TLSConfig     `json:"tls_settings,omitempty"`
	RealityConfig *RealityConfig `json:"reality_settings,omitempty"`
}

// TLSConfig holds server-side (inbound) TLS configuration.
type TLSConfig struct {
	ServerName       string   `json:"server_name,omitempty"`
	CertFile         string   `json:"cert_file,omitempty"`
	KeyFile          string   `json:"key_file,omitempty"`
	CertPEM          string   `json:"cert_pem,omitempty"`
	KeyPEM           string   `json:"key_pem,omitempty"`
	ALPN             []string `json:"alpn,omitempty"`
	MinVersion       string   `json:"min_version,omitempty"`
	MaxVersion       string   `json:"max_version,omitempty"`
	RejectUnknownSNI bool     `json:"reject_unknown_sni,omitempty"`
}

// RealityConfig holds server-side (inbound) Reality configuration.
type RealityConfig struct {
	Show         bool     `json:"show,omitempty"`
	Target       string   `json:"target,omitempty"`
	Xver         int      `json:"xver,omitempty"`
	ServerName   string   `json:"server_name,omitempty"`
	PrivateKey   string   `json:"private_key,omitempty"`
	ShortIds     []string `json:"short_ids,omitempty"`
	MinClientVer string   `json:"min_client_ver,omitempty"`
	MaxClientVer string   `json:"max_client_ver,omitempty"`
	MaxTimeDiff  int      `json:"max_time_diff,omitempty"`
}

// User represents a VLESS user pushed to a node.
type User struct {
	ID       string    `json:"id"`    // UUID — the VLESS credential
	Email    string    `json:"email"` // traffic-accounting key
	Level    int       `json:"level"`
	ExpireAt time.Time `json:"expire_at"`
	// SpeedLimitUpBps / SpeedLimitDownBps cap this user's upload / download
	// throughput in bytes/sec (0 = unlimited).
	SpeedLimitUpBps   int64 `json:"speed_limit_up_bps"`
	SpeedLimitDownBps int64 `json:"speed_limit_down_bps"`
}

// UserTraffic is a delta traffic report (bytes since last successful report,
// NOT cumulative). The manager aggregates these into totals. NodeID is the
// entry point the traffic was measured on (a virtual child of the reporting
// node, matched by Reality short ID); empty means the reporting node itself.
type UserTraffic struct {
	Email  string `json:"email"`
	NodeID string `json:"node_id,omitempty"`
	Up     int64  `json:"up"`
	Down   int64  `json:"down"`
}
