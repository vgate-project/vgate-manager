package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/service"
)

type SubHandler struct {
	svc *service.SubscriptionService
}

func NewSubHandler(svc *service.SubscriptionService) *SubHandler {
	return &SubHandler{svc: svc}
}

// Subscribe serves GET /api/v1/sub/:sub_token.
// The response format is chosen by ?type= (clash|v2rayn|raw) or auto-detected
// from the User-Agent. v2rayn (default/unknown UA) returns a base64-encoded
// newline-joined vless:// list; clash returns a Clash.Meta YAML config.
func (h *SubHandler) Subscribe(c *gin.Context) {
	user, err := h.svc.GetBySubToken(c.Param("sub_token"))
	if err != nil {
		c.Status(http.StatusNotFound) // do not reveal existence
		return
	}
	ct, body, err := h.svc.Render(user, detectClientType(c))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	writeETagged(c, ct, body)
}
