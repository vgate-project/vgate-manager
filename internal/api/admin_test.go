package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
	"gorm.io/gorm"
)

// bootstrapAdminAndLogin seeds a super_admin and returns a JWT via POST /admin/login.
func bootstrapAdminAndLogin(t *testing.T, db *gorm.DB, r *gin.Engine) string {
	t.Helper()
	authSvc := service.NewAuthService(db, "test-secret", time.Hour, 24*time.Hour)
	pw, err := authSvc.BootstrapAdmin("admin", "pass123")
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if pw != "pass123" {
		t.Fatalf("expected provided password to be used, got %q", pw)
	}
	body, _ := json.Marshal(map[string]string{"username": "admin", "password": "pass123"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	return resp["token"].(string)
}

func TestAdminNodeCRUD(t *testing.T) {
	db := setupTestDB(t)
	r := newRouter(db)
	token := bootstrapAdminAndLogin(t, db, r)

	authHeader := func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+token) }

	// Create a Reality node.
	createBody, _ := json.Marshal(map[string]any{
		"name": "hk-1", "address": "hk.example.com:443", "port": 443,
		"network": "tcp", "security": "reality",
		"reality_settings": map[string]any{
			"target":      "example.com:443",
			"server_name": "sni.example.com",
			"private_key": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			"short_ids":   []string{"0123456789abcdef"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/nodes", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	authHeader(req)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	nodeID := created["id"].(string)
	if created["token"] == nil || created["token"].(string) == "" {
		t.Errorf("create response should expose the node token")
	}

	// List → 1 node.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/nodes", nil)
	authHeader(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Items []map[string]any `json:"items"`
		Total int64            `json:"total"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &list)
	if len(list.Items) != 1 {
		t.Errorf("expected 1 node, got %d", len(list.Items))
	}
	// Token must NOT be exposed on list.
	if list.Items[0]["token"] != nil {
		t.Errorf("token leaked on list: %v", list.Items[0]["token"])
	}

	// Get.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/nodes/"+nodeID, nil)
	authHeader(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("get: %d", w.Code)
	}

	// No Authorization → 401.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/nodes", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated list: expected 401, got %d", w.Code)
	}

	// Delete.
	req = httptest.NewRequest(http.MethodDelete, "/api/v1/admin/nodes/"+nodeID, nil)
	authHeader(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
}

func TestAdminValidation(t *testing.T) {
	db := setupTestDB(t)
	r := newRouter(db)
	token := bootstrapAdminAndLogin(t, db, r)

	// v2 + vision mutual exclusion → 400.
	body, _ := json.Marshal(map[string]any{
		"name": "bad", "address": "x:443", "port": 443,
		"network": "tcp", "security": "none",
		"vless": map[string]any{"decryption": "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
		"flow":  "xtls-rprx-vision",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/nodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("v2+vision: expected 400, got %d %s", w.Code, w.Body.String())
	}

	// Invalid security → 400.
	body, _ = json.Marshal(map[string]any{
		"name": "bad", "address": "x:443", "port": 443, "network": "tcp", "security": "bogus",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/nodes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid security: expected 400, got %d %s", w.Code, w.Body.String())
	}
}

// TestDeleteAdminGuards verifies the DeleteAdmin safety rules: an admin cannot
// delete their own account, and the last remaining super_admin cannot be
// deleted (to avoid locking out management).
func TestDeleteAdminGuards(t *testing.T) {
	db := setupTestDB(t)
	authSvc := service.NewAuthService(db, "test-secret", time.Hour, time.Hour)

	self, err := authSvc.CreateAdmin("self", "Passw0rd!", "super_admin")
	if err != nil {
		t.Fatalf("create self: %v", err)
	}
	other, err := authSvc.CreateAdmin("other", "Passw0rd!", "admin")
	if err != nil {
		t.Fatalf("create other: %v", err)
	}

	// Cannot delete your own account.
	if err := authSvc.DeleteAdmin(self.ID, self.ID); err == nil {
		t.Error("expected self-delete to be rejected")
	}
	// A non-existent caller must not be able to delete the last super_admin.
	if err := authSvc.DeleteAdmin(self.ID, 99999); err == nil {
		t.Error("expected last-super-admin delete to be rejected")
	}
	// Deleting another admin is allowed.
	if err := authSvc.DeleteAdmin(other.ID, self.ID); err != nil {
		t.Fatalf("delete other: %v", err)
	}

	// With more than one super_admin, deleting a (non-self) super_admin is fine.
	sup2, err := authSvc.CreateAdmin("sup2", "Passw0rd!", "super_admin")
	if err != nil {
		t.Fatalf("create sup2: %v", err)
	}
	if err := authSvc.DeleteAdmin(sup2.ID, self.ID); err != nil {
		t.Fatalf("delete sup2: %v", err)
	}

	// Sanity: the seeded admin still exists with the expected role.
	var got model.Admin
	if err := db.First(&got, self.ID).Error; err != nil {
		t.Fatalf("reload self: %v", err)
	}
	if got.Role != "super_admin" {
		t.Errorf("self role = %q, want super_admin", got.Role)
	}
}
