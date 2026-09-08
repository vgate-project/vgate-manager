package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
	"github.com/vgate-project/vgate-manager/internal/wire"
)

// realitySIDPattern matches a normalized Reality short ID: 1-16 lowercase hex
// chars (up to 8 bytes, the Reality protocol maximum).
var realitySIDPattern = regexp.MustCompile("^[0-9a-f]{1,16}$")

type NodeService struct {
	db *gorm.DB
}

func NewNodeService(db *gorm.DB) *NodeService {
	return &NodeService{db: db}
}

// List returns a page of nodes, optionally filtered by type. nodeType is one
// of "all", "real" (no parent) or "virtual" (has a parent).
func (s *NodeService) List(page, pageSize int, nodeType string) ([]model.Node, int64, error) {
	q := s.db.Model(&model.Node{})
	switch nodeType {
	case "real":
		q = q.Where("parent_id IS NULL")
	case "virtual":
		q = q.Where("parent_id IS NOT NULL")
	}
	var nodes []model.Node
	var total int64
	q.Count(&total)
	err := q.Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&nodes).Error
	if err != nil {
		return nil, 0, err
	}
	// Virtual child nodes never poll, so backfill their liveness from the parent.
	ptrs := make([]*model.Node, len(nodes))
	for i := range nodes {
		// Real nodes compute Online from their own LastSeenAt.
		if nodes[i].ParentID == nil {
			nodes[i].Online = nodes[i].IsOnline()
		}
		ptrs[i] = &nodes[i]
	}
	if err := hydrateVirtualNodes(s.db, ptrs); err != nil {
		return nil, 0, err
	}
	return nodes, total, err
}

func (s *NodeService) Get(id string) (*model.Node, error) {
	var node model.Node
	if err := s.db.First(&node, "id = ?", id).Error; err != nil {
		return nil, err
	}
	if node.ParentID != nil {
		if err := hydrateVirtualNodes(s.db, []*model.Node{&node}); err != nil {
			return nil, err
		}
	}
	return &node, nil
}

// ResolveParent returns the real parent node of a virtual node, or nil when the
// node is real (has no parent). A lookup error is returned only on failure.
func (s *NodeService) ResolveParent(node *model.Node) (*model.Node, error) {
	if node.ParentID == nil {
		return nil, nil
	}
	return s.Get(*node.ParentID)
}

// hydrateVirtualNodes backfills Name, LastSeenAt, and Online for virtual child
// nodes. Virtual nodes never poll and have no liveness of their own. Parents
// already present in the slice are resolved in-memory; missing parents are
// fetched in a single combined DB query so the former hydrateVirtualOnline and
// hydrateParentNames no longer duplicate work or hit the database unnecessarily.
func hydrateVirtualNodes(db *gorm.DB, nodes []*model.Node) error {
	// 1. Index real (parent) nodes already in the slice for O(1) in-memory lookup.
	index := make(map[string]*model.Node, len(nodes))
	for _, n := range nodes {
		if n != nil && n.ParentID == nil {
			index[n.ID] = n
		}
	}

	// 2. Collect parent IDs that are NOT in the slice (need a DB lookup).
	missing := make(map[string]bool)
	for _, n := range nodes {
		if n == nil || n.ParentID == nil {
			continue
		}
		if _, ok := index[*n.ParentID]; ok {
			continue // will be resolved from memory below
		}
		missing[*n.ParentID] = true
	}

	// 3. Single combined DB query — only for parents absent from the slice.
	if len(missing) > 0 {
		ids := make([]string, 0, len(missing))
		for id := range missing {
			ids = append(ids, id)
		}
		type parentRow struct {
			ID         string     `gorm:"column:id"`
			Name       string     `gorm:"column:name"`
			LastSeenAt *time.Time `gorm:"column:last_seen_at"`
		}
		var parents []parentRow
		if err := db.Model(&model.Node{}).
			Select("id", "name", "last_seen_at").
			Where("id IN ?", ids).Find(&parents).Error; err != nil {
			return err
		}
		for _, p := range parents {
			index[p.ID] = &model.Node{Name: p.Name, LastSeenAt: p.LastSeenAt}
		}
	}

	// 4. Apply parent data to every virtual child.
	for _, n := range nodes {
		if n == nil || n.ParentID == nil {
			continue
		}
		if parent, ok := index[*n.ParentID]; ok {
			n.LastSeenAt = parent.LastSeenAt
			n.ParentName = parent.Name
			n.Online = n.IsOnline()
		}
	}
	return nil
}

