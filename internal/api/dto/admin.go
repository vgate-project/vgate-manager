package dto

import "time"

// --- Admin auth ---

type AdminLoginRequest struct {
	Username     string `json:"username" binding:"required"`
	Password     string `json:"password" binding:"required"`
	CaptchaToken string `json:"cf_turnstile_response"`
}

type AdminLoginResponse struct {
	Token        string    `json:"token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// AdminConfigResponse is the public (non-admin-auth) settings block consumed by
// the admin login page. It currently exposes only the captcha knobs so the SPA
// can decide whether to render the Turnstile widget.
type AdminConfigResponse struct {
	CaptchaEnabled bool   `json:"captcha_enabled"`
	CaptchaSiteKey string `json:"captcha_site_key"`
}

type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

type RefreshResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// --- Admin account management ---

type CreateAdminRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
	Role     string `json:"role"` // defaults to "admin"
}

type UpdatePasswordRequest struct {
	Password string `json:"password" binding:"required"`
}

// ChangeAdminPasswordRequest is the self-service admin change-password body.
// The caller must supply their current password; admins always have one set.
type ChangeAdminPasswordRequest struct {
	OldPassword string `json:"old_password" binding:"required"`
	NewPassword string `json:"new_password" binding:"required"`
}
