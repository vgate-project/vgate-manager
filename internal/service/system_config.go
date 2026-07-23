package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/vgate-project/vgate-manager/config"
	"github.com/vgate-project/vgate-manager/internal/model"
)

// Manager runtime-config SystemConfig keys. These mirror the corresponding
// fields in config.yml and can be overridden at runtime via
// PUT /api/v1/admin/system-config. The JWT secret (jwt.secret) is intentionally
// excluded — it stays in config.yml / env per model.SystemConfig's convention.
const (
	CfgKeyJWTAccessTTLSecs       = "jwt.access_ttl_secs"
	CfgKeyJWTRefreshTTLSecs      = "jwt.refresh_ttl_secs"
	CfgKeyLogLevel               = "log.level"
	CfgKeyLogFormat              = "log.format"
	CfgKeyCORSAllowedOrigins     = "cors.allowed_origins" // JSON array string, e.g. ["*"]
	CfgKeyServerReadTimeoutSecs  = "server.read_timeout_secs"
	CfgKeyServerWriteTimeoutSecs = "server.write_timeout_secs"
	// CfgKeyQuotaResetDay is the global monthly quota reset day-of-month
	// (1-28). It applies to every user whose quota_reset_enabled flag is true;
	// the per-user reset day was removed in favor of this single global value.
	CfgKeyQuotaResetDay = "quota.reset_day"

	// CfgKeyPasswordMinLength is the minimum accepted password length enforced
	// on password set/change (admin + user). 0/empty falls back to the default.
	CfgKeyPasswordMinLength = "password.min_length"
	// CfgKeyPasswordRequireComplexity toggles the complexity rule
	// ("true"|"false"): when enabled a password must contain lowercase,
	// uppercase, and a digit.
	CfgKeyPasswordRequireComplexity = "password.require_complexity"

	// CfgKeyRegisterEnabled toggles public user registration ("true"|"false").
	CfgKeyRegisterEnabled = "user.register_enabled"
	// CfgKeyRegisterRequireInvite forces a valid invite code on registration
	// ("true"|"false"). Only consulted when registration is enabled.
	CfgKeyRegisterRequireInvite = "user.register_require_invite"
	// CfgKeyRegisterRequireEmailVerify holds new accounts in a disabled/pending
	// state until the user clicks the emailed verification link
	// ("true"|"false"). When enabled, registration does not auto-login.
	CfgKeyRegisterRequireEmailVerify = "user.register_require_email_verify"
	// CfgKeyRegisterEmailSuffixWhitelist restricts registration to email
	// addresses whose domain (the part after "@") is in this list. Stored as a
	// JSON array of strings (e.g. `["example.com","foo.com"]`). An empty/absent
	// value means no restriction (allow any domain). Matching is exact and
	// case-insensitive.
	CfgKeyRegisterEmailSuffixWhitelist = "user.register_email_suffix_whitelist"
	// CfgKeyInviteDefaultUserQuota is the per-user cap on successful
	// registrations a user may sponsor via self-generated invite codes. 0 disables
	// user-generated invites (admins can still mint codes directly).
	CfgKeyInviteDefaultUserQuota = "invite.default_user_quota"

	// CfgKeyTrialEnabled toggles the automatic new-user trial ("true"|"false").
	CfgKeyTrialEnabled = "user.trial_enabled"
	// CfgKeyTrialQuotaBytes is the free traffic (bytes) granted to each new
	// user on signup when the trial is enabled. 0 ⇒ no trial quota.
	CfgKeyTrialQuotaBytes = "user.trial_quota_bytes"
	// CfgKeyTrialDurationDays is the trial validity in days. 0 ⇒ no expiry
	// (the trial lasts until the quota is consumed).
	CfgKeyTrialDurationDays = "user.trial_duration_days"

	// CfgKeySiteBaseURL is the public base URL of the user-facing SPA, used to
	// build clickable links in emails (verification). Empty ⇒ emails fall back
	// to printing the raw token with manual instructions.
	CfgKeySiteBaseURL = "site.base_url"

	// CfgKeySiteName is the display name of the site, shown on the admin and
	// user portals (login page, sidebar brand, browser tab title). It is a
	// plain string with no parsing; an empty/absent value falls back to
	// "VGate" on read (see GetSiteName).
	CfgKeySiteName = "site.name"

	// Email (SMTP) config keys. Values are stored as SystemConfig rows so an
	// admin can configure mail delivery at runtime without editing config.yml.
	// email.smtp_pass is the only secret here and is stored in plaintext at the
	// same trust level as alipay.private_key (self-hosted, single-tenant).
	CfgKeyEmailEnabled  = "email.enabled"   // "true" | "false" — gate outbound mail
	CfgKeyEmailSMTPHost = "email.smtp_host" // e.g. smtp.example.com
	CfgKeyEmailSMTPPort = "email.smtp_port" // e.g. 587 (starttls) / 465 (ssl) / 25 (none)
	CfgKeyEmailSMTPUser = "email.smtp_user" // auth user (empty ⇒ no auth)
	CfgKeyEmailSMTPPass = "email.smtp_pass" // auth password

	CfgKeyEmailFrom         = "email.from"          // shared From: address used by both SMTP and Resend
	CfgKeyEmailFromName     = "email.from_name"     // optional display name for the From address
	CfgKeyEmailSMTPSecurity = "email.smtp_security" // "none" | "starttls" | "ssl" (default "starttls")

	// CfgKeyEmailProvider selects the mail backend: "smtp" (default) or
	// "resend". Resend delivers via the Resend API and shares the same From
	// address (email.from) as SMTP — a domain verified at resend.com.
	CfgKeyEmailProvider     = "email.provider"       // "smtp" | "resend" (default "smtp")
	CfgKeyEmailResendAPIKey = "email.resend_api_key" // Resend dashboard API key (secret)

	// Captcha (Cloudflare Turnstile) config keys. The feature is opt-in: when
	// captcha.turnstile_enabled is "false" (the default) no challenge is
	// required anywhere. An admin enables it at runtime and pastes the site +
	// secret keys from their Turnstile widget; no restart is needed.
	CfgKeyCaptchaTurnstileEnabled   = "captcha.turnstile_enabled"    // "true" | "false"
	CfgKeyCaptchaTurnstileSiteKey   = "captcha.turnstile_site_key"   // public widget key
	CfgKeyCaptchaTurnstileSecretKey = "captcha.turnstile_secret_key" // server-side secret

	// CfgKeySubBaseURLs holds the list of subscription base URLs (bare
	// origins, no path, e.g. "https://sub.example.com"). When non-empty the
	// user subscription link is built from a random entry; when empty the
	// request origin is used as the fallback. Stored as a JSON array string.
	CfgKeySubBaseURLs = "sub.base_urls"

	// CfgKeyPaymentProductName is the optional fallback template for the
	// product name pushed to the payment gateway. It supports the placeholders
	// {plan} (plan/package name), {period} (billing period, empty for
	// traffic/reset) and {amount} (order amount in yuan). An empty/absent value
	// falls back to the built-in default subject. A plan or traffic package
	// with its own DisplayName takes precedence over this template.
	CfgKeyPaymentProductName = "payment.product_name_template"

	// Telegram bot config keys. All settings are stored as SystemConfig rows so
	// an admin can configure the bot at runtime without restarting. The bot
	// token is the only secret here and is stored in plaintext at the same
	// trust level as alipay.private_key / email.smtp_pass (self-hosted,
	// single-tenant).
	CfgKeyTelegramEnabled        = "telegram.enabled"          // "true" | "false" — master switch
	CfgKeyTelegramBotToken       = "telegram.bot_token"        // BotFather token (secret)
	CfgKeyTelegramBotUsername    = "telegram.bot_username"     // bot @username, used for deep links
	CfgKeyTelegramUserBotEnabled = "telegram.user_bot_enabled" // "true" | "false" — user self-service

	// Per-event alert toggles. Each admin alert is independently enableable so
	// an operator can opt into exactly the notifications they want.
	CfgKeyAlertNewRegistration = "telegram.alert_new_registration"
	CfgKeyAlertOrderPaid       = "telegram.alert_order_paid"
	CfgKeyAlertNodeUp          = "telegram.alert_node_up"
	CfgKeyAlertNodeDown        = "telegram.alert_node_down"
	CfgKeyAlertTrafficExceeded = "telegram.alert_traffic_exceeded"
	CfgKeyAlertAnnouncement    = "telegram.alert_announcement"
	CfgKeyAlertTicket          = "telegram.alert_ticket"

	// Traffic reminder config keys. These are global rules (set by an admin)
	// for notifying users when they approach their quota — either by usage
	// percentage or by how few days remain before the monthly reset. A
	// per-user cooldown bounds the send frequency. Users pick the channel
	// (email / Telegram) themselves on their profile.
	CfgKeyReminderEnabled  = "reminder.enabled"        // "true" | "false" — master switch
	CfgKeyReminderPct      = "reminder.pct_threshold"  // "80" — send when usage % of quota_bytes reaches this
	CfgKeyReminderDays     = "reminder.days_threshold" // "3" — send when <= N days remain until reset (reset-enabled users)
	CfgKeyReminderCooldown = "reminder.cooldown_days"  // "1" — min days between reminders per user
)

