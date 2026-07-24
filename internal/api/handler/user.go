package handler

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type UserHandler struct {
	userSvc *service.UserService
	subSvc  *service.SubscriptionService
	sysCfg  *service.SystemConfigService
}

func NewUserHandler(userSvc *service.UserService, subSvc *service.SubscriptionService, sysCfg *service.SystemConfigService) *UserHandler {
	return &UserHandler{userSvc: userSvc, subSvc: subSvc, sysCfg: sysCfg}
}

// Profile serves GET /api/v1/user/profile — the caller's own account/traffic.
func (h *UserHandler) Profile(c *gin.Context) {
	userID := c.GetString("user_id")
	user, err := h.userSvc.Get(userID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, user)
}

// UpdateProfile serves PUT /api/v1/user/profile — the caller edits their own
// profile (currently just the display username).
func (h *UserHandler) UpdateProfile(c *gin.Context) {
	var req dto.UpdateProfileRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.userSvc.UpdateUsername(c.GetString("user_id"), *req.Username); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// SetReminderChannel serves PUT /api/v1/user/reminder-channel — the caller
// chooses how they receive traffic reminders ("", "email", "telegram", "none").
func (h *UserHandler) SetReminderChannel(c *gin.Context) {
	var req dto.SetReminderChannelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	switch req.Channel {
	case "", "email", "telegram", "none":
		// valid
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid channel: must be \"\", email, telegram, or none"})
		return
	}
	if err := h.userSvc.SetReminderChannel(c.GetString("user_id"), req.Channel); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.Status(http.StatusNoContent)
}

// Subscribe serves GET /api/v1/user/subscribe — same payload as /sub/:sub_token
// but authenticated by JWT rather than the subscription token. Format is chosen
// by ?type= or User-Agent, identical to the public subscription endpoint.
func (h *UserHandler) Subscribe(c *gin.Context) {
	userID := c.GetString("user_id")
	user, err := h.userSvc.Get(userID)
	if writeErr(c, err) {
		return
	}
	// ?type= may request several formats at once (e.g. raw,base64). When more
	// than one is requested we return them as a JSON object keyed by token,
	// collapsing what used to be multiple calls to this endpoint into one.
	types := c.Query("type")
	if types == "" {
		ct, body, err := h.subSvc.Render(user, detectClientType(c))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		writeETagged(c, ct, body)
		return
	}
	tokens := strings.Split(types, ",")
	if len(tokens) == 1 {
		ct, body, err := h.subSvc.Render(user, clientTypeFromToken(tokens[0]))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		writeETagged(c, ct, body)
		return
	}
	out := make(map[string]string, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		_, body, err := h.subSvc.Render(user, clientTypeFromToken(t))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		out[t] = string(body)
	}
	c.JSON(http.StatusOK, out)
}

// Nodes serves GET /api/v1/user/nodes — the nodes assigned to the caller, with
// a server-computed online status.
func (h *UserHandler) Nodes(c *gin.Context) {
	nodes, err := h.userSvc.ListNodesForUser(c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	views := make([]dto.UserNodeView, 0, len(nodes))
	for _, n := range nodes {
		online := n.IsOnline()
		mult, err := h.userSvc.EffectiveTrafficMultiplier(n)
		if err != nil {
			writeErr(c, err)
			return
		}
		views = append(views, dto.UserNodeView{
			ID:                n.ID,
			Name:              n.Name,
			Address:           n.Address,
			Port:              n.Port,
			Level:             n.Level,
			Enabled:           n.Enabled,
			Online:            online,
			LastSeenAt:        n.LastSeenAt,
			TrafficMultiplier: mult,
		})
	}
	c.JSON(http.StatusOK, views)
}

// RegenerateCredential serves POST /api/v1/user/regenerate-credential — the
// caller rotates their own VLESS credential. The primary key is untouched, so
// a leaked credential is revoked immediately; the client must re-pull its
// subscription to pick up the new UUID.
func (h *UserHandler) RegenerateCredential(c *gin.Context) {
	userID := c.GetString("user_id")
	cred, err := h.userSvc.RegenerateCredential(userID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"credential": cred})
}

// ResetSubToken serves POST /api/v1/user/reset-sub-token — the caller rotates
// their own subscription token. The old /sub/:sub_token link is revoked
// immediately, so clients must re-pull the subscription to pick up the new one.
func (h *UserHandler) ResetSubToken(c *gin.Context) {
	userID := c.GetString("user_id")
	tok, err := h.userSvc.RegenerateSubToken(userID)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"sub_token": tok})
}

// SubscribeURL serves GET /api/v1/user/subscribe-url — returns a randomized
// subscription link for the caller. The admin-configured list of subscription
// base URLs (sub.base_urls) is consulted; when non-empty a random one is
// chosen and combined with the subscription path, otherwise the request origin
// is used as the fallback (preserving the previous behavior). Re-fetched on
// each call, so the returned domain varies per request.
func (h *UserHandler) SubscribeURL(c *gin.Context) {
	user, err := h.userSvc.Get(c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	baseURLs, err := h.sysCfg.GetSubBaseURLs()
	if err != nil {
		// A malformed stored value shouldn't break the endpoint — fall back to
		// the request origin instead of erroring out the subscription link.
		log.Warnf("subscribe-url: %v; falling back to request origin", err)
		baseURLs = nil
	}
	fallback := requestOrigin(c)
	url, base64URL := h.subSvc.SubscribeURL(user.SubToken, baseURLs, fallback)
	c.JSON(http.StatusOK, gin.H{"url": url, "base64_url": base64URL})
}

// requestOrigin derives the scheme://host the request arrived on, used as the
// fallback subscription base URL when no admin-configured base URLs exist.
func requestOrigin(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host
}
