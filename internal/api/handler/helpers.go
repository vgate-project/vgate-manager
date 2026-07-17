package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/service"
)

// writeErr maps common service errors to HTTP status codes. Pass the context,
// the error, and a fallback message for the 500 case. Returns true if handled.
func writeErr(c *gin.Context, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
	case errors.Is(err, service.ErrPendingOrderExists), errors.Is(err, service.ErrOrderNotPending):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, service.ErrEmailNotVerified):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case isValidationErr(err):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case isUniqueViolation(err):
		c.JSON(http.StatusConflict, gin.H{"error": "resource already exists"})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
	}
	return true
}

func isValidationErr(err error) bool {
	// service validateNode returns errors.New with messages we surface as 400.
	// Heuristic: validation errors carry these substrings. Cheap and adequate.
	msg := err.Error()
	for _, p := range []string{"invalid ", "is required", "mutually exclusive", "decode"} {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

func isUniqueViolation(err error) bool {
	// SQLite: "UNIQUE constraint failed"; Postgres: "duplicate key value".
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint") || strings.Contains(msg, "duplicate key")
}

// etagFor computes a strong ETag (double-quoted SHA-256 hex) for a payload.
// It changes iff the bytes change, so no version/revision column is needed.
func etagFor(b []byte) string {
	sum := sha256.Sum256(b)
	return `"` + hex.EncodeToString(sum[:]) + `"`
}

// writeETagged writes body with an ETag and honors If-None-Match. If the
// client sent a matching If-None-Match, it responds 304 Not Modified with no
// body, saving bandwidth on unchanged resources.
func writeETagged(c *gin.Context, contentType string, body []byte) {
	etag := etagFor(body)
	c.Header("ETag", etag)
	c.Header("Cache-Control", "no-cache")
	// RFC 7232: If-None-Match uses WEAK comparison, so a CDN-downgraded weak
	// ETag (W/"...") must match its strong form. Exact comparison here would
	// never return 304 behind such a CDN.
	if inm := c.GetHeader("If-None-Match"); inm != "" && stripWeak(inm) == stripWeak(etag) {
		c.Status(http.StatusNotModified)
		return
	}
	c.Data(http.StatusOK, contentType, body)
}

// stripWeak removes the optional "W/" weak-validator prefix for RFC 7232
// weak comparison.
func stripWeak(etag string) string {
	return strings.TrimPrefix(etag, "W/")
}

// writeETaggedJSON marshals data and writes it via writeETagged as JSON.
func writeETaggedJSON(c *gin.Context, data any) {
	body, err := json.Marshal(data)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	writeETagged(c, "application/json", body)
}

// detectClientType resolves which subscription format to serve. Precedence:
//  1. explicit ?type= query param (clash|v2rayn|raw|base64|surge)
//  2. User-Agent sniffing for common clients
//  3. "raw" plaintext default (preserves the original no-param behavior)
//
// Surge is unsupported (no VLESS) and falls back to v2rayn. Unknown values fall
// back to "raw" so a stray ?type= never silently switches to base64.
func detectClientType(c *gin.Context) string {
	if t := strings.ToLower(c.Query("type")); t != "" {
		switch t {
		case "clash":
			return "clash"
		case "v2rayn", "base64":
			return "v2rayn"
		case "raw":
			return "raw"
		case "surge":
			return "v2rayn" // unsupported → widest-compatible fallback
		default:
			return "raw"
		}
	}
	ua := strings.ToLower(c.GetHeader("User-Agent"))
	switch {
	case strings.Contains(ua, "clash") || strings.Contains(ua, "mihomo") ||
		strings.Contains(ua, "stash") || strings.Contains(ua, "nekobox") ||
		strings.Contains(ua, "sing-box") || strings.Contains(ua, "flclash") ||
		strings.Contains(ua, "clash-verge"):
		return "clash"
	case strings.Contains(ua, "v2rayn") || strings.Contains(ua, "v2rayng") ||
		strings.Contains(ua, "shadowrocket") || strings.Contains(ua, "quantumult") ||
		strings.Contains(ua, "loon") || strings.Contains(ua, "surge"):
		return "v2rayn"
	default:
		return "raw"
	}
}
