// Package service contains the manager's business logic. The ServerService
// implements the three server-facing endpoints consumed by vgate nodes.
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/wire"
)

type ServerService struct {
	db *gorm.DB
}

func NewServerService(db *gorm.DB) *ServerService {
	return &ServerService{db: db}
}

// FetchConfig materializes a node's stored JSON config into the wire.Config
// shape the node expects from GET /server/config. A virtual child node has no
// server of its own, so if one is passed we resolve its parent's config.
func (s *ServerService) FetchConfig(node *model.Node) (*wire.Config, error) {
	if node.ParentID != nil {
		var parent model.Node
		if err := s.db.First(&parent, "id = ?", *node.ParentID).Error; err != nil {
			return nil, fmt.Errorf("resolve parent node: %w", err)
		}
		node = &parent
	}
	return nodeToConfig(node)
}

// FetchUsers returns the active, non-expired, under-quota users a node serves,
// in the wire.User shape the node expects from GET /server/users. Eligibility is
// the level tier (user.level >= node.level) by default; an explicit
// user_nodes.override grant adds users above the node's level. Because users may
// reach this server through any of its virtual child IPs, eligibility is the
// union over the node and its virtual children.
func (s *ServerService) FetchUsers(nodeID string) ([]wire.User, error) {
	// Collect the node itself plus its virtual children (id + level).
	var siblings []struct {
		ID    string `gorm:"column:id"`
		Level int    `gorm:"column:level"`
	}
	if err := s.db.Model(&model.Node{}).Select("id", "level").
		Where("id = ? OR parent_id = ?", nodeID, nodeID).
		Scan(&siblings).Error; err != nil {
		return nil, err
	}

	// Union the eligible user IDs across all siblings.
	idSet := make(map[string]struct{})
	for _, sib := range siblings {
		ids, err := s.eligibleUserIDs(sib.ID, sib.Level)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			idSet[id] = struct{}{}
		}
	}
	if len(idSet) == 0 {
		return []wire.User{}, nil
	}
	userIDs := make([]string, 0, len(idSet))
	for id := range idSet {
		userIDs = append(userIDs, id)
	}

	var users []model.User
	now := time.Now()
	err := s.db.
		Where("users.id IN ?", userIDs).
		Where("users.enabled = ?", true).
		Where("users.expire_at IS NULL OR users.expire_at > ?", now).
		Where("users.quota_bytes = 0 OR (users.up_total + users.down_total) < users.quota_bytes").
		Find(&users).Error
	if err != nil {
		return nil, err
	}
	result := make([]wire.User, 0, len(users))
	for _, u := range users {
		var exp time.Time
		if u.ExpireAt != nil {
			exp = *u.ExpireAt
		}
		result = append(result, wire.User{
			// ID is the rotatable VLESS credential (user.Credential), NOT the
			// internal primary key. A leaked credential can be regenerated
			// without disturbing the user's primary key / relationships.
			ID:       u.Credential,
			Email:    u.Email,
			Level:    u.Level,
			ExpireAt: exp,
		})
	}
	return result, nil
}

// eligibleUserIDs returns the IDs of enabled users eligible for a single node by
// level tier or explicit override. Expiry/quota filtering is applied later by
// the caller on the merged set.
func (s *ServerService) eligibleUserIDs(nodeID string, nodeLevel int) ([]string, error) {
	var ids []string
	err := s.db.Model(&model.User{}).
		Distinct("users.id").
		Where("users.enabled = ?", true).
		Where("users.level >= ? OR EXISTS (SELECT 1 FROM user_nodes un WHERE un.user_id = users.id AND un.node_id = ? AND un.override = ?)", nodeLevel, nodeID, true).
		Pluck("users.id", &ids).Error
	return ids, err
}