// Create persists a new node, minting an ID and token if unset. Every node —
// real or virtual — gets its own Reality short ID (auto-generated, globally
// unique) so each entry point is trackable without an empty sid. A virtual
// child whose sid takes its parent's own forces the parent to re-issue a fresh
// one (child priority).
func (s *NodeService) Create(node *model.Node) error {
	if node.ID == "" {
		node.ID = util.NewNodeID()
	}
	if node.Token == "" {
		if node.ParentID != nil {
			node.Token = node.ID
		} else {
			node.Token = util.RandomToken(32)
		}
	}
	var parent *model.Node
	if node.ParentID != nil {
		var err error
		parent, err = s.Get(*node.ParentID)
		if err != nil {
			return fmt.Errorf("parent node not found: %w", err)
		}
		if parent.ParentID != nil {
			return errors.New("a virtual node cannot be the parent of another virtual node")
		}
	}
	if err := validateNode(node); err != nil {
		return err
	}
	// Auto-generate the node's own Reality short ID when left empty: real
	// nodes and virtual children of reality parents always get one.
	isRealityEntry := (node.ParentID == nil && node.Security == "reality") ||
		(node.ParentID != nil && parent.Security == "reality")
	if isRealityEntry && node.RealitySID == "" {
		sid, err := generateShortID(s.db)
		if err != nil {
			return err
		}
		node.RealitySID = sid
	}
	if s.nameExists(node.Name, "") {
		return fmt.Errorf("node name %q already exists", node.Name)
	}
	if node.RealitySID != "" {
		if err := s.checkShortIDConflict(node); err != nil {
			return err
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(node).Error; err != nil {
			return err
		}
		// Child priority: a virtual child taking its parent's own sid forces
		// the parent to re-issue a fresh one (avoiding every child's sid).
		if node.ParentID != nil && node.RealitySID != "" && parent.RealitySID == node.RealitySID {
			return regenerateOwnShortID(tx, parent)
		}
		return nil
	})
}

// nameExists reports whether another node already uses the given name.
// excludeID is the node being updated (skipped so it doesn't conflict with itself).
func (s *NodeService) nameExists(name, excludeID string) bool {
	var count int64
	q := s.db.Model(&model.Node{}).Where("name = ?", name)
	if excludeID != "" {
		q = q.Where("id <> ?", excludeID)
	}
	q.Count(&count)
	return count > 0
}

// generateShortID returns a random Reality short ID that no other node uses.
// Collisions trigger a bounded retry rather than an error — the 16-hex-char
// space makes them practically impossible, but the loop makes the avoidance
// guarantee explicit. Because the uniqueness query spans every node row, a
// generated sid automatically avoids all children's sids too.
func generateShortID(db *gorm.DB) (string, error) {
	for i := 0; i < 8; i++ {
		sid := util.RandomToken(8)
		var count int64
		if err := db.Model(&model.Node{}).Where("reality_sid = ?", sid).Count(&count).Error; err != nil {
			return "", err
		}
		if count == 0 {
			return sid, nil
		}
	}
	return "", errors.New("could not generate a unique reality short id")
}

// checkShortIDConflict rejects a manually provided Reality short ID that
// another node already uses. A virtual node does not conflict with its
// parent's own sid — child priority: the parent re-issues its own instead —
// while a real node cannot claim a sid one of its virtual children holds.
func (s *NodeService) checkShortIDConflict(node *model.Node) error {
	q := s.db.Model(&model.Node{}).
		Where("reality_sid = ? AND id <> ?", node.RealitySID, node.ID)
	if node.ParentID != nil {
		q = q.Where("id <> ?", *node.ParentID)
	}
	var count int64
	if err := q.Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	if node.ParentID == nil {
		var heldByChild int64
		if err := s.db.Model(&model.Node{}).
			Where("parent_id = ? AND reality_sid = ?", node.ID, node.RealitySID).
			Count(&heldByChild).Error; err != nil {
			return err
		}
		if heldByChild > 0 {
			return fmt.Errorf("reality_sid %q is reserved by a virtual child (child priority)", node.RealitySID)
		}
	}
	return fmt.Errorf("reality_sid %q is already used by another node", node.RealitySID)
}

