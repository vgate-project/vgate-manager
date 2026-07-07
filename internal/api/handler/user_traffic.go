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

// List serves GET /api/v1/user/traffic — the caller's own per-node traffic.
func (h *UserTrafficHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	rows, total, err := h.svc.ListForUser(c.GetString("user_id"), page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[service.UserTrafficRow]{Items: rows, Total: total, Page: page, PageSize: pageSize})
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