// ReportTraffic aggregates delta traffic into cumulative per-user and
// per-node-per-user totals. The node's last-seen timestamp (liveness) is
// refreshed centrally by the NodeAuth middleware on every successful request,
// so it is intentionally NOT updated here. Unknown/disabled emails are skipped
// with a warning (no ghost users). The whole update is transactional.
func (s *ServerService) ReportTraffic(nodeID string, deltas []wire.UserTraffic) error {
	// Resolve the traffic multiplier once. Virtual child nodes never poll, but
	// their traffic is reported against the real parent; mirror FetchConfig and
	// inherit the parent's multiplier. A missing node defaults to 1 (no change).
	mult, err := s.nodeTrafficMultiplier(nodeID)
	if err != nil {
		return err
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		for _, d := range deltas {
			if d.Up == 0 && d.Down == 0 {
				continue
			}
			// Apply the per-node traffic multiplier to the reported deltas.
			up := int64(math.Round(float64(d.Up) * mult))
			down := int64(math.Round(float64(d.Down) * mult))
			// Look up the user by email (enabled only). Email is a lookup key,
			// not a secret, so constant-time comparison does not apply here.
			var user model.User
			if err := tx.Select("id").Where("email = ? AND enabled = ?", d.Email, true).First(&user).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					log.Warnf("traffic reported for unknown/disabled email %q on node %s; skipped", d.Email, nodeID)
					continue
				}
				return fmt.Errorf("lookup user %s: %w", d.Email, err)
			}
			// Cumulative per-user totals.
			if err := tx.Model(&model.User{}).Where("id = ?", user.ID).
				Updates(map[string]any{
					"up_total":        gorm.Expr("up_total + ?", up),
					"down_total":      gorm.Expr("down_total + ?", down),
					"last_traffic_at": now,
				}).Error; err != nil {
				return fmt.Errorf("update user traffic: %w", err)
			}
			// Cumulative per-node-per-user totals (upsert on composite PK).
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "user_id"}, {Name: "node_id"}},
				DoUpdates: clause.Assignments(map[string]any{
					"up_total":   gorm.Expr("up_total + ?", up),
					"down_total": gorm.Expr("down_total + ?", down),
				}),
			}).Create(&model.UserNodeTraffic{UserID: user.ID, NodeID: nodeID, UpTotal: up, DownTotal: down}).Error; err != nil {
				return fmt.Errorf("upsert node traffic: %w", err)
			}
		}
		return nil
	})
}

// nodeTrafficMultiplier returns the effective traffic multiplier for a node.
// Virtual child nodes inherit their parent's multiplier. A multiplier <= 0 (an
// unset/legacy node) is treated as 1 so traffic is never zeroed or corrupted.
func (s *ServerService) nodeTrafficMultiplier(nodeID string) (float64, error) {
	var node model.Node
	if err := s.db.Select("parent_id", "traffic_multiplier").First(&node, "id = ?", nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 1, nil
		}
		return 0, fmt.Errorf("lookup node %s: %w", nodeID, err)
	}
	if node.ParentID != nil {
		var parent model.Node
		if err := s.db.Select("traffic_multiplier").First(&parent, "id = ?", *node.ParentID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return 1, nil
			}
			return 0, fmt.Errorf("lookup parent node: %w", err)
		}
		if parent.TrafficMultiplier <= 0 {
			return 1, nil
		}
		return parent.TrafficMultiplier, nil
	}
	if node.TrafficMultiplier <= 0 {
		return 1, nil
	}
	return node.TrafficMultiplier, nil
}

// nodeToConfig deserializes a Node's JSON config columns into the wire.Config
// shape. Network/Security/Port come from scalar columns; Settings/TLSConfig/
// RealityConfig/VLESS from JSON columns.
func nodeToConfig(node *model.Node) (*wire.Config, error) {
	cfg := &wire.Config{
		Port: node.Port,
		Stream: wire.Stream{
			Network:  node.Network,
			Security: node.Security,
		},
	}
	if len(node.Settings) > 0 {
		var settings map[string]any
		if err := json.Unmarshal(node.Settings, &settings); err != nil {
			return nil, fmt.Errorf("decode stream settings: %w", err)
		}
		cfg.Stream.Settings = settings
	}
	if node.TLSConfig != nil && len(*node.TLSConfig) > 0 {
		var tls wire.TLSConfig
		if err := json.Unmarshal(*node.TLSConfig, &tls); err != nil {
			return nil, fmt.Errorf("decode tls config: %w", err)
		}
		cfg.Stream.TLSConfig = &tls
	}
	if node.RealityConfig != nil && len(*node.RealityConfig) > 0 {
		var rc wire.RealityConfig
		if err := json.Unmarshal(*node.RealityConfig, &rc); err != nil {
			return nil, fmt.Errorf("decode reality config: %w", err)
		}
		cfg.Stream.RealityConfig = &rc
	}
	if node.VLESS != nil && len(*node.VLESS) > 0 {
		var vl wire.VLESS
		if err := json.Unmarshal(*node.VLESS, &vl); err != nil {
			return nil, fmt.Errorf("decode vless config: %w", err)
		}
		cfg.VLESS = vl
	}
	if node.Flow != nil {
		cfg.VLESS.Flow = *node.Flow
	}
	return cfg, nil
}
