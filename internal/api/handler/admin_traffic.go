package handler

import (
	"fmt"
	"net/http"
	"strings"
	"time"

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

// List serves GET /api/v1/admin/traffic?user_id=&node_id=&from=&to=&page=&page_size=
// from/to are optional hour-range bounds (RFC3339 timestamp or bare YYYY-MM-DD
// date parsed as UTC midnight); from is inclusive, to is exclusive.
func (h *AdminTrafficHandler) List(c *gin.Context) {
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
	rows, total, err := h.svc.List(c.Query("user_id"), c.Query("node_id"), from, to, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[service.TrafficRecord]{Items: rows, Total: total, Page: page, PageSize: pageSize})
}

// parseHourParam parses an optional traffic hour-range bound: an RFC3339
// timestamp or a bare YYYY-MM-DD date (treated as UTC midnight). Returns
// (nil, nil) when the value is empty; the result is truncated to the hour.
func parseHourParam(v string) (*time.Time, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			hour := t.UTC().Truncate(time.Hour)
			return &hour, nil
		}
	}
	return nil, fmt.Errorf("invalid time %q: want RFC3339 timestamp or YYYY-MM-DD date", v)
}
