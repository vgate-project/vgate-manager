package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/service"
)

type AdminStatsHandler struct {
	svc *service.StatsService
}

func NewAdminStatsHandler(svc *service.StatsService) *AdminStatsHandler {
	return &AdminStatsHandler{svc: svc}
}

// Overview serves GET /api/v1/admin/stats/overview — node/user/traffic counts
// and an hourly traffic series for the last 24 hours.
func (h *AdminStatsHandler) Overview(c *gin.Context) {
	stats, err := h.svc.GetOverview()
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, stats)
}