// regenerateOwnShortID re-issues a real node's own Reality short ID so it no
// longer collides with one of its virtual children (child priority: the child
// keeps its sid, the parent yields). The uniqueness query inside
// generateShortID spans every node row, so the fresh value avoids every
// child's sid as well.
func regenerateOwnShortID(db *gorm.DB, parent *model.Node) error {
	sid, err := generateShortID(db)
	if err != nil {
		return err
	}
	return db.Model(&model.Node{}).Where("id = ?", parent.ID).
		Update("reality_sid", sid).Error
}

// Update saves the full node state (PUT-replace semantics). The caller loads
// the existing node and applies the request before calling Update. Parent
// assignments are re-validated here — Create checks them too, but Update is a
// separate write path — so the API cannot produce a dangling, self-referential
// or two-levels-deep parent link.
func (s *NodeService) Update(node *model.Node) error {
	// Load the persisted row: the handler overwrites ParentID/RealitySID on the
	// passed struct, so the previous values must come from the database.
	var old model.Node
	if err := s.db.Select("id", "parent_id", "reality_sid").First(&old, "id = ?", node.ID).Error; err != nil {
		return err
	}
	var parent *model.Node
	if node.ParentID != nil {
		if *node.ParentID == node.ID {
			return errors.New("a node cannot be its own parent")
		}
		var err error
		parent, err = s.Get(*node.ParentID)
		if err != nil {
			return fmt.Errorf("parent node not found: %w", err)
		}
		if parent.ParentID != nil {
			return errors.New("a virtual node cannot be the parent of another virtual node")
		}
		// A node that itself has children cannot become a virtual child: its
		// children would become grandchildren, which the one-level model
		// (hydrateVirtualNodes, FetchConfig) does not support.
		var childCount int64
		if err := s.db.Model(&model.Node{}).Where("parent_id = ?", node.ID).Count(&childCount).Error; err != nil {
			return err
		}
		if childCount > 0 {
			return errors.New("a node with virtual children cannot become a virtual node")
		}
	}
	if err := validateNode(node); err != nil {
		return err
	}
	if s.nameExists(node.Name, node.ID) {
		return fmt.Errorf("node name %q already exists", node.Name)
	}
	if node.RealitySID != "" {
		if err := s.checkShortIDConflict(node); err != nil {
			return err
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(node).Error; err != nil {
			return err
		}
		// Child priority: a virtual child taking its parent's own sid forces
		// the parent to re-issue a fresh one (avoiding every child's sid).
		if node.ParentID != nil && node.RealitySID != "" && parent.RealitySID == node.RealitySID {
			return regenerateOwnShortID(tx, parent)
		}
		return nil
	})
}