type SystemConfigService struct {
	db *gorm.DB

	// cache holds the full key/value set in process memory so that hot paths
	// (CORS middleware, token issuance, order payment) never read the database
	// on every request. It is populated lazily on first read and invalidated
	// (rewarmed) on every write, so PUT /admin/system-config stays hot-applied.
	mu    sync.RWMutex
	cache map[string]string
	ready bool
}

func NewSystemConfigService(db *gorm.DB) *SystemConfigService {
	s := &SystemConfigService{db: db}
	// Best-effort warm so the first request is served from cache too.
	if err := s.refreshCache(); err != nil {
		log.Warnf("system-config: failed to warm cache on init: %v", err)
	}
	return s
}

// refreshCache loads the full key/value set from the database into the in-memory
// cache. Callers must NOT hold s.mu.
func (s *SystemConfigService) refreshCache() error {
	var cfgs []model.SystemConfig
	if err := s.db.Find(&cfgs).Error; err != nil {
		return err
	}
	m := make(map[string]string, len(cfgs))
	for _, c := range cfgs {
		m[c.Key] = c.Value
	}
	s.mu.Lock()
	s.cache = m
	s.ready = true
	s.mu.Unlock()
	return nil
}

// GetAll returns all key/value runtime settings, served from the in-memory cache.
// On a cold cache it warms once from the database, then every subsequent read is
// lock-only (no DB hit).
func (s *SystemConfigService) GetAll() (map[string]string, error) {
	s.mu.RLock()
	if s.ready {
		cp := make(map[string]string, len(s.cache))
		for k, v := range s.cache {
			cp[k] = v
		}
		s.mu.RUnlock()
		return cp, nil
	}
	s.mu.RUnlock()

	if err := s.refreshCache(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	cp := make(map[string]string, len(s.cache))
	for k, v := range s.cache {
		cp[k] = v
	}
	s.mu.RUnlock()
	return cp, nil
}

// AlipayConfig holds the alipay credentials/endpoints. Values are stored as
// SystemConfig rows (key "alipay.*") so they can be edited from the admin
// backend at runtime without restarting the server.
type AlipayConfig struct {
	AppID      string
	PrivateKey string
	PublicKey  string
	NotifyURL  string
	ReturnURL  string
	Sandbox    bool
}

// Alipay config SystemConfig keys.
const (
	AlipayKeyAppID      = "alipay.app_id"
	AlipayKeyPrivateKey = "alipay.private_key"
	AlipayKeyPublicKey  = "alipay.public_key"
	AlipayKeyNotifyURL  = "alipay.notify_url"
	AlipayKeyReturnURL  = "alipay.return_url"
	AlipayKeySandbox    = "alipay.sandbox" // "true" | "false"
)

// GetAlipayConfig reads the alipay settings from SystemConfig. An empty AppID
// means alipay has not been configured yet.
func (s *SystemConfigService) GetAlipayConfig() (AlipayConfig, error) {
	m, err := s.GetAll()
	if err != nil {
		return AlipayConfig{}, err
	}
	return AlipayConfig{
		AppID:      m[AlipayKeyAppID],
		PrivateKey: m[AlipayKeyPrivateKey],
		PublicKey:  m[AlipayKeyPublicKey],
		NotifyURL:  m[AlipayKeyNotifyURL],
		ReturnURL:  m[AlipayKeyReturnURL],
		Sandbox:    m[AlipayKeySandbox] == "true",
	}, nil
}

// PasswordPolicy describes the strength rules enforced when a password is set
// or changed (admin and user self-service). Values are sourced from
// SystemConfig so an admin can tune them at runtime without a restart.
type PasswordPolicy struct {
	MinLength         int
	RequireComplexity bool
}

// DefaultPasswordPolicy returns the baseline used when SystemConfig is
// unavailable (e.g. CLI bootstrap) or a key is missing/invalid.
func DefaultPasswordPolicy() PasswordPolicy {
	return PasswordPolicy{MinLength: 8, RequireComplexity: false}
}

// GetPasswordPolicy reads the password policy from SystemConfig, falling back
// to DefaultPasswordPolicy on any read/parse failure.
func (s *SystemConfigService) GetPasswordPolicy() PasswordPolicy {
	def := DefaultPasswordPolicy()
	m, err := s.GetAll()
	if err != nil {
		return def
	}
	if v, ok := m[CfgKeyPasswordMinLength]; ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			def.MinLength = n
		}
	}
	if v, ok := m[CfgKeyPasswordRequireComplexity]; ok {
		def.RequireComplexity = v == "true"
	}
	return def
}

