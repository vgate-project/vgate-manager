package service

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
)

type UserService struct {
	db     *gorm.DB
	sysCfg *SystemConfigService
}

func NewUserService(db *gorm.DB, sysCfg *SystemConfigService) *UserService {
	return &UserService{db: db, sysCfg: sysCfg}
}

// UserListFilter holds the optional filtering/sorting parameters for List.
// Enabled is a pointer so that an unset value (nil) means "no filter".
type UserListFilter struct {
	Search  string // case-insensitive substring match on email or username
	Enabled *bool  // filter by enabled state when set
	SortBy  string // created_at|email|username|quota_bytes|used|expire_at|level
	Order   string // asc|desc
}

// sortableColumns whitelists columns that may appear in an ORDER BY clause to
// avoid injecting arbitrary user input into the SQL query. "used" is a derived
// expression (up_total + down_total) since there is no stored column for it.
var sortableColumns = map[string]string{
	"created_at":  "created_at",
	"email":       "email",
	"username":    "username",
	"level":       "level",
	"quota_bytes": "quota_bytes",
	"up_total":    "up_total",
	"down_total":  "down_total",
	"used":        "(up_total + down_total)",
	"expire_at":   "expire_at",
}

func (s *UserService) List(filter UserListFilter, page, pageSize int) ([]model.User, int64, error) {
	q := s.db.Model(&model.User{})
	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		q = q.Where("email LIKE ? OR username LIKE ?", like, like)
	}
	if filter.Enabled != nil {
		q = q.Where("enabled = ?", *filter.Enabled)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	order := "created_at DESC"
	if col, ok := sortableColumns[filter.SortBy]; ok {
		dir := "ASC"
		if strings.EqualFold(filter.Order, "desc") {
			dir = "DESC"
		}
		order = col + " " + dir
	}

	var users []model.User
	err := q.Order(order).
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&users).Error
	return users, total, err
}

func (s *UserService) Get(id string) (*model.User, error) {
	var user model.User
	if err := s.db.First(&user, "id = ?", id).Error; err != nil {
		return nil, err
	}
	// The active product's kind is persisted on the user when its subscription
	// effect is applied; use it to resolve the display name and, for plans, the
	// traffic-reset package. No ambiguous plan→package fallback needed.
	if user.CurrentProductID != "" {
		switch user.CurrentProductKind {
		case model.OrderKindPlan:
			var plan model.Plan
			if err := s.db.First(&plan, "id = ?", user.CurrentProductID).Error; err == nil {
				user.CurrentProductName = plan.Name
				user.CurrentPlanResetEnabled = plan.ResetEnabled
				user.CurrentPlanResetPrice = plan.ResetPrice
			}
		case model.OrderKindTraffic:
			var pkg model.TrafficPackage
			if err := s.db.First(&pkg, "id = ?", user.CurrentProductID).Error; err == nil {
				user.CurrentProductName = pkg.Name
			}
		}
	}
	s.setDerivedFlags(&user)
	return &user, nil
}

// setDerivedFlags populates non-persisted fields on a user after load.
func (s *UserService) setDerivedFlags(user *model.User) {
	user.HasPassword = user.PasswordHash != nil
}

// Create persists a new user, minting a UUID primary key, a rotatable VLESS
// credential, and a subscription token. If password is non-empty it is hashed
// and stored.
func (s *UserService) Create(user *model.User, password string) error {
	if user.ID == "" {
		user.ID = util.NewUserID()
	}
	if user.Credential == "" {
		user.Credential = util.NewCredential()
	}
	if user.SubToken == "" {
		user.SubToken = util.RandomToken(16)
	}
	if password != "" {
		hash, err := HashPassword(password)
		if err != nil {
			return err
		}
		user.PasswordHash = &hash
	}
	return s.db.Create(user).Error
}

// Update saves the full user state (PUT-replace). Password is handled by
// SetPassword, not here.
func (s *UserService) Update(user *model.User) error {
	return s.db.Save(user).Error
}

func (s *UserService) Delete(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", id).Delete(&model.UserNode{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.User{}, "id = ?", id).Error
	})
}

func (s *UserService) RegenerateSubToken(id string) (string, error) {
	tok := util.RandomToken(16)
	res := s.db.Model(&model.User{}).Where("id = ?", id).Update("sub_token", tok)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", gorm.ErrRecordNotFound
	}
	return tok, nil
}

// RegenerateCredential mints a fresh, rotatable VLESS credential for the user
// and returns it. The internal primary key is untouched, so existing
// relationships (orders, traffic, sub_token) are preserved while a leaked
// credential can be revoked. Not found returns ErrRecordNotFound.
func (s *UserService) RegenerateCredential(id string) (string, error) {
	tok := util.NewCredential()
	res := s.db.Model(&model.User{}).Where("id = ?", id).Update("credential", tok)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", gorm.ErrRecordNotFound
	}
	return tok, nil
}

// SetPassword hashes and stores a new password for the user.
func (s *UserService) SetPassword(id string, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	res := s.db.Model(&model.User{}).Where("id = ?", id).Update("password_hash", hash)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

// ChangeOwnPassword lets a user rotate their own password. If a password is
// already set, the current password must be supplied and verified. Passwordless
// users (PasswordHash == nil) may set a first password by leaving oldPwd empty;
// supplying a non-empty oldPwd in that state is rejected. The new password is
// subject to the shared strength policy.
func (s *UserService) ChangeOwnPassword(userID, oldPwd, newPwd string) error {
	var user model.User
	if err := s.db.First(&user, "id = ?", userID).Error; err != nil {
		return err
	}
	if user.PasswordHash != nil {
		if err := bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(oldPwd)); err != nil {
			return errors.New("invalid current password")
		}
	} else if oldPwd != "" {
		return errors.New("user has no password set")
	}
	policy := DefaultPasswordPolicy()
	if s.sysCfg != nil {
		policy = s.sysCfg.GetPasswordPolicy()
	}
	if err := policy.Validate(newPwd); err != nil {
		return err
	}
	hash, err := HashPassword(newPwd)
	if err != nil {
		return err
	}
	return s.db.Model(&user).Update("password_hash", hash).Error
}

