package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type AdminAuthHandler struct {
	svc     *service.AuthService
	captcha *service.CaptchaService
	sysCfg  *service.SystemConfigService
}

func NewAdminAuthHandler(svc *service.AuthService, captcha *service.CaptchaService, sysCfg *service.SystemConfigService) *AdminAuthHandler {
	return &AdminAuthHandler{svc: svc, captcha: captcha, sysCfg: sysCfg}
}

// GetConfig serves GET /api/v1/admin/config — public (unauthenticated) system
// settings consumed by the admin login page. Mirrors UserAuthHandler.GetConfig
// on the user side so the admin SPA can render the Turnstile widget only when
// captcha is actually enabled.
func (h *AdminAuthHandler) GetConfig(c *gin.Context) {
	c.JSON(http.StatusOK, dto.AdminConfigResponse{
		CaptchaEnabled: h.captcha.Enabled(),
		CaptchaSiteKey: h.captcha.SiteKey(),
		SiteName:       h.sysCfg.GetSiteName(),
	})
}

func (h *AdminAuthHandler) Login(c *gin.Context) {
	var req dto.AdminLoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.captcha.Verify(req.CaptchaToken, c.ClientIP()); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	access, refresh, exp, err := h.svc.AdminLogin(req.Username, req.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid credentials"})
		return
	}
	c.JSON(http.StatusOK, dto.AdminLoginResponse{Token: access, RefreshToken: refresh, ExpiresAt: exp})
}

func (h *AdminAuthHandler) Refresh(c *gin.Context) {
	var req dto.RefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	access, exp, err := h.svc.RefreshAdmin(req.RefreshToken)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, dto.RefreshResponse{Token: access, ExpiresAt: exp})
}

// ChangePassword serves POST /api/v1/admin/change-password — the caller rotates
// their own password after verifying the current one. All of the admin's other
// sessions are revoked (refresh tokens invalidated) on success.
func (h *AdminAuthHandler) ChangePassword(c *gin.Context) {
	var req dto.ChangeAdminPasswordRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.ChangeOwnAdminPassword(c.GetUint("admin_id"), req.OldPassword, req.NewPassword); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}
