package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func nodeAuthDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func seedNode(t *testing.T, db *gorm.DB, n *model.Node) {
	t.Helper()
	if err := db.Create(n).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}
}

// perform sends a request with the given node_id/token query params through
// the NodeAuth middleware and returns the response status.
func perform(db *gorm.DB, nodeID, token string) int {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/probe", NodeAuth(db), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/probe?node_id="+nodeID+"&token="+token, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

// TestNodeAuthRejectsVirtualNode verifies that a virtual child node cannot
// authenticate even with its (placeholder) token: only real nodes poll the
// agent API, so a leaked virtual-row credential must be useless.
func TestNodeAuthRejectsVirtualNode(t *testing.T) {
	db := nodeAuthDB(t)
	parent := &model.Node{ID: "parent-id-01", Name: "parent", Token: "parent-secret", Address: "p:443", Port: 443, Security: "none", Enabled: true}
	seedNode(t, db, parent)
	virtual := &model.Node{ID: "virtual-id-01", Name: "virtual", Token: "virtual-id-01", Address: "1.1.1.1", ParentID: &parent.ID, Security: "none", Enabled: true}
	seedNode(t, db, virtual)

	if code := perform(db, "virtual-id-01", "virtual-id-01"); code != http.StatusUnauthorized {
		t.Errorf("virtual node auth status = %d, want 401", code)
	}
	if code := perform(db, "parent-id-01", "parent-secret"); code != http.StatusOK {
		t.Errorf("real node auth status = %d, want 200", code)
	}
}

// TestNodeAuthRejectsDisabledAndBadToken covers the remaining rejection paths:
// disabled nodes and wrong tokens must not authenticate.
func TestNodeAuthRejectsDisabledAndBadToken(t *testing.T) {
	db := nodeAuthDB(t)
	node := &model.Node{ID: "node-id-01", Name: "n", Token: "secret-01", Address: "p:443", Port: 443, Security: "none", Enabled: true}
	seedNode(t, db, node)
	// Disable via an explicit UPDATE: the Enabled column carries a
	// default:true tag, so a zero-value create would silently store true.
	if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Update("enabled", false).Error; err != nil {
		t.Fatalf("disable node: %v", err)
	}

	if code := perform(db, "node-id-01", "secret-01"); code != http.StatusUnauthorized {
		t.Errorf("disabled node auth status = %d, want 401", code)
	}
	if err := db.Model(&model.Node{}).Where("id = ?", node.ID).Update("enabled", true).Error; err != nil {
		t.Fatalf("enable node: %v", err)
	}
	if code := perform(db, "node-id-01", "wrong-secret"); code != http.StatusUnauthorized {
		t.Errorf("bad token status = %d, want 401", code)
	}
}
