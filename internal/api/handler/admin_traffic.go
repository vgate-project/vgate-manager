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

// Stats serves GET /api/v1/admin/traffic/stats?user_id=&node_id=&from=&to= —
// aggregated usage over an optional hour range (default last 24h): totals,
// a zero-filled hour/day series and a per-node breakdown. userID empty means
// all users.
func (h *AdminTrafficHandler) Stats(c *gin.Context) {
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
	stats, err := h.svc.Stats(c.Query("user_id"), c.Query("node_id"), from, to)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, stats)
}

// Aggregate serves GET /api/v1/admin/traffic/aggregate?group_by=user|node&
// user_id=&node_id=&from=&to=&page=&page_size= — usage summed per user or per
// entry point over the filtered range, sorted by total usage desc.
func (h *AdminTrafficHandler) Aggregate(c *gin.Context) {
	groupBy := c.DefaultQuery("group_by", "user")
	if groupBy != "user" && groupBy != "node" {
		c.JSON(http.StatusBadRequest, gin.H{"error": `group_by must be "user" or "node"`})
		return
	}
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
	rows, total, err := h.svc.Aggregate(groupBy, c.Query("user_id"), c.Query("node_id"), from, to, page, pageSize)
	if writeErr(c, err) {
		return
	}
	c.JSON(http.StatusOK, dto.Page[service.TrafficAggregateRow]{Items: rows, Total: total, Page: page, PageSize: pageSize})
}

// Export serves GET /api/v1/admin/traffic/export?user_id=&node_id=&from=&to=
// — the matching hourly detail rows as a CSV download (UTF-8 with BOM so
// Excel detects the encoding; UTC hour stamps).
func (h *AdminTrafficHandler) Export(c *gin.Context) {
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
	c.Header("Content-Disposition", `attachment; filename="traffic-`+time.Now().UTC().Format("20060102-150405")+`.csv"`)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	// BOM first so Excel opens the file as UTF-8.
	if _, err := c.Writer.Write([]byte{0xEF, 0xBB, 0xBF}); err != nil {
		writeErr(c, err)
		return
	}
	if err := h.svc.ExportCSV(c.Writer, c.Query("user_id"), c.Query("node_id"), from, to); err != nil {
		writeErr(c, err)
	}
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