// SetAll upserts all key/value pairs in a single transaction, then rewarms the
// in-memory cache so the next read sees the new values (hot-apply).
func (s *SystemConfigService) SetAll(values map[string]string) error {
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		for k, v := range values {
			if err := tx.Save(&model.SystemConfig{Key: k, Value: v}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return s.refreshCache()
}

// Get returns a single SystemConfig value by key, served from the in-memory cache.
// A cold cache is warmed from the database on first access.
func (s *SystemConfigService) Get(key string) (string, error) {
	s.mu.RLock()
	if s.ready {
		v, ok := s.cache[key]
		s.mu.RUnlock()
		if !ok {
			return "", gorm.ErrRecordNotFound
		}
		return v, nil
	}
	s.mu.RUnlock()

	if err := s.refreshCache(); err != nil {
		return "", err
	}
	s.mu.RLock()
	v, ok := s.cache[key]
	s.mu.RUnlock()
	if !ok {
		return "", gorm.ErrRecordNotFound
	}
	return v, nil
}

func (s *SystemConfigService) IsRegisterEnabled() bool {
	v, err := s.Get(CfgKeyRegisterEnabled)
	if err != nil {
		return false
	}
	return v == "true"
}

// IsRegisterRequireInvite reports whether a valid invite code is mandatory to
// register. Only meaningful when IsRegisterEnabled is also true.
func (s *SystemConfigService) IsRegisterRequireInvite() bool {
	v, err := s.Get(CfgKeyRegisterRequireInvite)
	if err != nil {
		return false
	}
	return v == "true"
}

// IsRegisterRequireEmailVerify reports whether new accounts are held pending
// email verification instead of being activated immediately.
func (s *SystemConfigService) IsRegisterRequireEmailVerify() bool {
	v, err := s.Get(CfgKeyRegisterRequireEmailVerify)
	if err != nil {
		return false
	}
	return v == "true"
}

// IsCaptchaEnabled reports whether Cloudflare Turnstile gating is turned on.
func (s *SystemConfigService) IsCaptchaEnabled() bool {
	v, err := s.Get(CfgKeyCaptchaTurnstileEnabled)
	if err != nil {
		return false
	}
	return v == "true"
}

// CaptchaSiteKey returns the public Turnstile site key for rendering the widget.
func (s *SystemConfigService) CaptchaSiteKey() string {
	v, err := s.Get(CfgKeyCaptchaTurnstileSiteKey)
	if err != nil {
		return ""
	}
	return v
}

// GetInviteDefaultUserQuota returns the per-user invite cap (default 0).
func (s *SystemConfigService) GetInviteDefaultUserQuota() int {
	v, err := s.Get(CfgKeyInviteDefaultUserQuota)
	if err != nil {
		return 0
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 0 {
		return 0
	}
	return n
}

// IsTrialEnabled reports whether new users receive an automatic trial grant.
func (s *SystemConfigService) IsTrialEnabled() bool {
	v, err := s.Get(CfgKeyTrialEnabled)
	if err != nil || v == "" {
		return false
	}
	return v == "true"
}

// GetTrialQuotaBytes returns the free traffic (bytes) granted to each new user
// on signup when the trial is enabled. A non-positive value disables the grant.
func (s *SystemConfigService) GetTrialQuotaBytes() int64 {
	v, err := s.Get(CfgKeyTrialQuotaBytes)
	if err != nil {
		return 0
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < 0 {
		return 0
	}
	return n
}

// GetTrialDurationDays returns the trial validity window in days. 0 means no
// expiry (the trial lasts until the quota is consumed).
func (s *SystemConfigService) GetTrialDurationDays() int {
	v, err := s.Get(CfgKeyTrialDurationDays)
	if err != nil {
		return 0
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < 0 {
		return 0
	}
	return n
}

// GetSiteBaseURL returns the public base URL of the user SPA used to build
// emailed links (may be empty).
func (s *SystemConfigService) GetSiteBaseURL() string {
	v, err := s.Get(CfgKeySiteBaseURL)
	if err != nil {
		return ""
	}
	return v
}

// GetSiteName returns the configured site display name, falling back to
// "VGate" when the key is absent (e.g. before the first seed) or empty.
func (s *SystemConfigService) GetSiteName() string {
	v, err := s.Get(CfgKeySiteName)
	if err != nil || v == "" {
		return "VGate"
	}
	return v
}

// GetSubBaseURLs returns the configured subscription base URLs (bare origins,
// no path). Each entry is validated to be an absolute http/https URL and is
// trimmed of any trailing slash; invalid entries are filtered out so callers
// never receive a malformed URL. A stored value that is not valid JSON returns
// an error (so a broken admin edit is surfaced rather than silently ignored).
func (s *SystemConfigService) GetSubBaseURLs() ([]string, error) {
	v, err := s.Get(CfgKeySubBaseURLs)
	if err != nil {
		// Absent key ⇒ empty list (fall back to request origin elsewhere).
		return nil, nil
	}
	var raw []string
	if err := json.Unmarshal([]byte(v), &raw); err != nil {
		return nil, fmt.Errorf("invalid %s: not a JSON array: %w", CfgKeySubBaseURLs, err)
	}
	out := make([]string, 0, len(raw))
	for _, u := range raw {
		trimmed := strings.TrimRight(strings.TrimSpace(u), "/")
		if trimmed == "" {
			continue
		}
		parsed, err := url.ParseRequestURI(trimmed)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			log.Warnf("system-config: skipping invalid %s entry %q", CfgKeySubBaseURLs, u)
			continue
		}
		out = append(out, trimmed)
	}
	return out, nil
}

// GetRegisterEmailSuffixWhitelist returns the configured list of allowed email
// domains for registration. Each entry is lowercased and trimmed; empty entries
// are filtered out so callers never receive a blank domain. A stored value that
// is not valid JSON returns an error (so a broken admin edit is surfaced rather
// than silently ignored). An absent key (or an empty list) means no restriction
// — any email domain is allowed.
func (s *SystemConfigService) GetRegisterEmailSuffixWhitelist() ([]string, error) {
	v, err := s.Get(CfgKeyRegisterEmailSuffixWhitelist)
	if err != nil {
		// Absent key ⇒ no restriction (allow all domains).
		return nil, nil
	}
	var raw []string
	if err := json.Unmarshal([]byte(v), &raw); err != nil {
		return nil, fmt.Errorf("invalid %s: not a JSON array: %w", CfgKeyRegisterEmailSuffixWhitelist, err)
	}
	out := make([]string, 0, len(raw))
	for _, d := range raw {
		d = strings.ToLower(strings.TrimSpace(d))
		if d == "" {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

// TelegramConfig is the resolved Telegram bot configuration sourced from
// SystemConfig. The BotToken is the only secret and is stored in plaintext at
// the same trust level as alipay.private_key / email.smtp_pass.
type TelegramConfig struct {
	Enabled           bool
	BotToken          string
	BotUsername       string
	UserBotEnabled    bool
	AlertNewReg       bool
	AlertOrderPaid    bool
	AlertNodeUp       bool
	AlertNodeDown     bool
	AlertTraffic      bool
	AlertAnnouncement bool
	AlertTicket       bool
}

// GetTelegramConfig reads the Telegram bot settings from SystemConfig. Missing
// keys fall back to the disabled/default values, so the bot stays off until an
// admin explicitly enables it and supplies a token.
func (s *SystemConfigService) GetTelegramConfig() (TelegramConfig, error) {
	m, err := s.GetAll()
	if err != nil {
		return TelegramConfig{}, err
	}
	cfg := TelegramConfig{
		Enabled:           m[CfgKeyTelegramEnabled] == "true",
		BotToken:          m[CfgKeyTelegramBotToken],
		BotUsername:       m[CfgKeyTelegramBotUsername],
		UserBotEnabled:    m[CfgKeyTelegramUserBotEnabled] == "true",
		AlertNewReg:       m[CfgKeyAlertNewRegistration] == "true",
		AlertOrderPaid:    m[CfgKeyAlertOrderPaid] == "true",
		AlertNodeUp:       m[CfgKeyAlertNodeUp] == "true",
		AlertNodeDown:     m[CfgKeyAlertNodeDown] == "true",
		AlertTraffic:      m[CfgKeyAlertTrafficExceeded] == "true",
		AlertAnnouncement: m[CfgKeyAlertAnnouncement] == "true",
		AlertTicket:       m[CfgKeyAlertTicket] == "true",
	}
	return cfg, nil
}

// ReminderConfig is the resolved global traffic-reminder configuration sourced
// from SystemConfig. Admins control the thresholds and cooldown; users pick
// the channel on their profile.
type ReminderConfig struct {
	Enabled       bool // master switch (reminder.enabled)
	PctThreshold  int  // send when usage reaches this % of quota_bytes (1-100)
	DaysThreshold int  // send when <= this many days remain until reset (>=0)
	CooldownDays  int  // min days between reminders for a single user (>=1)
}

// GetReminderConfig reads the traffic-reminder settings from SystemConfig,
// falling back to safe defaults when keys are missing/invalid (disabled, 80%,
// 3 days, 1-day cooldown). A disabled or malformed config simply means no
// reminders are sent, so failures degrade safe.
func (s *SystemConfigService) GetReminderConfig() (ReminderConfig, error) {
	m, err := s.GetAll()
	if err != nil {
		return ReminderConfig{}, err
	}
	cfg := ReminderConfig{
		Enabled:       m[CfgKeyReminderEnabled] == "true",
		PctThreshold:  80,
		DaysThreshold: 3,
		CooldownDays:  1,
	}
	if v, ok := m[CfgKeyReminderPct]; ok {
		if n, e := strconv.Atoi(v); e == nil {
			cfg.PctThreshold = clampInt(n, 1, 100)
		}
	}
	if v, ok := m[CfgKeyReminderDays]; ok {
		if n, e := strconv.Atoi(v); e == nil && n >= 0 {
			cfg.DaysThreshold = n
		}
	}
	if v, ok := m[CfgKeyReminderCooldown]; ok {
		if n, e := strconv.Atoi(v); e == nil && n >= 1 {
			cfg.CooldownDays = n
		}
	}
	return cfg, nil
}

// clampInt bounds n to the inclusive range [lo, hi].
func clampInt(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

// defaultConfigRows returns the migrated (DB-backed) runtime-config keys and
// their hardcoded default values, sourced from config.DefaultConfig(). Only
// these hot-reloadable keys are eligible to be written into the database;
// server.port, db.*, jwt.secret and admin.bootstrap.* are deliberately excluded
// (server.port/db are startup/infra config sourced from config.yml — db must
// exist before it can be read, and port requires a listener rebind i.e. a
// restart, so it is not hot-reloadable).
func (s *SystemConfigService) defaultConfigRows() map[string]string {
	d := config.DefaultConfig()
	cors, err := json.Marshal(d.CORS.AllowedOrigins)
	if err != nil {
		cors = []byte(`["*"]`)
	}
	return map[string]string{
		CfgKeyJWTAccessTTLSecs:             strconv.Itoa(d.JWT.AccessTTLSecs),
		CfgKeyJWTRefreshTTLSecs:            strconv.Itoa(d.JWT.RefreshTTLSecs),
		CfgKeyLogLevel:                     d.Log.Level,
		CfgKeyLogFormat:                    d.Log.Format,
		CfgKeyCORSAllowedOrigins:           string(cors),
		CfgKeyServerReadTimeoutSecs:        strconv.Itoa(d.Server.ReadTimeoutSecs),
		CfgKeyServerWriteTimeoutSecs:       strconv.Itoa(d.Server.WriteTimeoutSecs),
		CfgKeyQuotaResetDay:                "1",
		CfgKeyPasswordMinLength:            "8",
		CfgKeyPasswordRequireComplexity:    "false",
		CfgKeyRegisterEnabled:              "false",
		CfgKeyRegisterRequireInvite:        "false",
		CfgKeyRegisterRequireEmailVerify:   "false",
		CfgKeyRegisterEmailSuffixWhitelist: "[]",
		CfgKeyInviteDefaultUserQuota:       "5",
		CfgKeyTrialEnabled:                 "false",
		CfgKeyTrialQuotaBytes:              "1073741824", // 1 GiB
		CfgKeyTrialDurationDays:            "7",
		CfgKeySiteBaseURL:                  "",
		CfgKeySiteName:                     "VGate",
		CfgKeyEmailEnabled:                 "false",
		CfgKeyEmailSMTPHost:                "",
		CfgKeyEmailSMTPPort:                "587",
		CfgKeyEmailSMTPUser:                "",
		CfgKeyEmailSMTPPass:                "",
		CfgKeyEmailFrom:                    "",
		CfgKeyEmailFromName:                "",
		CfgKeyEmailSMTPSecurity:            "starttls",
		CfgKeyEmailProvider:                "smtp",
		CfgKeyEmailResendAPIKey:            "",
		CfgKeyCaptchaTurnstileEnabled:      "false",
		CfgKeyCaptchaTurnstileSiteKey:      "",
		CfgKeyCaptchaTurnstileSecretKey:    "",
		CfgKeySubBaseURLs:                  "[]",
		CfgKeyPaymentProductName:           "",
		CfgKeyTelegramEnabled:              "false",
		CfgKeyTelegramBotToken:             "",
		CfgKeyTelegramBotUsername:          "",
		CfgKeyTelegramUserBotEnabled:       "false",
		CfgKeyAlertNewRegistration:         "false",
		CfgKeyAlertOrderPaid:               "false",
		CfgKeyAlertNodeUp:                  "false",
		CfgKeyAlertNodeDown:                "false",
		CfgKeyAlertTrafficExceeded:         "false",
		CfgKeyAlertAnnouncement:            "false",
		CfgKeyAlertTicket:                  "false",
		CfgKeyReminderEnabled:              "false",
		CfgKeyReminderPct:                  "80",
		CfgKeyReminderDays:                 "3",
		CfgKeyReminderCooldown:             "1",
	}
}

// persistDefault inserts a default config row, but only if the key does not
// already exist (idempotent upsert — never overwrites an admin's edit). It
// rewarms the cache so the seeded value is visible to subsequent reads.
func (s *SystemConfigService) persistDefault(key, value string) error {
	err := s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "key"}},
		DoNothing: true,
	}).Create(&model.SystemConfig{Key: key, Value: value}).Error
	if err != nil {
		return err
	}
	return s.refreshCache()
}

// ApplyOverrides returns a Config where the migrated (DB-backed) sections come
// SOLELY from the database — config.yml file values for those sections are
// intentionally ignored. db.*, jwt.secret and admin.bootstrap.* are kept from
// base (they must come from config.yml / env). A DB read error is returned to
// the caller so it can fall back to the un-overridden config entirely;
// individual parse failures only warn and keep the hardcoded default.
func (s *SystemConfigService) ApplyOverrides(base *config.Config) (*config.Config, error) {
	m, err := s.GetAll()
	if err != nil {
		return nil, err
	}

	// Start from base. server.port stays sourced from config.yml (startup/infra
	// config requiring a listener rebind), so it is NOT reset here. The other
	// migrated sections are reset to hardcoded defaults so config.yml values
	// for them are never used as a fallback.
	out := *base
	def := config.DefaultConfig()
	out.JWT.AccessTTLSecs = def.JWT.AccessTTLSecs
	out.JWT.RefreshTTLSecs = def.JWT.RefreshTTLSecs
	out.Log = def.Log
	out.CORS = def.CORS
	out.Server.ReadTimeoutSecs = def.Server.ReadTimeoutSecs
	out.Server.WriteTimeoutSecs = def.Server.WriteTimeoutSecs

	// Write-on-miss: any migrated key absent from the DB gets its DefaultConfig
	// value written back, so the database is always the authoritative, complete
	// source (GET shows defaults; a runtime-deleted row is restored next boot).
	for k, v := range s.defaultConfigRows() {
		if _, ok := m[k]; ok {
			continue
		}
		if err := s.persistDefault(k, v); err != nil {
			log.Warnf("system-config: failed to seed default %s: %v", k, err)
		}
	}

	if v, ok := m[CfgKeyJWTAccessTTLSecs]; ok {
		if n, e := strconv.Atoi(v); e != nil {
			log.Warnf("system-config: invalid %s=%q, keep default %d: %v", CfgKeyJWTAccessTTLSecs, v, def.JWT.AccessTTLSecs, e)
		} else {
			out.JWT.AccessTTLSecs = n
		}
	}
	if v, ok := m[CfgKeyJWTRefreshTTLSecs]; ok {
		if n, e := strconv.Atoi(v); e != nil {
			log.Warnf("system-config: invalid %s=%q, keep default %d: %v", CfgKeyJWTRefreshTTLSecs, v, def.JWT.RefreshTTLSecs, e)
		} else {
			out.JWT.RefreshTTLSecs = n
		}
	}
	if v, ok := m[CfgKeyLogLevel]; ok {
		out.Log.Level = v
	}
	if v, ok := m[CfgKeyLogFormat]; ok {
		out.Log.Format = v
	}
	if v, ok := m[CfgKeyCORSAllowedOrigins]; ok {
		var origins []string
		if e := json.Unmarshal([]byte(v), &origins); e != nil {
			log.Warnf("system-config: invalid %s=%q, keep default: %v", CfgKeyCORSAllowedOrigins, v, e)
		} else {
			out.CORS.AllowedOrigins = origins
		}
	}
	if v, ok := m[CfgKeyServerReadTimeoutSecs]; ok {
		if n, e := strconv.Atoi(v); e != nil {
			log.Warnf("system-config: invalid %s=%q, keep default %d: %v", CfgKeyServerReadTimeoutSecs, v, def.Server.ReadTimeoutSecs, e)
		} else {
			out.Server.ReadTimeoutSecs = n
		}
	}
	if v, ok := m[CfgKeyServerWriteTimeoutSecs]; ok {
		if n, e := strconv.Atoi(v); e != nil {
			log.Warnf("system-config: invalid %s=%q, keep default %d: %v", CfgKeyServerWriteTimeoutSecs, v, def.Server.WriteTimeoutSecs, e)
		} else {
			out.Server.WriteTimeoutSecs = n
		}
	}

	return &out, nil
}
