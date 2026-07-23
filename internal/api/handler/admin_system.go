package handler

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/vgate-project/vgate-manager/internal/service"
)

type SystemHandler struct {
	svc *service.SystemConfigService
	srv *http.Server
}

func NewSystemHandler(svc *service.SystemConfigService, srv *http.Server) *SystemHandler {
	return &SystemHandler{svc: svc, srv: srv}
}

// Get serves GET /api/v1/admin/system-config.
func (h *SystemHandler) Get(c *gin.Context) {
	cfg, err := h.svc.GetAll()
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, cfg)
}

// Update serves PUT /api/v1/admin/system-config (body: {key: value, ...}).
func (h *SystemHandler) Update(c *gin.Context) {
	var body map[string]string
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.SetAll(body); writeErr(c, err) {
		return
	}
	// Apply log config changes immediately (no restart) for the hot-reload keys.
	if lvl, ok := body[service.CfgKeyLogLevel]; ok {
		level, err := log.ParseLevel(lvl)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log.level: " + err.Error()})
			return
		}
		log.SetLevel(level)
	}
	if fmt, ok := body[service.CfgKeyLogFormat]; ok {
		switch fmt {
		case "json":
			log.SetFormatter(&log.JSONFormatter{})
		case "text", "":
			log.SetFormatter(&log.TextFormatter{FullTimestamp: true})
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid log.format: must be text or json"})
			return
		}
	}
	// Hot-apply server read/write timeouts to the live listener. http.Server
	// reads these per connection, so changing them affects new connections with
	// no restart. (server.port is file-based and requires a restart.)
	if v, ok := body[service.CfgKeyServerReadTimeoutSecs]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeyServerReadTimeoutSecs + ": " + err.Error()})
			return
		}
		h.srv.ReadTimeout = time.Duration(n) * time.Second
	}
	if v, ok := body[service.CfgKeyServerWriteTimeoutSecs]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeyServerWriteTimeoutSecs + ": " + err.Error()})
			return
		}
		h.srv.WriteTimeout = time.Duration(n) * time.Second
	}
	// Validate subscription base URLs: must be a JSON array of absolute
	// http/https origins. Invalid edits are rejected immediately so the admin
	// gets feedback rather than a silently broken subscription link.
	if v, ok := body[service.CfgKeySubBaseURLs]; ok {
		var urls []string
		if err := json.Unmarshal([]byte(v), &urls); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeySubBaseURLs + ": must be a JSON array of URLs"})
			return
		}
		for _, u := range urls {
			trimmed := strings.TrimRight(strings.TrimSpace(u), "/")
			parsed, err := url.ParseRequestURI(trimmed)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeySubBaseURLs + ": " + trimmed + " is not an absolute http/https URL"})
				return
			}
		}
	}
	// Validate the registration email-suffix whitelist: must be a JSON array of
	// strings. Invalid edits are rejected immediately so the admin gets
	// feedback rather than a silently broken registration gate.
	if v, ok := body[service.CfgKeyRegisterEmailSuffixWhitelist]; ok {
		var domains []string
		if err := json.Unmarshal([]byte(v), &domains); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeyRegisterEmailSuffixWhitelist + ": must be a JSON array of strings"})
			return
		}
	}
	// Validate the traffic-reminder keys so a broken admin edit is rejected
	// immediately rather than silently producing no reminders.
	if v, ok := body[service.CfgKeyReminderEnabled]; ok {
		if v != "true" && v != "false" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeyReminderEnabled + ": must be true or false"})
			return
		}
	}
	if v, ok := body[service.CfgKeyReminderPct]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeyReminderPct + ": must be an integer 1-100"})
			return
		}
	}
	if v, ok := body[service.CfgKeyReminderDays]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeyReminderDays + ": must be an integer >= 0"})
			return
		}
	}
	if v, ok := body[service.CfgKeyReminderCooldown]; ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid " + service.CfgKeyReminderCooldown + ": must be an integer >= 1"})
			return
		}
	}
	c.JSON(http.StatusOK, body)
}
