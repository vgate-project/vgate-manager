package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

// ParsePaging reads page/page_size query params with sensible defaults.
// page is 1-based; page_size defaults to 20, clamped to [1, 1000].
func ParsePaging(c *gin.Context) (page, pageSize int) {
	page, _ = strconv.Atoi(c.DefaultQuery("page", "1"))
	pageSize, _ = strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 1000 {
		pageSize = 1000
	}
	return
}
