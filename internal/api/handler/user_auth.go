package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type UserAuthHandler struct {
	svc     *service.AuthService
	userSvc *service.UserService
	captcha *service.CaptchaService
	sysCfg  *service.SystemConfigService
}

func NewUserAuthHandler(svc *service.AuthService, userSvc *service.UserService, captcha *service.CaptchaService, sysCfg *service.SystemConfigService) *UserAuthHandler {
	return &UserAuthHandler{svc: svc, userSvc: userSvc, captcha: captcha, sysCfg: sysCfg}
}

// GetConfig serves GET /api/v1/user/config — returns public system settings.
func (h *UserAuthHandler) GetConfig(c *gin.Context) {
	whitelist, _ := h.sysCfg.GetRegisterEmailSuffixWhitelist()
	c.JSON(http.StatusOK, dto.UserConfigResponse{
		RegisterEnabled:              h.svc.IsRegisterEnabled(),
		RegisterRequireInvite:        h.svc.IsRegisterRequireInvite(),
		RegisterRequireEmailVerify:   h.svc.IsRegisterRequireEmailVerify(),
		RegisterEmailSuffixWhitelist: whitelist,
		CaptchaEnabled:               h.captcha.Enabled(),
		CaptchaSiteKey:               h.captcha.SiteKey(),
		SiteName:                     h.sysCfg.GetSiteName(),
	})
}

// Login serves POST /api/v1/user/login — email/password → JWT.
func (h *UserAuthHandler) Login(c *gin.Context) {
	var req dto.UserLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.captcha.Verify(req.CaptchaToken, c.ClientIP()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	token, exp, level, err := h.svc.UserLogin(req.Email, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	c.JSON(http.StatusOK, dto.UserLoginResponse{Token: token, ExpiresAt: exp, Level: level})
}

// Register serves POST /api/v1/user/register — creates a new account. When
// email verification is required the account is held pending and a 202 is
// returned (no auto-login); otherwise it auto-logs-in with a 201.
func (h *UserAuthHandler) Register(c *gin.Context) {
	var req dto.UserRegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.captcha.Verify(req.CaptchaToken, c.ClientIP()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	_, token, exp, pending, err := h.svc.RegisterUser(req.Username, req.Email, req.Password, req.InviteCode)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if pending {
		// 202 still carries a session: unverified accounts can log in (verification
		// only gates purchases/traffic), so the client auto-logs-in and the
		// dashboard's verify banner guides the user. The status stays 202 to
		// signal "registered, verification pending" for any client that cares.
		c.JSON(http.StatusAccepted, dto.UserLoginResponse{Token: token, ExpiresAt: exp, Level: 0})
		return
	}
	// Note: RegisterUser returns level 0 for new users.
	c.JSON(http.StatusCreated, dto.UserLoginResponse{Token: token, ExpiresAt: exp, Level: 0})
}

// VerifyEmail serves POST /api/v1/user/verify-email — activates a pending
// account by redeeming its emailed verification token. Public (rate-limited).
func (h *UserAuthHandler) VerifyEmail(c *gin.Context) {
	var req struct {
		Token        string `json:"token" binding:"required"`
		CaptchaToken string `json:"cf_turnstile_response"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.captcha.Verify(req.CaptchaToken, c.ClientIP()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.VerifyEmail(req.Token); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Email verified. Your account is now active — you can log in."})
}

// ResendVerification serves POST /api/v1/user/resend-verification — re-sends
// the registration verification email for a pending account. Public (rate-limited).
func (h *UserAuthHandler) ResendVerification(c *gin.Context) {
	var req dto.ResendVerificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.captcha.Verify(req.CaptchaToken, c.ClientIP()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.ResendVerification(req.Email); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"message": "If a pending account exists for that email, a new verification link has been sent.",
	})
}

// ChangePassword serves POST /api/v1/user/change-password — the caller rotates
// their own password after verifying the current one (passwordless users may
// set a first password by omitting it).
func (h *UserAuthHandler) ChangePassword(c *gin.Context) {
	var req dto.ChangeUserPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.userSvc.ChangeOwnPassword(c.GetString("user_id"), req.OldPassword, req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
