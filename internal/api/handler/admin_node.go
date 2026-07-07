package handler

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"
	"gorm.io/datatypes"

	"github.com/vgate-project/vgate-manager/internal/api/dto"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

type AdminNodeHandler struct {
	svc *service.NodeService
}

func NewAdminNodeHandler(svc *service.NodeService) *AdminNodeHandler {
	return &AdminNodeHandler{svc: svc}
}

func (h *AdminNodeHandler) List(c *gin.Context) {
	page, pageSize := ParsePaging(c)
	nodes, total, err := h.svc.List(page, pageSize)
	if writeErr(c, err) {
		return
	}
	for i := range nodes {
		nodes[i].Online = nodes[i].IsOnline()
	}
	c.JSON(http.StatusOK, dto.Page[model.Node]{Items: nodes, Total: total, Page: page, PageSize: pageSize})
}

func (h *AdminNodeHandler) Create(c *gin.Context) {
	var req dto.NodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	node := &model.Node{}
	applyNodeRequest(&req, node)
	if err := h.svc.Create(node); err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, dto.NodeWithToken{Node: node, Token: node.Token})
}

func (h *AdminNodeHandler) Get(c *gin.Context) {
	node, err := h.svc.Get(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	node.Online = node.IsOnline()
	c.JSON(http.StatusOK, node)
}

func (h *AdminNodeHandler) Update(c *gin.Context) {
	node, err := h.svc.Get(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	var req dto.NodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	applyNodeRequest(&req, node) // preserves ID, Token, timestamps
	if err := h.svc.Update(node); err != nil {
		writeErr(c, err)
		return
	}
	c.JSON(http.StatusOK, node)
}

func (h *AdminNodeHandler) Delete(c *gin.Context) {
	if err := h.svc.Delete(c.Param("id")); err != nil {
		writeErr(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *AdminNodeHandler) RegenerateToken(c *gin.Context) {
	tok, err := h.svc.RegenerateToken(c.Param("id"))
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": tok})
}

// applyNodeRequest copies request fields onto a model.Node, marshaling the
// nested config objects (Settings/TLS/Reality/VLESS) into JSON columns. It does
// not touch ID, Token, or timestamps, so it is safe for both create and update.
func applyNodeRequest(req *dto.NodeRequest, node *model.Node) {
	node.Name = req.Name
	node.ParentID = req.ParentID
	node.Address = req.Address
	node.Port = req.Port
	node.Network = req.Network
	node.Security = req.Security
	node.Flow = new(req.Flow)
	node.Level = req.Level
	node.AllowInsecure = req.AllowInsecure
	// Default to 1 when unset/0 so stored value is always a valid multiplier.
	if req.TrafficMultiplier <= 0 {
		node.TrafficMultiplier = 1
	} else {
		node.TrafficMultiplier = req.TrafficMultiplier
	}
	if req.Enabled != nil {
		node.Enabled = *req.Enabled
	} else {
		node.Enabled = true
	}

	if req.Settings != nil {
		b, _ := json.Marshal(req.Settings)
		node.Settings = b
	} else {
		node.Settings = nil
	}

	if req.TLSSettings != nil {
		b, _ := json.Marshal(req.TLSSettings)
		node.TLSConfig = new(datatypes.JSON(b))
	} else {
		node.TLSConfig = nil
	}

	if req.RealitySettings != nil {
		b, _ := json.Marshal(req.RealitySettings)
		node.RealityConfig = new(datatypes.JSON(b))
	} else {
		node.RealityConfig = nil
	}

	if req.VLESS != nil {
		// Set defaults: SecondsFrom 300, SecondsTo 600
		if req.VLESS.SecondsFrom == 0 {
			req.VLESS.SecondsFrom = 300
		}
		if req.VLESS.SecondsTo == 0 {
			req.VLESS.SecondsTo = 600
		}
		b, _ := json.Marshal(req.VLESS)
		node.VLESS = new(datatypes.JSON(b))
	} else {
		node.VLESS = nil
	}
}
