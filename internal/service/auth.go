package service

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

// Claims is the JWT claim set shared by admin and user tokens. The Type field
// distinguishes "admin" from "user" so one middleware can guard both.
type Claims struct {
	Type     string `json:"type"` // "admin" | "user"
	AdminID  uint   `json:"admin_id,omitempty"`
	Username string `json:"username,omitempty"`
	Role     string `json:"role,omitempty"` // super_admin | admin
	UserID   string `json:"user_id,omitempty"`
	Level    int    `json:"level,omitempty"` // user's access tier, carried for identity/display
	jwt.RegisteredClaims
}

type AuthService struct {
	db     *gorm.DB
	secret string
	// accessTTL/refreshTTL are the config.yml defaults, used when no
	// SystemConfigService is wired (e.g. the `admin create` CLI) or when the
	// DB read fails. Runtime overrides come from the system_configs table.
	accessTTL   time.Duration
	refreshTTL  time.Duration
	sysCfg      *SystemConfigService
	inviteSvc   *InviteService
	emailSvc    *EmailService
	telegramSvc *TelegramService
}

func NewAuthService(db *gorm.DB, secret string, accessTTL, refreshTTL time.Duration) *AuthService {
	return &AuthService{db: db, secret: secret, accessTTL: accessTTL, refreshTTL: refreshTTL}
}

// SetConfigService injects the SystemConfigService so JWT TTLs can be read from
// the database at token-issue time (allowing admin edits without restart).
func (a *AuthService) SetConfigService(svc *SystemConfigService) {
	a.sysCfg = svc
}

// SetInviteService wires the invite service so registration can validate and
// consume invite codes.
func (a *AuthService) SetInviteService(svc *InviteService) {
	a.inviteSvc = svc
}

// SetEmailService wires the email service so registration can send the
// verification mail when email verification is required.
func (a *AuthService) SetEmailService(svc *EmailService) {
	a.emailSvc = svc
}

// SetTelegramService wires the Telegram bot service so registration can emit
// an admin alert (when the admin enabled the new_registration alert).
func (a *AuthService) SetTelegramService(svc *TelegramService) {
	a.telegramSvc = svc
}

// ttl returns the effective access/refresh TTLs, preferring DB overrides and
// falling back to the config.yml defaults on any error.
func (a *AuthService) ttl() (access, refresh time.Duration) {
	access, refresh = a.accessTTL, a.refreshTTL
	if a.sysCfg == nil {
		return
	}
	m, err := a.sysCfg.GetAll()
	if err != nil {
		return
	}
	if v, ok := m[CfgKeyJWTAccessTTLSecs]; ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			access = time.Duration(n) * time.Second
		}
	}
	if v, ok := m[CfgKeyJWTRefreshTTLSecs]; ok {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			refresh = time.Duration(n) * time.Second
		}
	}
	return
}

// HashPassword returns a bcrypt hash of the plaintext password.
func (a *AuthService) HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// BootstrapAdmin creates a super_admin account iff no admins exist yet. Called
// on startup, and idempotent. If password is empty, a random password is
// generated and returned so the caller can surface it to the operator (the
// plaintext is never stored — only its bcrypt hash). Returns the password
// actually used (empty when an admin already existed).
func (a *AuthService) BootstrapAdmin(username, password string) (string, error) {
	var count int64
	if err := a.db.Model(&model.Admin{}).Count(&count).Error; err != nil {
		return "", fmt.Errorf("count admins: %w", err)
	}
	if count > 0 {
		return "", nil
	}
	if password == "" {
		password = util.RandomToken(12)
	}
	hash, err := a.HashPassword(password)
	if err != nil {
		return "", err
	}
	if err := a.db.Create(&model.Admin{
		Username:     username,
		PasswordHash: hash,
		Role:         "super_admin",
	}).Error; err != nil {
		return "", err
	}
	return password, nil
}

