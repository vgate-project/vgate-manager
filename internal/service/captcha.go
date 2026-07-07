package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// turnstileSiteVerifyURL is Cloudflare's Turnstile verification endpoint.
const turnstileSiteVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// turnstileResponse is the subset of the siteverify JSON response we care about.
type turnstileResponse struct {
	Success     bool     `json:"success"`
	ChallengeTS string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
	ErrorCodes  []string `json:"error-codes"`
	Action      string   `json:"action"`
	CData       string   `json:"cdata"`
}

// CaptchaService verifies Cloudflare Turnstile challenge tokens. It is a thin,
// dependency-free wrapper around Turnstile's siteverify endpoint: verification
// is a single form-POST. When the feature is disabled in SystemConfig the
// Verify call is a no-op (returns nil), so existing deployments that never
// configure captcha are completely unchanged.
type CaptchaService struct {
	sysCfg *SystemConfigService
	httpc  *http.Client
}

func NewCaptchaService(sysCfg *SystemConfigService) *CaptchaService {
	return &CaptchaService{
		sysCfg: sysCfg,
		httpc:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Enabled reports whether Turnstile gating is turned on by an admin.
func (s *CaptchaService) Enabled() bool {
	v, err := s.sysCfg.Get(CfgKeyCaptchaTurnstileEnabled)
	if err != nil {
		return false
	}
	return v == "true"
}

// SiteKey returns the public Turnstile site key (for rendering the widget).
func (s *CaptchaService) SiteKey() string {
	v, err := s.sysCfg.Get(CfgKeyCaptchaTurnstileSiteKey)
	if err != nil {
		return ""
	}
	return v
}

// Verify checks a Turnstile response token. It is safe to call unconditionally:
// when the feature is disabled it returns nil immediately. When enabled it
// returns an error if the token is missing/empty, if Turnstile keys are not
// configured, or if Cloudflare reports the challenge as failed.
//
// remoteIP is optional metadata forwarded to Cloudflare verbatim (best-effort
// from the request's ClientIP); Turnstile uses it only as a signal.
func (s *CaptchaService) Verify(token, remoteIP string) error {
	if !s.Enabled() {
		return nil
	}
	if token == "" {
		return errors.New("captcha token required")
	}
	secret, err := s.sysCfg.Get(CfgKeyCaptchaTurnstileSecretKey)
	if err != nil || secret == "" {
		return errors.New("captcha not configured")
	}

	form := url.Values{}
	form.Set("secret", secret)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequest(http.MethodPost, turnstileSiteVerifyURL,
		// PostForm body encoding matches url.Values.String().
		io.NopCloser(strings.NewReader(form.Encode())))
	if err != nil {
		return fmt.Errorf("captcha: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := s.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("captcha: siteverify request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("captcha: read siteverify response: %w", err)
	}

	var tr turnstileResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return fmt.Errorf("captcha: decode siteverify response: %w", err)
	}
	if !tr.Success {
		return errors.New("captcha verification failed")
	}
	return nil
}
