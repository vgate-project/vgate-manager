// Package handler contains the gin HTTP handlers, organized by feature.
package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
	"github.com/vgate-project/vgate-manager/internal/wire"
)

type ServerHandler struct {
	svc *service.ServerService
}

func NewServerHandler(svc *service.ServerService) *ServerHandler {
	return &ServerHandler{svc: svc}
}

// GetConfig serves GET /api/v1/server/config.
func (h *ServerHandler) GetConfig(c *gin.Context) {
	node := c.MustGet("node").(*model.Node)
	cfg, err := h.svc.FetchConfig(node)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	writeETaggedJSON(c, cfg)
}

// GetUsers serves GET /api/v1/server/users.
func (h *ServerHandler) GetUsers(c *gin.Context) {
	node := c.MustGet("node").(*model.Node)
	users, err := h.svc.FetchUsers(node.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	writeETaggedJSON(c, users)
}

// PostTraffic serves POST /api/v1/server/traffic.
func (h *ServerHandler) PostTraffic(c *gin.Context) {
	node := c.MustGet("node").(*model.Node)
	var deltas []wire.UserTraffic
	if err := c.ShouldBindJSON(&deltas); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.svc.ReportTraffic(node.ID, deltas); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
