package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/util"
	"github.com/vgate-project/vgate-manager/internal/wire"
)

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

// Create persists a new node, minting an ID and token if unset. A virtual child
// node (ParentID set) is validated against its parent but otherwise mints a token
// (unused — no server polls a virtual node) to satisfy the not-null constraint.
func (s *NodeService) Create(node *model.Node) error {
	if node.ID == "" {
		node.ID = util.NewNodeID()
	}
	if node.Token == "" {
		node.Token = util.RandomToken(32)
	}
	if node.ParentID != nil {
		parent, err := s.Get(*node.ParentID)
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
	if s.nameExists(node.Name, "") {
		return fmt.Errorf("node name %q already exists", node.Name)
	}
	return s.db.Create(node).Error
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

// Update saves the full node state (PUT-replace semantics). The caller loads
// the existing node and applies the request before calling Update.
func (s *NodeService) Update(node *model.Node) error {
	if err := validateNode(node); err != nil {
		return err
	}
	if s.nameExists(node.Name, node.ID) {
		return fmt.Errorf("node name %q already exists", node.Name)
	}
	return s.db.Save(node).Error
}

// Delete removes a node and its user assignments. Virtual child nodes of the
// deleted node are removed first (cascading), along with their user assignments.
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
	if node.ParentID != nil {
		if node.Name == "" {
			return errors.New("name is required")
		}
		if node.Address == "" {
			return errors.New("address is required")
		}
		return nil
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
	// TrafficMultiplier must be a positive factor. (0 is only allowed because
	// applyNodeRequest normalizes it to 1; reject any other non-positive value.)
	if node.TrafficMultiplier != 0 && (node.TrafficMultiplier < 0.01 || node.TrafficMultiplier > 1000) {
		return fmt.Errorf("traffic_multiplier must be between 0.01 and 1000 (got %g)", node.TrafficMultiplier)
	}
	const maxSpeedBps = 10 * 1024 * 1024 * 1024 // 10 Gbps
	if node.SpeedLimitUpBps < 0 || node.SpeedLimitUpBps > maxSpeedBps {
		return fmt.Errorf("speed_limit_up_bps must be between 0 and %d bytes/sec (got %d)", maxSpeedBps, node.SpeedLimitUpBps)
	}
	if node.SpeedLimitDownBps < 0 || node.SpeedLimitDownBps > maxSpeedBps {
		return fmt.Errorf("speed_limit_down_bps must be between 0 and %d bytes/sec (got %d)", maxSpeedBps, node.SpeedLimitDownBps)
	}
	return nil
}
