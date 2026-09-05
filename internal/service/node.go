package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gorm.io/datatypes"
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

// Create persists a new node, minting an ID and token if unset. A virtual child
// node (ParentID set) is validated against its parent and gets its own ID as a
// token placeholder: it never authenticates (NodeAuth only matches real nodes),
// but the token column is not null + unique, so it needs a value that is
// clearly not a usable secret. A virtual child with a Reality short ID gets
// that SID appended to its parent's short_ids whitelist.
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
	// Auto-assign a dedicated Reality short ID to virtual children of reality
	// nodes so every entry point is attributable out of the box. Admins can
	// override it in the editor; parents without a reality config get none.
	if node.ParentID != nil && node.RealitySID == "" && parent.RealityConfig != nil {
		node.RealitySID = util.RandomToken(8)
	}
	if s.nameExists(node.Name, "") {
		return fmt.Errorf("node name %q already exists", node.Name)
	}
	if node.RealitySID != "" {
		if err := s.checkRealitySIDAvailable(node, parent, ""); err != nil {
			return err
		}
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(node).Error; err != nil {
			return err
		}
		if node.ParentID != nil && node.RealitySID != "" {
			return syncParentShortIDs(tx, *node.ParentID, node.RealitySID, "", node.ID)
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

// checkRealitySIDAvailable rejects a virtual node's Reality short ID when
// another node already uses it or it collides with the parent's short_ids
// whitelist entries. ignoreSID is the node's previous SID (allowed because it
// is being re-set and re-synced); parent may be nil.
func (s *NodeService) checkRealitySIDAvailable(node *model.Node, parent *model.Node, ignoreSID string) error {
	var count int64
	if err := s.db.Model(&model.Node{}).
		Where("reality_sid = ? AND id <> ?", node.RealitySID, node.ID).
		Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("reality_sid %q is already used by another node", node.RealitySID)
	}
	if parent == nil || parent.RealityConfig == nil || node.RealitySID == ignoreSID {
		return nil
	}
	var rc wire.RealityConfig
	if err := json.Unmarshal(*parent.RealityConfig, &rc); err != nil {
		return fmt.Errorf("decode parent reality config: %w", err)
	}
	for _, sid := range rc.ShortIds {
		if sid == node.RealitySID {
			return fmt.Errorf("reality_sid %q collides with the parent node's short_ids", node.RealitySID)
		}
	}
	return nil
}

// syncParentShortIDs adds addSID to and removes removeSID from the parent
// node's Reality short_ids whitelist, persisting the updated RealityConfig.
// The parent row is reloaded by ID so the whitelist is always read from the
// current transaction state (earlier steps in the same tx may already have
// modified it). Removal is skipped while another child of the same parent
// still uses the SID (excludeID is the node being changed or deleted).
// Parents without a RealityConfig are skipped — there is no whitelist to
// maintain until the parent is configured for reality, at which point Update
// re-syncs children.
func syncParentShortIDs(db *gorm.DB, parentID string, addSID, removeSID, excludeID string) error {
	if parentID == "" || (addSID == "" && removeSID == "") {
		return nil
	}
	var parent model.Node
	if err := db.First(&parent, "id = ?", parentID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // parent already gone; nothing to clean
		}
		return err
	}
	if parent.RealityConfig == nil {
		return nil
	}
	var rc wire.RealityConfig
	if err := json.Unmarshal(*parent.RealityConfig, &rc); err != nil {
		return fmt.Errorf("decode parent reality config: %w", err)
	}
	changed := false
	if removeSID != "" {
		var others int64
		if err := db.Model(&model.Node{}).
			Where("parent_id = ? AND reality_sid = ? AND id <> ?", parent.ID, removeSID, excludeID).
			Count(&others).Error; err != nil {
			return err
		}
		if others == 0 {
			kept := rc.ShortIds[:0]
			for _, sid := range rc.ShortIds {
				if sid != removeSID {
					kept = append(kept, sid)
				}
			}
			if len(kept) != len(rc.ShortIds) {
				rc.ShortIds = kept
				changed = true
			}
		}
	}
	if addSID != "" {
		exists := false
		for _, sid := range rc.ShortIds {
			if sid == addSID {
				exists = true
				break
			}
		}
		if !exists {
			rc.ShortIds = append(rc.ShortIds, addSID)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(rc)
	if err != nil {
		return err
	}
	return db.Model(&model.Node{}).Where("id = ?", parent.ID).
		Update("reality_config", datatypes.JSON(b)).Error
}

// appendChildrenShortIDs adds every virtual child's reality_sid that is
// missing from the real node's short_ids whitelist. Called when a real node
// (re)gains a reality config so children configured earlier keep working.
func appendChildrenShortIDs(db *gorm.DB, parentID string) error {
	var parent model.Node
	if err := db.First(&parent, "id = ?", parentID).Error; err != nil {
		return err
	}
	if parent.RealityConfig == nil {
		return nil
	}
	var sids []string
	if err := db.Model(&model.Node{}).
		Where("parent_id = ? AND reality_sid <> ?", parentID, "").
		Pluck("reality_sid", &sids).Error; err != nil {
		return err
	}
	if len(sids) == 0 {
		return nil
	}
	var rc wire.RealityConfig
	if err := json.Unmarshal(*parent.RealityConfig, &rc); err != nil {
		return fmt.Errorf("decode parent reality config: %w", err)
	}
	existing := make(map[string]bool, len(rc.ShortIds))
	for _, sid := range rc.ShortIds {
		existing[sid] = true
	}
	changed := false
	for _, sid := range sids {
		if !existing[sid] {
			rc.ShortIds = append(rc.ShortIds, sid)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	b, err := json.Marshal(rc)
	if err != nil {
		return err
	}
	return db.Model(&model.Node{}).Where("id = ?", parent.ID).
		Update("reality_config", datatypes.JSON(b)).Error
}

// Update saves the full node state (PUT-replace semantics). The caller loads
// the existing node and applies the request before calling Update. Parent
// assignments are re-validated here — Create checks them too, but Update is a
// separate write path — so the API cannot produce a dangling, self-referential
// or two-levels-deep parent link. Reality short IDs are kept in sync with the
// parent's short_ids whitelist across sid changes, clears and reparenting.
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
	if node.ParentID != nil && node.RealitySID != "" {
		if err := s.checkRealitySIDAvailable(node, parent, old.RealitySID); err != nil {
			return err
		}
	}
	sameParent := old.ParentID != nil && node.ParentID != nil && *old.ParentID == *node.ParentID
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(node).Error; err != nil {
			return err
		}
		// Drop the previous SID from the previous parent's whitelist when the
		// sid changed, was cleared, or the node moved to another parent.
		if old.RealitySID != "" && old.ParentID != nil && (old.RealitySID != node.RealitySID || !sameParent) {
			if err := syncParentShortIDs(tx, *old.ParentID, "", old.RealitySID, node.ID); err != nil {
				return err
			}
		}
		if node.ParentID != nil && node.RealitySID != "" {
			if err := syncParentShortIDs(tx, *node.ParentID, node.RealitySID, "", node.ID); err != nil {
				return err
			}
		}
		// A real node (re)gaining a reality config must whitelist its virtual
		// children's sids so their share links keep working.
		if node.ParentID == nil && node.RealityConfig != nil {
			return appendChildrenShortIDs(tx, node.ID)
		}
		return nil
	})
}

// Delete removes a node and its user assignments. Virtual child nodes of the
// deleted node are removed first (cascading), along with their user assignments.
// If the deleted node is itself a virtual child with a Reality short ID, the
// SID is dropped from its parent's short_ids whitelist.
func (s *NodeService) Delete(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var self model.Node
		if err := tx.Select("id", "parent_id", "reality_sid").First(&self, "id = ?", id).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		} else if self.ParentID != nil && self.RealitySID != "" {
			if err := syncParentShortIDs(tx, *self.ParentID, "", self.RealitySID, self.ID); err != nil {
				return err
			}
		}
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
		// Reality short ID: optional, normalized to lowercase hex in place so
		// the stored value, the parent's whitelist and the agent's sid→node
		// map all agree on one canonical form.
		node.RealitySID = strings.ToLower(strings.TrimSpace(node.RealitySID))
		if node.RealitySID != "" && !realitySIDPattern.MatchString(node.RealitySID) {
			return fmt.Errorf("reality_sid must be 1-16 hex chars (got %q)", node.RealitySID)
		}
		return nil
	}
	// A real node is the default attribution target; a dedicated sid would be
	// meaningless on it.
	if node.RealitySID != "" {
		return errors.New("reality_sid is only valid on virtual child nodes")
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
