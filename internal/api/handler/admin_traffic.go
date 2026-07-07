package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type AdminTrafficHandler struct {
	svc *service.TrafficService
}

func NewAdminTrafficHandler(svc *service.TrafficService) *AdminTrafficHandler {
	return &AdminTrafficHandler{svc: svc}
}

// List serves GET /api/v1/admin/traffic?user_id=&node_id=&page=&page_size=
func (h *AdminTrafficHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	rows, total, err := h.svc.List(c.Query("user_id"), c.Query("node_id"), page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[service.TrafficRow]{Items: rows, Total: total, Page: page, PageSize: pageSize})
}