// Delete removes a node and its user assignments. Virtual child nodes of the
// deleted node are removed first (cascading), along with their user assignments.
// The delivered short_ids whitelist is computed at config time, so a deleted
// child's sid automatically disappears from what the agent receives.
func (s *NodeService) Delete(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var childIDs []string
		if err := tx.Model(&model.Node{}).Where("parent_id = ?", id).Pluck("id", &childIDs).Error; err != nil {
			return err
		}
		if len(childIDs) > 0 {
			if err := tx.Where("node_id IN ?", childIDs).Delete(&model.UserNode{}).Error; err != nil {
				return err
			}
			if err := tx.Where("parent_id = ?", id).Delete(&model.Node{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.UserNode{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.Node{}, "id = ?", id).Error
	})
}

// RegenerateToken issues a new node token and returns it.
func (s *NodeService) RegenerateToken(id string) (string, error) {
	tok := util.RandomToken(32)
	res := s.db.Model(&model.Node{}).Where("id = ?", id).Update("token", tok)
	if res.Error != nil {
		return "", res.Error
	}
	if res.RowsAffected == 0 {
		return "", gorm.ErrRecordNotFound
	}
	return tok, nil
}

// validateNode enforces field-format and the v2/vision mutual-exclusion rules.
// Virtual child nodes (ParentID set) only need a Name and Address — their
// transport config is inherited from the parent, so the transport checks below
// are skipped.
func validateNode(node *model.Node) error {
	// Reality short ID: optional, normalized to lowercase hex in place so the
	// stored value, the delivered whitelist and the agent's sid→node map all
	// agree on one canonical form.
	node.RealitySID = strings.ToLower(strings.TrimSpace(node.RealitySID))
	if node.RealitySID != "" && !realitySIDPattern.MatchString(node.RealitySID) {
		return fmt.Errorf("reality_sid must be 1-16 hex chars (got %q)", node.RealitySID)
	}
	// TrafficMultiplier must be a positive factor. Virtual children carry their
	// own multiplier (no longer inherited from the parent), so this check runs
	// before the virtual early-return below. (0 is only allowed because
	// applyNodeRequest normalizes it to 1; reject any other non-positive value.)
	if node.TrafficMultiplier != 0 && (node.TrafficMultiplier < 0.01 || node.TrafficMultiplier > 1000) {
		return fmt.Errorf("traffic_multiplier must be between 0.01 and 1000 (got %g)", node.TrafficMultiplier)
	}
	if node.ParentID != nil {
		if node.Name == "" {
			return errors.New("name is required")
		}
		if node.Address == "" {
			return errors.New("address is required")
		}
		return nil
	}
	// A real node's own sid only means something under reality security.
	if node.RealitySID != "" && node.Security != "reality" {
		return errors.New("reality_sid requires reality security")
	}
	switch node.Network {
	case "", "tcp", "ws", "xhttp":
	default:
		return fmt.Errorf("invalid network %q (want tcp|ws|xhttp)", node.Network)
	}
	if node.Port <= 0 {
		return errors.New("port is required")
	}
	switch node.Security {
	case "", "none", "tls", "reality":
	default:
		return fmt.Errorf("invalid security %q (want none|tls|reality)", node.Security)
	}
	if node.Security == "" {
		return errors.New("security is required")
	}
	// flow cannot be set when security is none
	if node.Security == "none" && node.Flow != nil && *node.Flow != "" {
		return errors.New("invalid flow: cannot be set when security is none")
	}
	// flow can only be used with tcp network
	if node.Flow != nil && *node.Flow != "" && node.Network != "tcp" && node.Network != "" {
		return errors.New("flow can only be used with tcp network")
	}
	if node.Security == "reality" && node.RealityConfig != nil {
		var rc wire.RealityConfig
		if err := json.Unmarshal(*node.RealityConfig, &rc); err != nil {
			return fmt.Errorf("decode reality config: %w", err)
		}
		if rc.ServerName == "" {
			return errors.New("server_name (SNI) is required for reality security")
		}
	}
	// v2 encryption (VLESS.Decryption) and xtls-rprx-vision are mutually exclusive.
	if node.VLESS != nil && len(*node.VLESS) > 0 {
		var vl wire.VLESS
		if err := json.Unmarshal(*node.VLESS, &vl); err != nil {
			return fmt.Errorf("decode vless: %w", err)
		}
		if vl.Decryption != "" && node.Flow != nil && *node.Flow == "xtls-rprx-vision" {
			return errors.New("v2 encryption and xtls-rprx-vision are mutually exclusive")
		}
	}
	// (Multiplier is validated above so virtual children are covered too.)
	const maxSpeedBps = 10 * 1024 * 1024 * 1024 // 10 Gbps
	if node.SpeedLimitUpBps < 0 || node.SpeedLimitUpBps > maxSpeedBps {
		return fmt.Errorf("speed_limit_up_bps must be between 0 and %d bytes/sec (got %d)", maxSpeedBps, node.SpeedLimitUpBps)
	}
	if node.SpeedLimitDownBps < 0 || node.SpeedLimitDownBps > maxSpeedBps {
		return fmt.Errorf("speed_limit_down_bps must be between 0 and %d bytes/sec (got %d)", maxSpeedBps, node.SpeedLimitDownBps)
	}
	return nil
}
