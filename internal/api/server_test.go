package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/config"
	"github.com/vgate-project/vgate-manager/internal/api"
	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
	"github.com/vgate-project/vgate-manager/internal/wire"
)

func setupTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Admin{}, &model.Node{}, &model.User{},
		&model.UserNode{}, &model.UserNodeTraffic{}, &model.TrafficHourlyStat{},
		&model.RefreshToken{}, &model.SystemConfig{},
		&model.InviteCode{}, &model.EmailVerification{}, &model.Announcement{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func newRouter(db *gorm.DB) *gin.Engine {
	cfg := &config.Config{
		Log: config.LogConfig{Level: "warn", Format: "text"},
		JWT: config.JWTConfig{Secret: "test-secret", AccessTTLSecs: 3600, RefreshTTLSecs: 86400},
	}
	authSvc := service.NewAuthService(db, cfg.JWT.Secret,
		time.Duration(cfg.JWT.AccessTTLSecs)*time.Second,
		time.Duration(cfg.JWT.RefreshTTLSecs)*time.Second)
	sysCfg := service.NewSystemConfigService(db)
	return api.NewRouter(db, cfg, authSvc, sysCfg, &http.Server{})
}

const (
	testNodeID = "01TESTNODE0000000000000NOD" // 26 chars (ULID-shaped)
	testToken  = "secret-node-token"
	testUserID = "11111111-2222-3333-4444-555555555555"
	testEmail  = "user@example.com"
)

func seedNodeUser(t *testing.T, db *gorm.DB) {
	t.Helper()
	// Reality config as JSON column.
	rc := wire.RealityConfig{
		Target:     "example.com:443",
		ServerName: "sni.example.com",
		PrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", // 43 chars base64url
		ShortIds:   []string{"0123456789abcdef", ""},
	}
	rcJSON, _ := json.Marshal(rc)
	settingsJSON, _ := json.Marshal(map[string]any{"path": "/xhttp"})

	node := model.Node{
		ID:            testNodeID,
		Name:          "test-node",
		Address:       "example.com:443",
		Port:          443,
		Token:         testToken,
		Network:       "xhttp",
		Security:      "reality",
		Settings:      datatypes.JSON(settingsJSON),
		RealityConfig: (*datatypes.JSON)(&rcJSON),
		Flow:          new("xtls-rprx-vision"),
		Enabled:       true,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	user := model.User{
		ID:         testUserID,
		Credential: testUserID, // surfaced as wire.User.ID (the rotatable VLESS credential)
		Email:      testEmail,
		SubToken:   "subtoken123",
		Level:      1,
		ExpireAt:   new(time.Now().Add(24 * time.Hour)),
		Enabled:    true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := db.Create(&model.UserNode{UserID: user.ID, NodeID: node.ID}).Error; err != nil {
		t.Fatalf("seed assignment: %v", err)
	}
}

func TestServerConfig(t *testing.T) {
	db := setupTestDB(t)
	seedNodeUser(t, db)
	r := newRouter(db)

	// Valid token → 200 + config with stream materialized from JSON columns.
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/server/config?node_id="+testNodeID+"&token="+testToken, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var cfg wire.Config
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if cfg.Port != 443 {
		t.Errorf("Port = %d, want 443", cfg.Port)
	}
	if cfg.Stream.Network != "xhttp" || cfg.Stream.Security != "reality" {
		t.Errorf("Stream = %+v, want network=xhttp security=reality", cfg.Stream)
	}
	if cfg.Stream.RealityConfig == nil || cfg.Stream.RealityConfig.Target != "example.com:443" {
		t.Errorf("RealityConfig not materialized: %+v", cfg.Stream.RealityConfig)
	}
	if cfg.Stream.Settings["path"] != "/xhttp" {
		t.Errorf("Settings.path = %v, want /xhttp", cfg.Stream.Settings["path"])
	}
	if cfg.VLESS.Flow != "xtls-rprx-vision" {
		t.Errorf("VLESS.Flow = %q, want xtls-rprx-vision", cfg.VLESS.Flow)
	}

	// Wrong token → 401.
	req = httptest.NewRequest(http.MethodGet,
		"/api/v1/server/config?node_id="+testNodeID+"&token=WRONG", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: expected 401, got %d", w.Code)
	}

	// Missing params → 401.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/server/config", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing params: expected 401, got %d", w.Code)
	}
}

func TestServerUsers(t *testing.T) {
	db := setupTestDB(t)
	seedNodeUser(t, db)
	r := newRouter(db)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/server/users?node_id="+testNodeID+"&token="+testToken, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var users []wire.User
	if err := json.Unmarshal(w.Body.Bytes(), &users); err != nil {
		t.Fatalf("decode users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].ID != testUserID || users[0].Email != testEmail {
		t.Errorf("user = %+v", users[0])
	}

	// Expired user is excluded.
	expiredAt := time.Now().Add(-1 * time.Hour)
	db.Model(&model.User{}).Where("id = ?", testUserID).Update("expire_at", expiredAt)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/server/users?node_id="+testNodeID+"&token="+testToken, nil))
	json.Unmarshal(w.Body.Bytes(), &users)
	if len(users) != 0 {
		t.Errorf("expired user should be excluded, got %d users", len(users))
	}
}

func TestServerTraffic(t *testing.T) {
	db := setupTestDB(t)
	seedNodeUser(t, db)
	r := newRouter(db)

	// Report a delta.
	deltas := []wire.UserTraffic{{Email: testEmail, Up: 1024, Down: 2048}}
	body, _ := json.Marshal(deltas)
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/server/traffic?node_id="+testNodeID+"&token="+testToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// Verify cumulative totals aggregated.
	var u model.User
	if err := db.Where("email = ?", testEmail).First(&u).Error; err != nil {
		t.Fatalf("reload user: %v", err)
	}
	if u.UpTotal != 1024 || u.DownTotal != 2048 {
		t.Errorf("totals = up=%d down=%d, want 1024/2048", u.UpTotal, u.DownTotal)
	}

	// Verify node last_seen updated.
	var n model.Node
	db.Where("id = ?", testNodeID).First(&n)
	if n.LastSeenAt == nil {
		t.Errorf("node last_seen_at not updated")
	}

	// Unknown email is skipped (no ghost user, no error).
	deltas = []wire.UserTraffic{{Email: "ghost@example.com", Up: 10, Down: 10}}
	body, _ = json.Marshal(deltas)
	req = httptest.NewRequest(http.MethodPost,
		"/api/v1/server/traffic?node_id="+testNodeID+"&token="+testToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("unknown-email traffic: expected 200, got %d", w.Code)
	}
	var ghost model.User
	if err := db.Where("email = ?", "ghost@example.com").First(&ghost).Error; err == nil {
		t.Errorf("ghost user should not be created")
	}

	// Second delta for real user accumulates (not replaces).
	deltas = []wire.UserTraffic{{Email: testEmail, Up: 100, Down: 200}}
	body, _ = json.Marshal(deltas)
	req = httptest.NewRequest(http.MethodPost,
		"/api/v1/server/traffic?node_id="+testNodeID+"&token="+testToken, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), req)
	db.Where("email = ?", testEmail).First(&u)
	if u.UpTotal != 1124 || u.DownTotal != 2248 {
		t.Errorf("accumulated totals = up=%d down=%d, want 1124/2248", u.UpTotal, u.DownTotal)
	}
}
