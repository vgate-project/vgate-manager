package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/vgate-project/vgate-manager/internal/util"
	"gorm.io/gorm"
)

// loginAsUser creates a user via the admin API, sets a password, then logs in
// as that user, returning the user JWT.
func loginAsUser(t *testing.T, db *gorm.DB, r *gin.Engine, token string) string {
	t.Helper()
	email := "featureuser" + util.RandomToken(4) + "@example.com"
	createBody, _ := json.Marshal(map[string]any{
		"email":    email,
		"username": "feat_" + util.RandomToken(4),
		"level":    0,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(createBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &created)
	id := created["id"].(string)

	pwBody, _ := json.Marshal(map[string]string{"password": "Passw0rd!"})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/"+id+"/password", bytes.NewReader(pwBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("set password: %d %s", w.Code, w.Body.String())
	}

	loginBody, _ := json.Marshal(map[string]string{"username": created["username"].(string), "password": "Passw0rd!"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("user login: %d %s", w.Code, w.Body.String())
	}
	var loginResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &loginResp)
	return loginResp["token"].(string)
}

func TestFeatureInviteAnnouncementEmail(t *testing.T) {
	db := setupTestDB(t)
	r := newRouter(db)
	token := bootstrapAdminAndLogin(t, db, r)
	userTok := loginAsUser(t, db, r, token)

	authHeader := func(req *http.Request, tok string) {
		req.Header.Set("Authorization", "Bearer "+tok)
	}

	// --- Admin creates an invite code (no quota) ---
	invBody, _ := json.Marshal(map[string]any{"max_uses": 3, "note": "promo"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/invites", bytes.NewReader(invBody))
	req.Header.Set("Content-Type", "application/json")
	authHeader(req, token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin create invite: %d %s", w.Code, w.Body.String())
	}
	var inv map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &inv)
	if inv["code"] == "" || inv["max_uses"].(float64) != 3 {
		t.Fatalf("unexpected invite response: %v", inv)
	}

	// --- Admin lists invites ---
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/invites", nil)
	authHeader(req, token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("admin list invites: %d %s", w.Code, w.Body.String())
	}
	var invPage map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &invPage)
	if int(invPage["total"].(float64)) < 1 {
		t.Fatalf("expected at least 1 invite, got %v", invPage)
	}

	// --- Admin creates an announcement (active by default) ---
	annBody, _ := json.Marshal(map[string]any{"title": "Maintenance", "content": "Soon", "pinned": true, "active": true})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/announcements", bytes.NewReader(annBody))
	req.Header.Set("Content-Type", "application/json")
	authHeader(req, token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("admin create announcement: %d %s", w.Code, w.Body.String())
	}

	// --- User sees the active announcement ---
	req = httptest.NewRequest(http.MethodGet, "/api/v1/user/announcements", nil)
	authHeader(req, userTok)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("user list announcements: %d %s", w.Code, w.Body.String())
	}
	var annResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &annResp)
	items := annResp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 active announcement, got %d", len(items))
	}

	// --- Email send with no resolvable recipients must 400 ---
	emailBody, _ := json.Marshal(map[string]any{
		"recipients": "ids", "user_ids": []string{}, "subject": "Hi", "body": "x",
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/email/send", bytes.NewReader(emailBody))
	req.Header.Set("Content-Type", "application/json")
	authHeader(req, token)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("email send (no recipients) expected 400, got %d %s", w.Code, w.Body.String())
	}
}