// CreateAdmin creates a new admin account (for the CLI / super_admin API).
func (a *AuthService) CreateAdmin(username, password, role string) (*model.Admin, error) {
	if role == "" {
		role = "admin"
	}
	if !validAdminRole(role) {
		return nil, errors.New("invalid role")
	}
	hash, err := a.HashPassword(password)
	if err != nil {
		return nil, err
	}
	admin := &model.Admin{Username: username, PasswordHash: hash, Role: role}
	if err := a.db.Create(admin).Error; err != nil {
		return nil, err
	}
	return admin, nil
}

// ListAdmins returns admin accounts, paginated.
func (a *AuthService) ListAdmins(page, pageSize int) ([]model.Admin, int64, error) {
	var admins []model.Admin
	var total int64
	a.db.Model(&model.Admin{}).Count(&total)
	err := a.db.Order("created_at ASC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&admins).Error
	return admins, total, err
}

// UpdateAdminPassword sets a new password for an admin.
func (a *AuthService) UpdateAdminPassword(id uint, password string) error {
	hash, err := a.HashPassword(password)
	if err != nil {
		return err
	}
	res := a.db.Model(&model.Admin{}).Where("id = ?", id).Update("password_hash", hash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// validAdminRole reports whether role is one of the known admin roles.
func validAdminRole(role string) bool {
	return role == "super_admin" || role == "admin"
}

// GetAdmin returns a single admin by id.
func (a *AuthService) GetAdmin(id uint) (*model.Admin, error) {
	var admin model.Admin
	if err := a.db.First(&admin, id).Error; err != nil {
		return nil, err
	}
	return &admin, nil
}

// GetAdminByTelegramID returns the admin linked to a Telegram chat ID, or
// nil when none is linked.
func (a *AuthService) GetAdminByTelegramID(telegramID int64) (*model.Admin, error) {
	if telegramID == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	var admin model.Admin
	if err := a.db.Where("telegram_id = ?", telegramID).First(&admin).Error; err != nil {
		return nil, err
	}
	return &admin, nil
}

// SetAdminTelegram links (or unlinks, with chatID 0) a Telegram chat to an
// admin account. Used by the admin Telegram bind flow. A chat may only be
// linked to one admin, so before linking we clear any other admin that already
// holds the same chat id; this prevents two admins from answering tickets under
// the same Telegram identity. Unlinking (chatID 0) only touches the target row.
func (a *AuthService) SetAdminTelegram(adminID uint, chatID int64) error {
	if chatID != 0 {
		// Clear the chat from any other admin that may still hold it.
		if res := a.db.Model(&model.Admin{}).
			Where("telegram_id = ? AND id <> ?", chatID, adminID).
			Updates(map[string]any{
				"telegram_id":              0,
				"telegram_bind_token":      "",
				"telegram_bind_expires_at": nil,
			}); res.Error != nil {
			return res.Error
		}
	}
	res := a.db.Model(&model.Admin{}).
		Where("id = ?", adminID).
		Updates(map[string]any{
			"telegram_id":              chatID,
			"telegram_bind_token":      "",
			"telegram_bind_expires_at": nil,
		})
	return res.Error
}

// UpdateAdmin changes an admin's username and/or role. An empty field is left
// unchanged. Role, when set, must be a known value.
func (a *AuthService) UpdateAdmin(id uint, username, role string) (*model.Admin, error) {
	var admin model.Admin
	if err := a.db.First(&admin, id).Error; err != nil {
		return nil, err
	}
	if role != "" {
		if !validAdminRole(role) {
			return nil, errors.New("invalid role")
		}
		// Demoting the last remaining super_admin would leave zero super admins
		// and lock out management; refuse it (mirrors DeleteAdmin's guard).
		if role != "super_admin" && admin.Role == "super_admin" {
			var n int64
			a.db.Model(&model.Admin{}).Where("role = ?", "super_admin").Count(&n)
			if n <= 1 {
				return nil, errors.New("cannot demote the last super admin")
			}
		}
		admin.Role = role
	}
	if username != "" {
		admin.Username = username
	}
	if err := a.db.Save(&admin).Error; err != nil {
		return nil, err
	}
	return &admin, nil
}

// DeleteAdmin removes an admin. The caller may not delete their own account,
// and the last remaining super_admin may not be deleted (to avoid locking out
// management). currentAdminID is the authenticated caller's id.
func (a *AuthService) DeleteAdmin(id, currentAdminID uint) error {
	if id == currentAdminID {
		return errors.New("cannot delete your own account")
	}
	var admin model.Admin
	if err := a.db.First(&admin, id).Error; err != nil {
		return err
	}
	if admin.Role == "super_admin" {
		var n int64
		a.db.Model(&model.Admin{}).Where("role = ?", "super_admin").Count(&n)
		if n <= 1 {
			return errors.New("cannot delete the last super admin")
		}
	}
	return a.db.Delete(&admin).Error
}

// ChangeOwnAdminPassword lets an admin rotate their own password. It verifies
// the current password before hashing the new one, then revokes all of the
// admin's outstanding refresh tokens so any other active sessions are forced to
// re-authenticate.
func (a *AuthService) ChangeOwnAdminPassword(adminID uint, oldPwd, newPwd string) error {
	var admin model.Admin
	if err := a.db.First(&admin, adminID).Error; err != nil {
		return err
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(oldPwd)); err != nil {
		return errors.New("invalid current password")
	}
	policy := DefaultPasswordPolicy()
	if a.sysCfg != nil {
		policy = a.sysCfg.GetPasswordPolicy()
	}
	if err := policy.Validate(newPwd); err != nil {
		return err
	}
	hash, err := a.HashPassword(newPwd)
	if err != nil {
		return err
	}
	if err := a.db.Model(&admin).Update("password_hash", hash).Error; err != nil {
		return err
	}
	// Force re-login on every other session: revoke issued refresh tokens.
	return a.db.Model(&model.RefreshToken{}).
		Where("admin_id = ?", adminID).Update("revoked", true).Error
}

// AdminLogin authenticates an admin and returns (access token, refresh token, access expiry).
func (a *AuthService) AdminLogin(username, password string) (string, string, time.Time, error) {
	var admin model.Admin
	if err := a.db.Where("username = ?", username).First(&admin).Error; err != nil {
		return "", "", time.Time{}, errors.New("invalid credentials")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(password)); err != nil {
		return "", "", time.Time{}, errors.New("invalid credentials")
	}
	access, exp, err := a.issueAdminToken(&admin)
	if err != nil {
		return "", "", time.Time{}, err
	}
	refresh, err := a.issueRefreshToken(admin.ID)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return access, refresh, exp, nil
}

// RefreshAdmin exchanges a refresh token for a new access token. The refresh
// token is opaque, DB-stored, and revocable.
func (a *AuthService) RefreshAdmin(refreshToken string) (string, time.Time, error) {
	var rt model.RefreshToken
	if err := a.db.Where("id = ?", refreshToken).First(&rt).Error; err != nil {
		return "", time.Time{}, errors.New("invalid refresh token")
	}
	if rt.Revoked || time.Now().After(rt.ExpiresAt) {
		return "", time.Time{}, errors.New("refresh token revoked or expired")
	}
	var admin model.Admin
	if err := a.db.First(&admin, rt.AdminID).Error; err != nil {
		return "", time.Time{}, errors.New("admin not found")
	}
	return a.issueAdminToken(&admin)
}

// UserLogin authenticates a user by email (only if a password is set) and
// returns an access token plus the user's level. Users do not receive refresh
// tokens. The email is normalized to lowercase so login is case-insensitive.
func (a *AuthService) UserLogin(email, password string) (string, time.Time, int, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	var user model.User
	if err := a.db.Where("email = ?", email).First(&user).Error; err != nil {
		return "", time.Time{}, 0, errors.New("invalid credentials")
	}
	if user.PasswordHash == nil {
		return "", time.Time{}, 0, errors.New("user has no password set")
	}
	if err := bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(password)); err != nil {
		return "", time.Time{}, 0, errors.New("invalid credentials")
	}
	if !user.Enabled {
		return "", time.Time{}, 0, errors.New("user disabled")
	}
	at, _ := a.ttl()
	exp := time.Now().Add(at)
	claims := Claims{
		Type:   "user",
		UserID: user.ID,
		Level:  user.Level,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	if user.Username != nil {
		claims.Username = *user.Username
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(a.secret))
	return s, exp, user.Level, err
}

// RegisterUser creates a new user account if registration is enabled. When
// invite codes are required it validates and consumes one; when email
// verification is required it creates a pending (disabled) account plus a
// verification token and emails the link, returning pending=true with no token
// (the caller should instruct the user to check their inbox). Otherwise it
// auto-logs-in and returns a token.
func (a *AuthService) RegisterUser(username, email, password, inviteCode string) (user *model.User, token string, exp time.Time, pending bool, err error) {
	// Normalize the email: it is the account's unique key and login identifier,
	// so store it lowercased for consistent case-insensitive matching.
	email = strings.ToLower(strings.TrimSpace(email))
	if a.sysCfg != nil && !a.sysCfg.IsRegisterEnabled() {
		return nil, "", time.Time{}, false, errors.New("registration is disabled")
	}

	// Invite gating.
	if a.sysCfg != nil && a.sysCfg.IsRegisterRequireInvite() {
		if a.inviteSvc == nil {
			return nil, "", time.Time{}, false, errors.New("invite service unavailable")
		}
		if inviteCode == "" {
			return nil, "", time.Time{}, false, errors.New("invite code required")
		}
		if _, err = a.inviteSvc.ValidateAndConsume(inviteCode); err != nil {
			return nil, "", time.Time{}, false, err
		}
	}

	policy := DefaultPasswordPolicy()
	if a.sysCfg != nil {
		policy = a.sysCfg.GetPasswordPolicy()
	}
	if err = policy.Validate(password); err != nil {
		return nil, "", time.Time{}, false, err
	}

	// Check if username or email already exists.
	var count int64
	a.db.Model(&model.User{}).Where("username = ?", username).Count(&count)
	if count > 0 {
		return nil, "", time.Time{}, false, errors.New("username already exists")
	}
	a.db.Model(&model.User{}).Where("email = ?", email).Count(&count)
	if count > 0 {
		return nil, "", time.Time{}, false, errors.New("email already exists")
	}

	hash, herr := HashPassword(password)
	if herr != nil {
		return nil, "", time.Time{}, false, herr
	}

	requireVerify := a.sysCfg != nil && a.sysCfg.IsRegisterRequireEmailVerify()
	// New users inherit the global default invite quota (admin can override per
	// user later via Update).
	defaultQuota := 0
	if a.sysCfg != nil {
		defaultQuota = a.sysCfg.GetInviteDefaultUserQuota()
	}

	user = &model.User{
		ID:           util.NewUserID(),
		Username:     &username,
		Email:        email,
		PasswordHash: &hash,
		Enabled:      !requireVerify, // pending until verified when required
		Level:        0,
		Credential:   util.NewCredential(),
		SubToken:     util.RandomToken(16),
		MaxInvites:   defaultQuota,
	}

	if err = a.db.Create(user).Error; err != nil {
		return nil, "", time.Time{}, false, err
	}

	// Best-effort admin alert: notify on a new registration when the admin
	// enabled the new_registration alert. The bot service swallows errors.
	if a.telegramSvc != nil {
		source := "open"
		if a.sysCfg != nil && a.sysCfg.IsRegisterRequireInvite() {
			source = "invite"
		}
		a.telegramSvc.NotifyAdminEvent(CfgKeyAlertNewRegistration,
			fmt.Sprintf("New user registered: %s (via %s)", email, source))
	}

	// Email verification required → hold the account and email a link.
	if requireVerify {
		vtok := util.RandomToken(16)
		ev := &model.EmailVerification{
			ID:        util.NewVerificationID(),
			UserID:    user.ID,
			Email:     email,
			Token:     vtok,
			Purpose:   "register",
			ExpiresAt: time.Now().Add(24 * time.Hour),
		}
		if err = a.db.Create(ev).Error; err != nil {
			return user, "", time.Time{}, true, fmt.Errorf("create verification: %w", err)
		}
		if a.emailSvc != nil {
			link := a.buildVerifyLink(vtok)
			if serr := a.emailSvc.SendVerification(email, link, vtok); serr != nil {
				// Best-effort: the pending account + valid token still let the
				// user verify later; surface via log rather than failing signup.
				log.Warnf("registration: failed to send verification email to %s: %v", email, serr)
			}
		}
		return user, "", time.Time{}, true, nil
	}

	// Auto-login: issue token.
	token, exp, _, err = a.UserLogin(email, password)
	if err != nil {
		return user, "", time.Time{}, false, fmt.Errorf("auto-login failed: %w", err)
	}
	return user, token, exp, false, nil
}

// buildVerifyLink returns the clickable verification URL, or "" when
// site.base_url is not configured (the raw token is shown in the email).
func (a *AuthService) buildVerifyLink(token string) string {
	if a.sysCfg == nil {
		return ""
	}
	base := a.sysCfg.GetSiteBaseURL()
	if base == "" {
		return ""
	}
	return strings.TrimRight(base, "/") + "/verify-email?token=" + token
}

// VerifyEmail activates a pending account by redeeming its verification token.
func (a *AuthService) VerifyEmail(token string) error {
	if token == "" {
		return errors.New("verification token required")
	}
	var ev model.EmailVerification
	if err := a.db.Where("token = ?", token).First(&ev).Error; err != nil {
		return errors.New("invalid verification token")
	}
	if !ev.Valid(time.Now()) {
		return errors.New("verification token is invalid or expired")
	}
	var user model.User
	if err := a.db.First(&user, "id = ?", ev.UserID).Error; err != nil {
		return errors.New("user not found")
	}
	if err := a.db.Model(&user).Updates(map[string]any{
		"enabled":        true,
		"email_verified": true,
	}).Error; err != nil {
		return err
	}
	return a.db.Model(&ev).Update("consumed_at", new(time.Now())).Error
}

func (a *AuthService) IsRegisterEnabled() bool {
	if a.sysCfg == nil {
		return false
	}
	return a.sysCfg.IsRegisterEnabled()
}

// IsRegisterRequireInvite reports whether registration requires an invite code.
func (a *AuthService) IsRegisterRequireInvite() bool {
	if a.sysCfg == nil {
		return false
	}
	return a.sysCfg.IsRegisterRequireInvite()
}

// IsRegisterRequireEmailVerify reports whether new accounts must verify email.
func (a *AuthService) IsRegisterRequireEmailVerify() bool {
	if a.sysCfg == nil {
		return false
	}
	return a.sysCfg.IsRegisterRequireEmailVerify()
}

// VerifyToken parses and validates a JWT, returning the claims.
func (a *AuthService) VerifyToken(tokenStr string) (*Claims, error) {
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(tokenStr, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return []byte(a.secret), nil
	})
	if err != nil {
		return nil, err
	}
	return claims, nil
}

func (a *AuthService) issueAdminToken(admin *model.Admin) (string, time.Time, error) {
	at, _ := a.ttl()
	exp := time.Now().Add(at)
	claims := Claims{
		Type:     "admin",
		AdminID:  admin.ID,
		Username: admin.Username,
		Role:     admin.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(exp),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	s, err := tok.SignedString([]byte(a.secret))
	return s, exp, err
}

func (a *AuthService) issueRefreshToken(adminID uint) (string, error) {
	_, rtTTL := a.ttl()
	rt := model.RefreshToken{
		ID:        util.RandomToken(16),
		AdminID:   adminID,
		ExpiresAt: time.Now().Add(rtTTL),
	}
	if err := a.db.Create(&rt).Error; err != nil {
		return "", err
	}
	return rt.ID, nil
}
