package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type UserTrafficHandler struct {
	svc *service.TrafficService
}

func NewUserTrafficHandler(svc *service.TrafficService) *UserTrafficHandler {
	return &UserTrafficHandler{svc: svc}
}

// List serves GET /api/v1/user/traffic?node_id=&from=&to=&page=&page_size= —
// the caller's hourly traffic detail rows (raw bytes, multiplier, billed
// bytes), newest hour first, optionally filtered by entry point and hour
// range [from, to) (from inclusive, to exclusive; see parseHourParam).
func (h *UserTrafficHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	from, err := parseHourParam(c.Query("from"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	to, err := parseHourParam(c.Query("to"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	rows, total, err := h.svc.ListForUser(c.GetString("user_id"), c.Query("node_id"), from, to, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[service.UserTrafficRecord]{Items: rows, Total: total, Page: page, PageSize: pageSize})
}

// Hourly serves GET /api/v1/user/traffic/hourly — the caller's last-24h
// per-hour traffic deltas.
func (h *UserTrafficHandler) Hourly(c *gin.Context) {
	series, err := h.svc.HourlyForUser(c.GetString("user_id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, series)
}

// Stats serves GET /api/v1/user/traffic/stats?from=&to=&node_id= — the
// caller's aggregated usage over an optional hour range (default last 24h):
// totals for the summary cards, a zero-filled hour/day series for the trend
// chart and a per-node breakdown.
func (h *UserTrafficHandler) Stats(c *gin.Context) {
	from, err := parseHourParam(c.Query("from"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	to, err := parseHourParam(c.Query("to"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	stats, err := h.svc.Stats(c.GetString("user_id"), c.Query("node_id"), from, to)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, stats)
}
