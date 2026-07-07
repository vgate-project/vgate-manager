package dto

import "time"

// UserNodeView is the node info a user sees on their dashboard, including a
// server-computed online status (last_seen within nodeOnlineWindow).
type UserNodeView struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Address    string     `json:"address"`
	Port       int        `json:"port"`
	Level      int        `json:"level"`
	Enabled    bool       `json:"enabled"`
	Online     bool       `json:"online"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	// TrafficMultiplier is the effective multiplier applied to this node's
	// reported traffic. Virtual child nodes inherit their parent's multiplier.
	TrafficMultiplier float64 `json:"traffic_multiplier"`
}

type OverviewStats struct {
	// Nodes
	NodeCount  int64 `json:"node_count"`
	NodeOnline int64 `json:"node_online"` // last_seen within 2 min
	// Users
	UserCount      int64 `json:"user_count"`
	OnlineUsers24h int64 `json:"online_users_24h"`
	// Traffic (24h, derived from the hourly series)
	Up24h   int64        `json:"up_24h"`
	Down24h int64        `json:"down_24h"`
	Series  []HourlyStat `json:"series"`

	OrderCount24h  int64 `json:"order_count_24h"`  // paid orders in last 24h
	OrderAmount24h int64 `json:"order_amount_24h"` // sum of their amounts, cents

	// User health (cheap conditional counts over the users table)
	ExpiringUsers7d     int64 `json:"expiring_users_7d"`     // expire_at set and within next 7d (incl. already expired)
	QuotaExhaustedUsers int64 `json:"quota_exhausted_users"` // quota_bytes>0 and used>=quota
	UnverifiedUsers     int64 `json:"unverified_users"`      // email_verified=false
	NewUsersToday       int64 `json:"new_users_today"`       // created since local midnight
	NewUsersYesterday   int64 `json:"new_users_yesterday"`   // created in [−48h, −24h) for day-over-day

	// Pending orders (status=pending) — operational attention metric
	OrderPendingCount int64 `json:"order_pending_count"`

	// Previous 24h values, for day-over-day (DoD) trend comparison
	Up24hPrev          int64 `json:"up_24h_prev"`
	Down24hPrev        int64 `json:"down_24h_prev"`
	OnlineUsers24hPrev int64 `json:"online_users_24h_prev"`
	OrderCount24hPrev  int64 `json:"order_count_24h_prev"`
	OrderAmount24hPrev int64 `json:"order_amount_24h_prev"`
}

type HourlyStat struct {
	Hour time.Time `json:"hour"`
	Up   int64     `json:"up"`
	Down int64     `json:"down"`
}
