package dto

import (
	"time"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// --- User management ---

// UserRequest is the create/update body for a user. Password is handled via
// the separate SetPassword endpoint, not here.
type UserRequest struct {
	Email             string     `json:"email" binding:"required"`
	Username          *string    `json:"username"`
	Level             int        `json:"level"`
	ExpireAt          *time.Time `json:"expire_at"`
	QuotaBytes        int64      `json:"quota_bytes"`
	QuotaResetEnabled bool       `json:"quota_reset_enabled"`
	Enabled           *bool      `json:"enabled"`
	// MaxInvites overrides the user's invite quota (cap on successful
	// registrations they may sponsor). 0 ⇒ use the global default.
	MaxInvites *int `json:"max_invites"`
}

// UserWithSubToken is the create/regenerate response: the user plus the
// subscription token (hidden on model.User via json:"-").
type UserWithSubToken struct {
	*model.User
	SubToken string `json:"sub_token"`
}

type SetPasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// ChangePasswordRequest is the self-service change-password body. OldPassword
// may be empty only when the caller has no password set yet (first-time setup).
type ChangeUserPasswordRequest struct {
	OldPassword string `json:"old_password"`
	NewPassword string `json:"new_password" binding:"required"`
}

type SetUserNodesRequest struct {
	NodeIDs []string `json:"node_ids" binding:"required"`
}

// --- User auth ---

type UserLoginRequest struct {
	// Login is by email (the unique account key); Username is display-only.
	Email        string `json:"email" binding:"required"`
	Password     string `json:"password" binding:"required"`
	CaptchaToken string `json:"cf_turnstile_response"`
}

type UserRegisterRequest struct {
	Username     string `json:"username" binding:"required"`
	Email        string `json:"email" binding:"required"`
	Password     string `json:"password" binding:"required"`
	InviteCode   string `json:"invite_code"`
	CaptchaToken string `json:"cf_turnstile_response"`
}

type UserLoginResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Level     int       `json:"level"`
}

type UserConfigResponse struct {
	RegisterEnabled            bool   `json:"register_enabled"`
	RegisterRequireInvite      bool   `json:"register_require_invite"`
	RegisterRequireEmailVerify bool   `json:"register_require_email_verify"`
	CaptchaEnabled             bool   `json:"captcha_enabled"`
	CaptchaSiteKey             string `json:"captcha_site_key"`
}
