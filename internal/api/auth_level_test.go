package api_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/payment"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// TestUserLoginCarriesLevel verifies that the user's level is returned by the
// login endpoint, embedded in the JWT, and round-trips through VerifyToken.
func TestUserLoginCarriesLevel(t *testing.T) {
	db := setupTestDB(t)

	// Create a user with a non-default level and a password.
	hash, err := service.NewAuthService(db, "test-secret", time.Hour, time.Hour).HashPassword("secret-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user := model.User{
		ID:            "ulvl-user",
		Email:         "ulvl@example.com",
		Username:      new("ulvl"),
		Level:         7,
		PasswordHash:  &hash,
		Enabled:       true,
		EmailVerified: true,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	authSvc := service.NewAuthService(db, "test-secret", time.Hour, time.Hour)

	// Service returns the level (login by email).
	tok, exp, lvl, err := authSvc.UserLogin("ulvl@example.com", "secret-pass")
	if err != nil {
		t.Fatalf("UserLogin: %v", err)
	}
	if lvl != 7 {
		t.Errorf("UserLogin level = %d, want 7", lvl)
	}

	// JWT round-trips the level via VerifyToken.
	claims, err := authSvc.VerifyToken(tok)
	if err != nil {
		t.Fatalf("VerifyToken: %v", err)
	}
	if claims.Level != 7 {
		t.Errorf("claim level = %d, want 7", claims.Level)
	}

	// Login endpoint returns level in the JSON body.
	loginBody, _ := json.Marshal(map[string]any{"email": "ulvl@example.com", "password": "secret-pass"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/user/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	newRouter(db).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Token     string `json:"token"`
		ExpiresAt any    `json:"expires_at"`
		Level     int    `json:"level"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode login resp: %v", err)
	}
	if resp.Level != 7 {
		t.Errorf("login response level = %d, want 7", resp.Level)
	}
	_ = exp
}

// TestUserLoginEmailCaseInsensitive verifies that the email used to log in is
// matched case-insensitively. The create path lowercases the stored email, and
// the login input is lowercased too, so any casing authenticates.
func TestUserLoginEmailCaseInsensitive(t *testing.T) {
	db := setupTestDB(t)

	userSvc := service.NewUserService(db, nil)
	user := model.User{
		ID:            "ci-user",
		Email:         "MixedCase@example.com", // the create path lowercases this
		Username:      new("ci"),
		Level:         0,
		Enabled:       true,
		EmailVerified: true,
	}
	if err := userSvc.Create(&user, "secret-pass"); err != nil {
		t.Fatalf("create user: %v", err)
	}

	authSvc := service.NewAuthService(db, "test-secret", time.Hour, time.Hour)

	// Different casings of the email must all authenticate.
	for _, login := range []string{"mixedcase@example.com", "MIXEDCASE@EXAMPLE.COM", "MixedCase@example.com"} {
		if _, _, _, err := authSvc.UserLogin(login, "secret-pass"); err != nil {
			t.Errorf("UserLogin(%q): %v", login, err)
		}
	}

	// A wrong password still fails.
	if _, _, _, err := authSvc.UserLogin("MixedCase@example.com", "wrong"); err == nil {
		t.Error("UserLogin with wrong password succeeded")
	}
}

// TestUserLoginAllowsUnverifiedButBlocksPurchase verifies Option A: an enabled
// but unverified account can log in (so it can reach the dashboard and verify
// its email), but a self-service purchase is refused until the email is
// verified. Admins placing orders on the user's behalf are exempt.
func TestUserLoginAllowsUnverifiedButBlocksPurchase(t *testing.T) {
	db := setupTestDB(t)

	hash, err := service.NewAuthService(db, "test-secret", time.Hour, time.Hour).HashPassword("secret-pass")
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	user := model.User{
		ID:            "pending-user",
		Email:         "pending@example.com",
		Username:      new("pending"),
		Level:         0,
		PasswordHash:  &hash,
		Enabled:       true, // enabled, but not yet verified
		EmailVerified: false,
	}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	authSvc := service.NewAuthService(db, "test-secret", time.Hour, time.Hour)

	// Login now succeeds even though the email is unverified.
	if _, _, _, err := authSvc.UserLogin("pending@example.com", "secret-pass"); err != nil {
		t.Errorf("UserLogin refused unverified user: %v", err)
	}

	// But a self-service purchase is blocked until the email is verified.
	orderSvc := service.NewOrderService(db, nil, payment.NewRegistry(nil))
	pkg := model.TrafficPackage{ID: "tp1", Name: "pkg", QuotaBytes: 1 << 30, Price: 100, ValidityDays: 7, Enabled: true}
	if err := db.Create(&pkg).Error; err != nil {
		t.Fatalf("create package: %v", err)
	}
	if _, _, err := orderSvc.Create(user.ID, service.CreateOrderParams{Kind: model.OrderKindTraffic, TrafficPackageID: "tp1"}); err == nil {
		t.Error("Create succeeded for unverified user")
	} else if !errors.Is(err, service.ErrEmailNotVerified) {
		t.Errorf("Create returned %v, want ErrEmailNotVerified", err)
	}

	// After verifying, the gate opens: Create no longer returns
	// ErrEmailNotVerified (it may then fail at payment-provider resolution,
	// which is unrelated to email verification).
	if err := db.Model(&user).Update("email_verified", true).Error; err != nil {
		t.Fatalf("verify user: %v", err)
	}
	if _, _, err := orderSvc.Create(user.ID, service.CreateOrderParams{Kind: model.OrderKindTraffic, TrafficPackageID: "tp1"}); err != nil && errors.Is(err, service.ErrEmailNotVerified) {
		t.Errorf("Create after verify still returned ErrEmailNotVerified: %v", err)
	}
}
