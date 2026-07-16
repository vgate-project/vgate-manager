package model

import "time"

// User is a VLESS end-user. ID is the VLESS UUID credential; Email is the
// traffic-accounting key. UpTotal/DownTotal are cumulative (aggregated from
// node-reported deltas). SubToken authenticates the share URL; PasswordHash is
// optional (enables /user/login when set).
type User struct {
	ID string `gorm:"primaryKey;size:36" json:"id"` // stable internal PK (NOT the VLESS credential)
	// Credential is the rotatable VLESS UUID sent to nodes (wire.User.ID) and
	// embedded in the subscription link. It is decoupled from ID so a leaked
	// credential can be regenerated without touching the primary key.
	Credential string `gorm:"uniqueIndex;size:36" json:"credential"`
	// CurrentProductID is the id of the currently active product applied to the
	// user — a plan id (from a paid plan order) or a traffic-package id (from a
	// paid traffic order). Set when the order effect is applied; nullable; not
	// cleared on expiry.
	CurrentProductID string `gorm:"size:36;index" json:"current_product_id,omitempty"`
	// CurrentProductName is the display name of CurrentProductID, populated by
	// the service layer (not stored). Empty when no active product or the
	// product no longer exists.
	CurrentProductName string `gorm:"-" json:"current_product_name,omitempty"`
	// CurrentProductKind is the kind of CurrentProductID ("plan" | "traffic"),
	// persisted when the subscription effect is applied (applyPlanEffect /
	// applyTrafficEffect). Empty when no active product.
	CurrentProductKind string `gorm:"size:16;default:''" json:"current_product_kind,omitempty"`
	// CurrentPlanResetEnabled / CurrentPlanResetPrice surface the reset package
	// of the user's currently active plan. Populated by UserService.Get; empty
	// when the active product is a traffic package or none. Not stored.
	CurrentPlanResetEnabled bool    `gorm:"-" json:"current_plan_reset_enabled,omitempty"`
	CurrentPlanResetPrice   int64   `gorm:"-" json:"current_plan_reset_price,omitempty"`
	Email                   string  `gorm:"uniqueIndex;size:255;not null" json:"email"`
	Username                *string `gorm:"uniqueIndex;size:64" json:"username,omitempty"`
	PasswordHash            *string `gorm:"size:128" json:"-"` // bcrypt, nullable
	// HasPassword is a derived flag (not stored) exposing whether the user has
	// a password set, so the client can decide whether to prompt for the
	// current password when changing it.
	HasPassword       bool       `gorm:"-" json:"has_password,omitempty"`
	SubToken          string     `gorm:"uniqueIndex;size:32;not null" json:"sub_token"` // crypto-random share-URL credential
	Level             int        `gorm:"default:0" json:"level"`
	ExpireAt          *time.Time `gorm:"index" json:"expire_at,omitempty"`
	QuotaBytes        int64      `gorm:"default:0" json:"quota_bytes"`             // 0 = unlimited
	QuotaResetEnabled bool       `gorm:"default:false" json:"quota_reset_enabled"` // participates in global monthly reset (reset day from system_config)
	UpTotal           int64      `gorm:"default:0" json:"up_total"`
	DownTotal         int64      `gorm:"default:0" json:"down_total"`
	LastTrafficAt     *time.Time `gorm:"index" json:"last_traffic_at,omitempty"` // last node-reported traffic delta
	LastResetAt       *time.Time `json:"last_reset_at,omitempty"`
	Enabled           bool       `gorm:"default:true" json:"enabled"`
	// EmailVerified is set true once the user proves ownership of Email (e.g.
	// via the registration verification link). Surfaced to admins so pending
	// (registered-but-unverified) accounts are visible.
	EmailVerified bool `gorm:"default:false" json:"email_verified"`
	// MaxInvites caps how many successful registrations this user may sponsor
	// via invite codes they generate. 0 means "use the global default"
	// (system_config invite.default_user_quota). Admin-set overrides apply.
	MaxInvites int `gorm:"default:0" json:"max_invites"`
	// Telegram integration fields. TelegramID is the chat id of the user's
	// linked Telegram account (0 = not linked). TelegramNotify gates
	// announcement broadcasts; it defaults to true once linked so the user
	// receives announcements unless they opt out. The bind token is a
	// one-time code (with expiry) exchanged via /start <code> to link the
	// account; both are cleared after a successful bind.
	TelegramID            int64      `gorm:"index" json:"telegram_id"`
	TelegramBoundAt       *time.Time `json:"telegram_bound_at,omitempty"`
	TelegramNotify        bool       `gorm:"default:true" json:"telegram_notify"`
	TelegramBindToken     string     `gorm:"size:32;index" json:"-"`
	TelegramBindExpiresAt *time.Time `json:"-"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}
