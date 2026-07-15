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

func (s *NodeService) List(page, pageSize int) ([]model.Node, int64, error) {
	var nodes []model.Node
	var total int64
	s.db.Model(&model.Node{}).Count(&total)
	err := s.db.Order("created_at DESC").
		Limit(pageSize).Offset((page - 1) * pageSize).
		Find(&nodes).Error
	if err != nil {
		return nil, 0, err
	}
	// Virtual child nodes never poll, so backfill their liveness from the parent.
	ptrs := make([]*model.Node, len(nodes))
	for i := range nodes {
		ptrs[i] = &nodes[i]
	}
	if err := hydrateVirtualOnline(s.db, ptrs); err != nil {
		return nil, 0, err
	}
	if err := s.hydrateParentNames(ptrs); err != nil {
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
		if err := hydrateVirtualOnline(s.db, []*model.Node{&node}); err != nil {
			return nil, err
		}
		if err := s.hydrateParentNames([]*model.Node{&node}); err != nil {
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

// hydrateParentNames fills ParentName for virtual child nodes from a single
// query over the distinct parent IDs on the page, so the admin UI can show the
// parent's display name without depending on the parent being on the same page
// (which pagination/filtering would otherwise break).
func (s *NodeService) hydrateParentNames(nodes []*model.Node) error {
	parentIDs := make([]string, 0)
	for _, n := range nodes {
		if n.ParentID != nil {
			parentIDs = append(parentIDs, *n.ParentID)
		}
	}
	if len(parentIDs) == 0 {
		return nil
	}
	var parents []struct {
		ID   string `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := s.db.Model(&model.Node{}).Select("id", "name").
		Where("id IN ?", parentIDs).Find(&parents).Error; err != nil {
		return err
	}
	names := make(map[string]string, len(parents))
	for i := range parents {
		names[parents[i].ID] = parents[i].Name
	}
	for _, n := range nodes {
		if n.ParentID != nil {
			n.ParentName = names[*n.ParentID]
		}
	}
	return nil
}

// hydrateVirtualOnline backfills LastSeenAt/Online for virtual child nodes from
// their parent, since virtual nodes never poll and have no liveness of their own.
func hydrateVirtualOnline(db *gorm.DB, nodes []*model.Node) error {
	parentIDs := make([]string, 0)
	for _, n := range nodes {
		if n.ParentID != nil {
			parentIDs = append(parentIDs, *n.ParentID)
		}
	}
	if len(parentIDs) == 0 {
		return nil
	}
	var parents []struct {
		ID         string     `gorm:"column:id"`
		LastSeenAt *time.Time `gorm:"column:last_seen_at"`
	}
	if err := db.Model(&model.Node{}).Select("id", "last_seen_at").
		Where("id IN ?", parentIDs).Find(&parents).Error; err != nil {
		return err
	}
	seen := make(map[string]*time.Time, len(parents))
	for i := range parents {
		seen[parents[i].ID] = parents[i].LastSeenAt
	}
	for _, n := range nodes {
		if n.ParentID != nil {
			if ls, ok := seen[*n.ParentID]; ok {
				n.LastSeenAt = ls
			}
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
	return s.db.Create(node).Error
}

// Update saves the full node state (PUT-replace semantics). The caller loads
// the existing node and applies the request before calling Update.
func (s *NodeService) Update(node *model.Node) error {
	if err := validateNode(node); err != nil {
		return err
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
		return errors.New("flow cannot be set when security is none")
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
	return nil
}