// ResetDueQuotas zeroes the monthly usage counters (up_total/down_total) of
// every user that participates in the global monthly reset, renewing their
// quota window. The reset day itself is global and supplied by system_config
// (quota.reset_day); the caller (the daily cron) is responsible for only
// invoking this on that day, so it cannot double-reset within a month.
// It only affects users with a finite quota (quota_bytes <> 0); unlimited
// users and users who have opted out (quota_reset_enabled = false, e.g. after
// buying a one-time traffic package) keep their historical stats. last_reset_at
// is stamped so the reset is recorded. Returns the number of users reset.
func (s *UserService) ResetDueQuotas() (int64, error) {
	res := s.db.Model(&model.User{}).
		Where("quota_reset_enabled = ? AND quota_bytes <> ?", true, 0).
		Updates(map[string]any{
			"up_total":      0,
			"down_total":    0,
			"last_reset_at": gorm.Expr("CURRENT_TIMESTAMP"),
		})
	return res.RowsAffected, res.Error
}

// ListNodesForUser returns the nodes a user can use: nodes at or below the
// user's level by default (the level tier), plus any node the admin explicitly
// granted via a user_nodes.override row despite a higher level. No assignment
// row is required for within-level nodes.
func (s *UserService) ListNodesForUser(userID string) ([]model.Node, error) {
	var user model.User
	if err := s.db.Select("level").First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	var nodes []model.Node
	err := s.db.
		Where("nodes.enabled = ?", true).
		Where("nodes.level <= ? OR EXISTS (SELECT 1 FROM user_nodes un WHERE un.node_id = nodes.id AND un.user_id = ? AND un.override = ?)", user.Level, userID, true).
		Order("nodes.created_at DESC").
		Find(&nodes).Error
	if err != nil {
		return nil, err
	}
	// Virtual child nodes never poll, so backfill their liveness from the parent
	// (mirrors NodeService.List/Get) so the frontend shows the correct online state.
	if len(nodes) > 0 {
		ptrs := make([]*model.Node, len(nodes))
		for i := range nodes {
			ptrs[i] = &nodes[i]
		}
		if err := hydrateVirtualOnline(s.db, ptrs); err != nil {
			return nil, err
		}
	}
	return nodes, nil
}

// EffectiveTrafficMultiplier returns the multiplier applied to a node's reported
// traffic. Virtual child nodes inherit their parent's multiplier (mirroring
// FetchConfig / ReportTraffic on the server side). A non-positive value (an
// unset/legacy node) is treated as 1 so traffic is never zeroed.
func (s *UserService) EffectiveTrafficMultiplier(node model.Node) (float64, error) {
	if node.ParentID == nil {
		if node.TrafficMultiplier <= 0 {
			return 1, nil
		}
		return node.TrafficMultiplier, nil
	}
	var parent model.Node
	if err := s.db.Select("traffic_multiplier").First(&parent, "id = ?", *node.ParentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 1, nil
		}
		return 0, err
	}
	if parent.TrafficMultiplier <= 0 {
		return 1, nil
	}
	return parent.TrafficMultiplier, nil
}

// ListUsersForNode returns the users assigned to a node (all, regardless of
// enabled/expiry — the server-facing FetchUsers filters those), paginated.
func (s *UserService) ListUsersForNode(nodeID string, page, pageSize int) ([]model.User, int64, error) {
	q := s.db.Model(&model.User{}).
		Joins("JOIN user_nodes ON user_nodes.user_id = users.id").
		Where("user_nodes.node_id = ?", nodeID)
	var total int64
	q.Count(&total)
	var users []model.User
	err := q.Order("users.created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&users).Error
	return users, total, err
}

// SetUserNodes replaces all node assignments for a user. Unknown node IDs are
// rejected. For each assigned node, Override is set when the node's level
// exceeds the user's level — that is the admin's deliberate grant of a
// higher-tier node to this specific user (default gate: node.level <= user.level).
func (s *UserService) SetUserNodes(userID string, nodeIDs []string) error {
	// Validate node IDs exist and load their levels.
	var nodes []model.Node
	if len(nodeIDs) > 0 {
		if err := s.db.Where("id IN ?", nodeIDs).Find(&nodes).Error; err != nil {
			return err
		}
		if len(nodes) != len(uniqueStrings(nodeIDs)) {
			return fmt.Errorf("one or more node_ids do not exist")
		}
	}
	// Resolve the user's level once.
	var user model.User
	if err := s.db.Select("level").First(&user, "id = ?", userID).Error; err != nil {
		return err
	}
	nodeLevel := make(map[string]int, len(nodes))
	for _, n := range nodes {
		nodeLevel[n.ID] = n.Level
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&model.UserNode{}).Error; err != nil {
			return err
		}
		for _, nid := range nodeIDs {
			override := nodeLevel[nid] > user.Level
			if err := tx.Create(&model.UserNode{
				UserID:   userID,
				NodeID:   nid,
				Override: override,
			}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
