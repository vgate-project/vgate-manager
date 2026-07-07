package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	yaml "go.yaml.in/yaml/v3"

	vgcrypto "github.com/vgate-project/vgate-manager/pkg/crypto"
)

func TestSubscriptionLink(t *testing.T) {
	db := setupTestDB(t)
	r := newRouter(db)
	token := bootstrapAdminAndLogin(t, db, r)
	auth := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
	}

	// Create a TCP+Reality node with a freshly generated key pair.
	priv, _, err := vgcrypto.GenerateX25519KeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	expectedPBK, err := vgcrypto.DeriveX25519Public(priv)
	if err != nil {
		t.Fatalf("derive expected pbk: %v", err)
	}
	nodeBody, _ := json.Marshal(map[string]any{
		"name": "hk-1", "address": "hk.example.com:443", "port": 443,
		"network": "tcp", "security": "reality",
		"reality_settings": map[string]any{
			"target":      "example.com:443",
			"server_name": "sni.example.com",
			"private_key": priv,
			"short_ids":   []string{"0123456789abcdef"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/nodes", bytes.NewReader(nodeBody))
	auth(req)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", w.Code, w.Body.String())
	}
	var node map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &node)
	nodeID := node["id"].(string)

	// Create a user.
	userBody, _ := json.Marshal(map[string]any{"email": "sub@example.com"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(userBody))
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var user map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &user)
	userID := user["id"].(string)
	credential := user["credential"].(string)
	subToken := user["sub_token"].(string)

	// Assign user → node.
	assignBody, _ := json.Marshal(map[string]any{"node_ids": []string{nodeID}})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/"+userID+"/nodes", bytes.NewReader(assignBody))
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}

	// Fetch the subscription (plain).
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sub: %d %s", w.Code, w.Body.String())
	}
	link := strings.TrimSpace(w.Body.String())
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link %q: %v", link, err)
	}
	if u.Scheme != "vless" || u.User.Username() != credential || u.Host != "hk.example.com:443" {
		t.Errorf("link base = scheme=%s user=%s host=%s", u.Scheme, u.User.Username(), u.Host)
	}
	q := u.Query()
	// A Reality node without v2 decryption emits no `encryption` param
	// (absence means none); only v2 nodes set it to the derived pubkey.
	if q.Get("type") != "tcp" || q.Get("security") != "reality" || q.Get("encryption") != "" {
		t.Errorf("query = %s", q.Encode())
	}
	if q.Get("pbk") != expectedPBK {
		t.Errorf("pbk = %s, want %s", q.Get("pbk"), expectedPBK)
	}
	if q.Get("sni") != "sni.example.com" || q.Get("sid") != "0123456789abcdef" || q.Get("fp") != "chrome" {
		t.Errorf("reality params: sni=%s sid=%s fp=%s", q.Get("sni"), q.Get("sid"), q.Get("fp"))
	}
	if u.Fragment != "hk-1" {
		t.Errorf("fragment = %q, want hk-1", u.Fragment)
	}

	// Base64 format decodes to the same list.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken+"?fmt=base64", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Body.String()))
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	if strings.TrimSpace(string(decoded)) != link {
		t.Errorf("base64 mismatch: %q vs %q", string(decoded), link)
	}

	// ?type=v2rayn explicit → base64 of the same link.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken+"?type=v2rayn", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sub v2rayn: %d %s", w.Code, w.Body.String())
	}
	decV2, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Body.String()))
	if err != nil {
		t.Fatalf("v2rayn base64 decode: %v", err)
	}
	if strings.TrimSpace(string(decV2)) != link {
		t.Errorf("v2rayn mismatch: %q vs %q", string(decV2), link)
	}

	// ?type=clash → Clash.Meta YAML with a vless proxy.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken+"?type=clash", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sub clash: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "yaml") {
		t.Errorf("clash content-type = %q", ct)
	}
	var clash struct {
		Proxies []struct {
			Name   string `yaml:"name"`
			Type   string `yaml:"type"`
			Server string `yaml:"server"`
			Port   int    `yaml:"port"`
			UUID   string `yaml:"uuid"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(w.Body.Bytes(), &clash); err != nil {
		t.Fatalf("unmarshal clash yaml: %v\nbody: %s", err, w.Body.String())
	}
	if len(clash.Proxies) != 1 {
		t.Fatalf("clash proxies = %d, want 1", len(clash.Proxies))
	}
	cp := clash.Proxies[0]
	if cp.Type != "vless" || cp.Server != "hk.example.com" || cp.Port != 443 || cp.UUID != credential || cp.Name != "hk-1" {
		t.Errorf("clash proxy = %+v", cp)
	}

	// UA detection: Clash UA (no ?type) → YAML.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken, nil)
	req.Header.Set("User-Agent", "Clash/1.18.0")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if !strings.Contains(w.Header().Get("Content-Type"), "yaml") {
		t.Errorf("clash UA content-type = %q", w.Header().Get("Content-Type"))
	}

	// UA detection: v2rayN UA (no ?type) → base64 links.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken, nil)
	req.Header.Set("User-Agent", "v2rayN/6.0")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	decUA, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Body.String()))
	if err != nil {
		t.Fatalf("v2rayn UA base64 decode: %v", err)
	}
	if strings.TrimSpace(string(decUA)) != link {
		t.Errorf("v2rayn UA mismatch: %q vs %q", string(decUA), link)
	}

	// Surge UA → falls back to v2rayn base64 (Surge unsupported).
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken, nil)
	req.Header.Set("User-Agent", "Surge/4.0")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if _, err := base64.StdEncoding.DecodeString(strings.TrimSpace(w.Body.String())); err != nil {
		t.Errorf("surge UA should fall back to base64 v2rayn: %v", err)
	}
}

// TestRegenerateCredential verifies that rotating a user's VLESS credential
// changes the UUID embedded in the subscription link and leaves the primary
// key / sub_token untouched (so leaked credentials can be revoked without
// disturbing the account).
func TestRegenerateCredential(t *testing.T) {
	db := setupTestDB(t)
	r := newRouter(db)
	token := bootstrapAdminAndLogin(t, db, r)
	auth := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
	}

	userBody, _ := json.Marshal(map[string]any{"email": "rot@example.com"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(userBody))
	auth(req)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var user map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &user)
	userID := user["id"].(string)
	oldCred := user["credential"].(string)
	if oldCred == "" {
		t.Fatalf("credential not set on create")
	}

	// Create a Reality node so the subscription link has a proxy to render.
	priv, _, err := vgcrypto.GenerateX25519KeyPair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	nodeBody, _ := json.Marshal(map[string]any{
		"name": "rot-node", "address": "rot.example.com:443", "port": 443,
		"network": "tcp", "security": "reality",
		"reality_settings": map[string]any{
			"target":      "example.com:443",
			"server_name": "sni.example.com",
			"private_key": priv,
			"short_ids":   []string{"0123456789abcdef"},
		},
	})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/nodes", bytes.NewReader(nodeBody))
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", w.Code, w.Body.String())
	}
	var node map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &node)
	nodeID := node["id"].(string)

	// Assign user → node.
	assignBody, _ := json.Marshal(map[string]any{"node_ids": []string{nodeID}})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/"+userID+"/nodes", bytes.NewReader(assignBody))
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}

	// Old credential appears in the subscription link.
	subToken := user["sub_token"].(string)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	oldLink := strings.TrimSpace(w.Body.String())
	if !strings.Contains(oldLink, oldCred) {
		t.Fatalf("old credential %s not in sub link %q", oldCred, oldLink)
	}

	// Regenerate via admin endpoint.
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users/"+userID+"/regenerate-credential", nil)
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("regenerate: %d %s", w.Code, w.Body.String())
	}
	var reg map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &reg)
	newCred, _ := reg["credential"].(string)
	if newCred == "" || newCred == oldCred {
		t.Fatalf("regenerate returned unchanged/empty credential: %q", newCred)
	}

	// Primary key and sub_token must be unchanged.
	var after map[string]any
	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/users/"+userID, nil)
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	_ = json.Unmarshal(w.Body.Bytes(), &after)
	if after["id"].(string) != userID {
		t.Errorf("primary key changed: %s -> %s", userID, after["id"])
	}
	if after["sub_token"].(string) != subToken {
		t.Errorf("sub_token changed after credential rotation")
	}
	if after["credential"].(string) != newCred {
		t.Errorf("stored credential not updated: %s", after["credential"])
	}

	// New subscription link carries the new credential (not the old).
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	newLink := strings.TrimSpace(w.Body.String())
	if !strings.Contains(newLink, newCred) {
		t.Fatalf("new credential %s not in sub link %q", newCred, newLink)
	}
	if strings.Contains(newLink, oldCred) {
		t.Fatalf("old credential %s still present in sub link %q", oldCred, newLink)
	}
}

func TestUserLoginAndSubscribe(t *testing.T) {
	db := setupTestDB(t)
	r := newRouter(db)
	token := bootstrapAdminAndLogin(t, db, r)
	auth := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
	}

	// Create user + node + assignment via admin API.
	userBody, _ := json.Marshal(map[string]any{"email": "u2@example.com", "username": "u2"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(userBody))
	auth(req)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var user map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &user)
	userID := user["id"].(string)

	// Set a password.
	pwBody, _ := json.Marshal(map[string]any{"password": "secret-pass"})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/"+userID+"/password", bytes.NewReader(pwBody))
	auth(req)
	r.ServeHTTP(httptest.NewRecorder(), req)

	// User login.
	loginBody, _ := json.Marshal(map[string]any{"username": "u2", "password": "secret-pass"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("user login: %d %s", w.Code, w.Body.String())
	}
	var loginResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &loginResp)
	userJWT := loginResp["token"].(string)

	// GET /user/profile with the JWT.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/user/profile", nil)
	req.Header.Set("Authorization", "Bearer "+userJWT)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("profile: %d %s", w.Code, w.Body.String())
	}
	var profile map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &profile)
	if profile["email"] != "u2@example.com" || profile["sub_token"] != nil {
		t.Errorf("profile = %v", profile)
	}

	// GET /user/subscribe returns empty list (no nodes assigned) — should be 200 empty.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/user/subscribe", nil)
	req.Header.Set("Authorization", "Bearer "+userJWT)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("subscribe: %d", w.Code)
	}
	if strings.TrimSpace(w.Body.String()) != "" {
		t.Errorf("expected empty subscribe, got %q", w.Body.String())
	}

	// Bad password → 401.
	loginBody, _ = json.Marshal(map[string]any{"username": "u2", "password": "WRONG"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), req)
}

// TestUserSubscribeFormats exercises the authenticated /user/subscribe route
// with the same client-type selection as the public endpoint.
func TestUserSubscribeFormats(t *testing.T) {
	db := setupTestDB(t)
	r := newRouter(db)
	token := bootstrapAdminAndLogin(t, db, r)
	auth := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
	}

	// Reality node.
	priv, _, err := vgcrypto.GenerateX25519KeyPair()
	if err != nil {
		t.Fatalf("keypair: %v", err)
	}
	nodeBody, _ := json.Marshal(map[string]any{
		"name": "hk-2", "address": "hk2.example.com:443", "port": 443,
		"network": "tcp", "security": "reality",
		"reality_settings": map[string]any{
			"target":      "example.com:443",
			"server_name": "sni2.example.com",
			"private_key": priv,
			"short_ids":   []string{"fedcba9876543210"},
		},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/nodes", bytes.NewReader(nodeBody))
	auth(req)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", w.Code, w.Body.String())
	}
	var node map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &node)
	nodeID := node["id"].(string)

	// User with password.
	userBody, _ := json.Marshal(map[string]any{"email": "u3@example.com", "username": "u3"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(userBody))
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var user map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &user)
	userID := user["id"].(string)

	pwBody, _ := json.Marshal(map[string]any{"password": "secret-pass"})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/"+userID+"/password", bytes.NewReader(pwBody))
	auth(req)
	r.ServeHTTP(httptest.NewRecorder(), req)

	// Assign node.
	assignBody, _ := json.Marshal(map[string]any{"node_ids": []string{nodeID}})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/"+userID+"/nodes", bytes.NewReader(assignBody))
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}

	// Login.
	loginBody, _ := json.Marshal(map[string]any{"username": "u3", "password": "secret-pass"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/user/login", bytes.NewReader(loginBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	var loginResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &loginResp)
	userJWT := loginResp["token"].(string)

	// ?type=clash via the authenticated route → YAML.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/user/subscribe?type=clash", nil)
	req.Header.Set("Authorization", "Bearer "+userJWT)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("user subscribe clash: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "yaml") {
		t.Errorf("user subscribe clash content-type = %q", w.Header().Get("Content-Type"))
	}
	var clash struct {
		Proxies []struct {
			Name   string `yaml:"name"`
			Type   string `yaml:"type"`
			Server string `yaml:"server"`
		} `yaml:"proxies"`
	}
	if err := yaml.Unmarshal(w.Body.Bytes(), &clash); err != nil {
		t.Fatalf("unmarshal user clash yaml: %v\nbody: %s", err, w.Body.String())
	}
	if len(clash.Proxies) != 1 || clash.Proxies[0].Type != "vless" || clash.Proxies[0].Server != "hk2.example.com" {
		t.Errorf("user clash proxy = %+v", clash.Proxies)
	}
}

// TestSubscriptionV2Encryption verifies that a VLESS v2 (decryption) node
// emits the public key derived from the decryption private key in BOTH the
// `encryption` and `pbk` query params (not the literal "v2").
func TestSubscriptionV2Encryption(t *testing.T) {
	db := setupTestDB(t)
	r := newRouter(db)
	token := bootstrapAdminAndLogin(t, db, r)
	auth := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
	}

	// Generate the NFS X25519 private key and derive the expected public key.
	priv, _, err := vgcrypto.GenerateX25519KeyPair()
	if err != nil {
		t.Fatalf("generate keypair: %v", err)
	}
	expectedPBK, err := vgcrypto.DeriveX25519Public(priv)
	if err != nil {
		t.Fatalf("derive expected pbk: %v", err)
	}

	nodeBody, _ := json.Marshal(map[string]any{
		"name": "v2-node", "address": "v2.example.com:443", "port": 443,
		"network": "tcp", "security": "none",
		"vless": map[string]any{"decryption": priv},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/nodes", bytes.NewReader(nodeBody))
	auth(req)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create node: %d %s", w.Code, w.Body.String())
	}
	var node map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &node)
	nodeID := node["id"].(string)

	// Create a user.
	userBody, _ := json.Marshal(map[string]any{"email": "v2sub@example.com"})
	req = httptest.NewRequest(http.MethodPost, "/api/v1/admin/users", bytes.NewReader(userBody))
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user: %d %s", w.Code, w.Body.String())
	}
	var user map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &user)
	userID := user["id"].(string)
	subToken := user["sub_token"].(string)

	// Assign user -> node.
	assignBody, _ := json.Marshal(map[string]any{"node_ids": []string{nodeID}})
	req = httptest.NewRequest(http.MethodPut, "/api/v1/admin/users/"+userID+"/nodes", bytes.NewReader(assignBody))
	auth(req)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}

	// Fetch the subscription (plain) and inspect the link.
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sub/"+subToken, nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("sub: %d %s", w.Code, w.Body.String())
	}
	link := strings.TrimSpace(w.Body.String())
	u, err := url.Parse(link)
	if err != nil {
		t.Fatalf("parse link %q: %v", link, err)
	}
	q := u.Query()
	if got := q.Get("encryption"); got != expectedPBK {
		t.Errorf("encryption = %q, want derived pubkey %q", got, expectedPBK)
	}
	if got := q.Get("pbk"); got != expectedPBK {
		t.Errorf("pbk = %q, want derived pubkey %q", got, expectedPBK)
	}
	if q.Get("encryption") == "v2" {
		t.Errorf("encryption should not be the literal %q", "v2")
	}
}
