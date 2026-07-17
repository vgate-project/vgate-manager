package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// AdminEmailHandler lets an admin broadcast an email to users and optionally
// also persist it as an announcement shown in the user SPA.
type AdminEmailHandler struct {
	emailSvc *service.EmailService
	annSvc   *service.AnnouncementService
	db       *gorm.DB
}

func NewAdminEmailHandler(emailSvc *service.EmailService, annSvc *service.AnnouncementService, db *gorm.DB) *AdminEmailHandler {
	return &AdminEmailHandler{emailSvc: emailSvc, annSvc: annSvc, db: db}
}

// Send serves POST /api/v1/admin/email/send — resolve recipients, deliver mail,
// and optionally create a matching announcement.
func (h *AdminEmailHandler) Send(c *gin.Context) {
	var req dto.AdminSendEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	emails, err := service.CollectTargetEmails(h.db, req.Recipients, req.UserIDs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(emails) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "no recipients found"})
		return
	}

	sent := 0
	var firstErr error
	for _, to := range emails {
		if serr := h.emailSvc.SendAnnouncement(to, req.Subject, req.Body); serr != nil {
			if firstErr == nil {
				firstErr = serr
			}
		} else {
			sent++
		}
	}

	// Optionally persist as an announcement so it also appears in the SPA.
	if req.CreateAnnouncement {
		adminID := c.GetUint("admin_id")
		if _, aerr := h.annSvc.Create(req.Subject, req.Body, req.Pinned, true, adminID); aerr != nil && firstErr == nil {
			firstErr = aerr
		}
	}

	if firstErr != nil {
		c.JSON(http.StatusOK, gin.H{
			"message": "Email sent with errors",
			"sent":    sent,
			"total":   len(emails),
			"error":   firstErr.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "Email sent", "sent": sent, "total": len(emails)})
}

// Test serves POST /api/v1/admin/email/test — send a single test email to the
// given recipient using the currently saved email configuration, so an admin
// can verify SMTP/Resend connectivity without broadcasting to users. The
// response carries ok=false (not an HTTP error) on delivery failure so the
// client can surface the underlying mail error to the admin.
func (h *AdminEmailHandler) Test(c *gin.Context) {
	var req dto.AdminTestEmailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	subject := req.Subject
	if subject == "" {
		subject = "VGate email test"
	}
	body := req.Body
	if body == "" {
		body = "<p>This is a test email sent from your VGate instance.</p>" +
			"<p>If you are reading this, your email configuration is working.</p>"
	}

	if err := h.emailSvc.Send(req.To, subject, body); err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "Test email sent to " + req.To})
}
