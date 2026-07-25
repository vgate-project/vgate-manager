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
	// CurrentProductID is the id of the user's currently active plan, set when
	// a paid plan order's effect is applied. Traffic packages are add-ons and
	// never become the current product, so this is always a plan id or empty;
	// nullable and not cleared on expiry.
	CurrentProductID string `gorm:"size:36;index" json:"current_product_id,omitempty"`
	// CurrentProductName is the display name of CurrentProductID, populated by
	// the service layer (not stored). Empty when no active product or the
	// product no longer exists.
	CurrentProductName string  `gorm:"-" json:"current_product_name,omitempty"`
	Email              string  `gorm:"uniqueIndex;size:255;not null" json:"email"`
	Username           *string `gorm:"uniqueIndex;size:64" json:"username,omitempty"`
	PasswordHash       *string `gorm:"size:128" json:"-"` // bcrypt, nullable
	// HasPassword is a derived flag (not stored) exposing whether the user has
	// a password set, so the client can decide whether to prompt for the
	// current password when changing it.
	HasPassword       bool       `gorm:"-" json:"has_password,omitempty"`
	SubToken          string     `gorm:"uniqueIndex;size:32;not null" json:"sub_token"` // crypto-random share-URL credential
	Level             int        `gorm:"default:0" json:"level"`
	ExpireAt          *time.Time `gorm:"index" json:"expire_at,omitempty"`
	QuotaBytes        int64      `gorm:"default:0" json:"quota_bytes"`         // base traffic cap in bytes: -1 = unlimited, 0 = no quota (blocked), >0 = capped. Set by plans; traffic-package/redemption bonuses live in TrafficQuotaBytes.
	TrafficQuotaBytes int64      `gorm:"default:0" json:"traffic_quota_bytes"` // sum of active (non-expired) traffic-package / redemption bonuses, reclaimed on expiry
	// PackageUsedBytes is the cumulative traffic (bytes) charged to
	// traffic-package / redemption grants (FIFO). It persists across base-quota
	// resets (monthly, manual, or plan renewal) so a reset renews the base
	// window without refunding package traffic already consumed. The remaining
	// package pool is TrafficQuotaBytes; per-grant remaining is on TrafficGrant.
	PackageUsedBytes int64 `gorm:"default:0" json:"package_used_bytes"`
	// TrafficGrants lists the user's active (non-reclaimed) grants, populated
	// by UserService.Get for the profile / admin detail responses. Not stored.
	TrafficGrants     []TrafficGrant `gorm:"-" json:"traffic_grants,omitempty"`
	QuotaResetEnabled bool           `gorm:"default:false" json:"quota_reset_enabled"` // participates in global monthly reset (reset day from system_config)
	// BalanceCents is the user's spendable account-balance wallet (cents). It
	// can pay for any purchase (plans, traffic packages) and is credited
	// when a plan change refunds the remaining value of the old plan.
	BalanceCents int64 `gorm:"default:0" json:"balance_cents"`
	// CurrentPlanPaidCents / CurrentPlanDurationDays record what the user paid
	// for the CURRENT plan entitlement (gross cents + the duration of that
	// purchase). They enable per-day amortization so a mid-period plan change
	// can credit the old plan's remaining value. Only meaningful when the user
	// has a current plan (CurrentProductID is set). Exposed so the change-plan
	// dialog can preview the credit client-side.
	CurrentPlanPaidCents    int64 `gorm:"default:0" json:"current_plan_paid_cents"`
	CurrentPlanDurationDays int   `gorm:"default:0" json:"current_plan_duration_days"`
	// SpeedLimitUpBps / SpeedLimitDownBps cap this user's upload / download
	// throughput in bytes/sec (0 = unlimited). Enforced by the node; the
	// effective rate is min(node global limit, this per-user limit).
	SpeedLimitUpBps   int64      `gorm:"default:0" json:"speed_limit_up_bps"`
	SpeedLimitDownBps int64      `gorm:"default:0" json:"speed_limit_down_bps"`
	UpTotal           int64      `gorm:"default:0" json:"up_total"`
	DownTotal         int64      `gorm:"default:0" json:"down_total"`
	LastTrafficAt     *time.Time `gorm:"index" json:"last_traffic_at,omitempty"` // last node-reported traffic delta
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
	// ReminderChannel selects how this user receives traffic reminders.
	// "" means auto (Telegram if linked, else email if verified, else none);
	// explicit values are "email", "telegram", or "none" (disabled). The
	// thresholds and cooldown are configured globally by an admin.
	ReminderChannel string `gorm:"size:8;default:''" json:"reminder_channel"`
	// LastTrafficReminderAt is the timestamp of the last traffic reminder sent
	// to this user; it enforces the global cooldown (reminder.cooldown_days)
	// so a user is not reminded more than once per cooldown window.
	LastTrafficReminderAt *time.Time `json:"last_traffic_reminder_at,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
}

// EffectiveQuotaBytes returns the user's total traffic cap, combining the base
// plan quota (QuotaBytes, with -1 = unlimited) and any active traffic-package /
// redemption bonuses (TrafficQuotaBytes). A base of -1 stays unlimited
// regardless of bonuses; otherwise the two are summed.
func (u User) EffectiveQuotaBytes() int64 {
	if u.QuotaBytes < 0 {
		return -1
	}
	return u.QuotaBytes + u.TrafficQuotaBytes
}

// BaseUsedBytes returns the traffic charged against the base plan quota only
// (total used minus the package-used portion). Floored at 0. The package-used
// portion survives base resets, so this isolates the base window's consumption.
func (u User) BaseUsedBytes() int64 {
	used := u.UpTotal + u.DownTotal - u.PackageUsedBytes
	if used < 0 {
		return 0
	}
	return used
}
