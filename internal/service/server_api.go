// Package service contains the manager's business logic. The ServerService
// implements the three server-facing endpoints consumed by vgate nodes.
package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
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
// server of its own, so if one is passed we resolve its parent's config. For a
// real node the config also carries the sid → virtual child mapping so the
// node can attribute reported traffic to the entry point the client used.
func (s *ServerService) FetchConfig(node *model.Node) (*wire.Config, error) {
	if node.ParentID != nil {
		var parent model.Node
		if err := s.db.First(&parent, "id = ?", *node.ParentID).Error; err != nil {
			return nil, fmt.Errorf("resolve parent node: %w", err)
		}
		node = &parent
	}
	cfg, err := nodeToConfig(node)
	if err != nil {
		return nil, err
	}
	var children []model.Node
	if err := s.db.Select("id", "reality_sid").
		Where("parent_id = ? AND reality_sid <> ?", node.ID, "").
		Find(&children).Error; err != nil {
		return nil, fmt.Errorf("load child short ids: %w", err)
	}
	if len(children) > 0 {
		cfg.TrafficSIDs = make([]wire.TrafficSID, 0, len(children))
		for _, c := range children {
			cfg.TrafficSIDs = append(cfg.TrafficSIDs, wire.TrafficSID{NodeID: c.ID, SID: c.RealitySID})
		}
	}
	// The delivered whitelist combines every entry point's short ID — the real
	// node's own first, then its legacy stored entries, then each virtual
	// child's. One sid per node; the server accepts them all and the
	// traffic_sids map above attributes each to its entry point.
	if cfg.Stream.Security == "reality" && cfg.Stream.RealityConfig != nil {
		seen := make(map[string]bool)
		combined := make([]string, 0, len(children)+1)
		add := func(sid string) {
			if sid != "" && !seen[sid] {
				seen[sid] = true
				combined = append(combined, sid)
			}
		}
		add(node.RealitySID) // the node's own first
		for _, sid := range cfg.Stream.RealityConfig.ShortIds {
			add(sid) // legacy stored entries
		}
		for _, c := range children {
			add(c.RealitySID)
		}
		cfg.Stream.RealityConfig.ShortIds = combined
	}
	return cfg, nil
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
		Where("users.email_verified = ?", true).
		Where("users.expire_at IS NULL OR users.expire_at > ?", now).
		Where("users.quota_bytes = -1 OR (users.up_total + users.down_total) < (users.quota_bytes + COALESCE(users.traffic_quota_bytes, 0))").
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
			// Per-user speed limits (bytes/sec, 0 = unlimited). The node
			// applies min(node global limit, this limit).
			SpeedLimitUpBps:   u.SpeedLimitUpBps,
			SpeedLimitDownBps: u.SpeedLimitDownBps,
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
		Where("users.email_verified = ?", true).
		Where("users.level >= ? OR EXISTS (SELECT 1 FROM user_nodes un WHERE un.user_id = users.id AND un.node_id = ? AND un.override = ?)", nodeLevel, nodeID, true).
		Pluck("users.id", &ids).Error
	return ids, err
}

// ReportTraffic aggregates delta traffic into cumulative per-user and
// per-node-per-user totals, and into per-user hourly deltas in
// traffic_hourly_stat (used by the dashboard traffic series). The node's
// last-seen timestamp (liveness) is refreshed centrally by the NodeAuth
// middleware on every successful request, so it is intentionally NOT updated
// here. Unknown/disabled emails are skipped with a warning (no ghost users).
// The whole update is transactional.
//
// The per-node traffic_multiplier scales the bytes reported by a node's users
// "for billing" (model.Node.TrafficMultiplier): each entry point (a real node
// or one of its virtual children) uses its own stored multiplier, so the
// CUMULATIVE totals (users.up_total/down_total and user_node_traffic) are
// written multiplied.
// The per-node-per-user hourly delta in traffic_hourly_stat is written
// UN-MULTIPLIED (the real reported bytes) so the dashboard 24h series / hourly
// chart reflects actual traffic rather than the billing-inflated figure.
func (s *ServerService) ReportTraffic(nodeID string, deltas []wire.UserTraffic) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		now := time.Now()
		hour := now.UTC().Truncate(time.Hour)
		// statAgg merges the batch's raw (un-multiplied) deltas per
		// (user, entry point) before the single upsert below, so multiple
		// deltas for the same key in one batch accumulate instead of
		// colliding in the ON CONFLICT upsert.
		statAgg := make(map[string]*model.TrafficHourlyStat)
		mults := make(map[string]float64) // resolved node id → multiplier cache
		for _, d := range deltas {
			if d.Up == 0 && d.Down == 0 {
				continue
			}
			// Resolve the entry point this delta belongs to (the reporting node
			// itself or one of its virtual children, matched by Reality short
			// ID on the node side) and its effective traffic multiplier.
			targetNodeID, err := resolveTrafficNode(tx, nodeID, d.NodeID)
			if err != nil {
				return err
			}
			mult, ok := mults[targetNodeID]
			if !ok {
				if mult, err = nodeTrafficMultiplier(tx, targetNodeID); err != nil {
					return err
				}
				mults[targetNodeID] = mult
			}
			// Apply the per-node traffic multiplier to the reported deltas.
			up := int64(math.Round(float64(d.Up) * mult))
			down := int64(math.Round(float64(d.Down) * mult))
			// Look up the user by email (enabled only). Email is a lookup key,
			// not a secret, so constant-time comparison does not apply here.
			// Normalize to lowercase so it matches the stored (lower-cased)
			// email regardless of how the node reports it.
			var user model.User
			if err := tx.Select("id").Where("email = ? AND enabled = ?", strings.ToLower(strings.TrimSpace(d.Email)), true).First(&user).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					log.Warnf("traffic reported for unknown/disabled email %q on node %s; skipped", d.Email, nodeID)
					continue
				}
				return fmt.Errorf("lookup user %s: %w", d.Email, err)
			}

			// Package-first deduction: charge traffic to the user's active
			// grants (FIFO by GrantedAt) before the base plan quota. The grants'
			// used_bytes and the user's package_used_bytes absorb the package
			// portion; anything beyond the total grant capacity stays as base
			// usage (handled by the up_total/down_total increment below).
			delta := up + down
			pkgConsumed := int64(0)
			if delta > 0 {
				var grants []model.TrafficGrant
				if err := tx.Where("user_id = ?", user.ID).
					Order("granted_at ASC").
					Find(&grants).Error; err != nil {
					return fmt.Errorf("load traffic grants for %s: %w", user.ID, err)
				}
				room := int64(0)
				for i := range grants {
					room += grants[i].RemainingBytes()
				}
				consume := delta
				if consume > room {
					consume = room
				}
				if consume > 0 {
					remaining := consume
					for i := range grants {
						if remaining <= 0 {
							break
						}
						take := grants[i].RemainingBytes()
						if take > remaining {
							take = remaining
						}
						if take > 0 {
							if err := tx.Model(&model.TrafficGrant{}).
								Where("id = ?", grants[i].ID).
								Update("used_bytes", gorm.Expr("used_bytes + ?", take)).Error; err != nil {
								return fmt.Errorf("update grant used for %s: %w", grants[i].ID, err)
							}
							remaining -= take
						}
					}
					pkgConsumed = consume - remaining
				}
			}

			// Cumulative per-user totals. up_total/down_total track the global
			// combined usage (base + package) for the node cap and display;
			// package_used_bytes tracks only the package portion so a base
			// reset can preserve it.
			if err := tx.Model(&model.User{}).Where("id = ?", user.ID).
				Updates(map[string]any{
					"up_total":           gorm.Expr("up_total + ?", up),
					"down_total":         gorm.Expr("down_total + ?", down),
					"package_used_bytes": gorm.Expr("package_used_bytes + ?", pkgConsumed),
					"last_traffic_at":    now,
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
			}).Create(&model.UserNodeTraffic{UserID: user.ID, NodeID: targetNodeID, UpTotal: up, DownTotal: down}).Error; err != nil {
				return fmt.Errorf("upsert node traffic: %w", err)
			}
			// Per-user-per-node hourly delta for the dashboard traffic series.
			// Written UN-MULTIPLIED (the real reported bytes) so the time-series
			// chart reflects actual traffic; the cumulative totals above are the
			// multiplied (billing) figures. Merged per (user, node) within the
			// batch; the upsert below accumulates across concurrent reports.
			statKey := user.ID + "\x00" + targetNodeID
			if agg, ok := statAgg[statKey]; ok {
				agg.UpTotal += d.Up
				agg.DownTotal += d.Down
			} else {
				statAgg[statKey] = &model.TrafficHourlyStat{
					UserID:    user.ID,
					NodeID:    targetNodeID,
					Hour:      hour,
					UpTotal:   d.Up,
					DownTotal: d.Down,
				}
			}
		}

		// Additively upsert all per-user-per-node hourly deltas in one statement.
		if len(statAgg) > 0 {
			statRows := make([]model.TrafficHourlyStat, 0, len(statAgg))
			for _, agg := range statAgg {
				statRows = append(statRows, *agg)
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "user_id"}, {Name: "node_id"}, {Name: "hour"}},
				DoUpdates: clause.Assignments(map[string]any{
					"up_total":   gorm.Expr("up_total + EXCLUDED.up_total"),
					"down_total": gorm.Expr("down_total + EXCLUDED.down_total"),
				}),
			}).Create(&statRows).Error; err != nil {
				return fmt.Errorf("upsert hourly stat: %w", err)
			}
		}
		return nil
	})
}

// resolveTrafficNode maps a reported node_id onto an entry point of the
// authenticated real node. An empty value falls back to the reporting node
// itself (legacy agents, non-reality traffic, or the ws/xhttp xraybridge path
// where the short ID never surfaces). A claimed id is accepted only when it is
// the reporting node or a direct virtual child of it, so a buggy or hostile
// agent cannot attribute traffic to arbitrary nodes.
func resolveTrafficNode(db *gorm.DB, nodeID, claimed string) (string, error) {
	claimed = strings.TrimSpace(claimed)
	if claimed == "" || claimed == nodeID {
		return nodeID, nil
	}
	var child model.Node
	if err := db.Select("id").First(&child, "id = ? AND parent_id = ?", claimed, nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			log.Warnf("traffic reported for unknown node %q on node %s; attributed to %s", claimed, nodeID, nodeID)
			return nodeID, nil
		}
		return "", fmt.Errorf("resolve traffic node %q: %w", claimed, err)
	}
	return child.ID, nil
}

// nodeTrafficMultiplier returns the traffic multiplier stored on the node
// itself. Real nodes and virtual children each carry their own multiplier —
// virtual children no longer inherit their parent's. A multiplier <= 0 (an
// unset/legacy node) is treated as 1 so traffic is never zeroed or corrupted.
func nodeTrafficMultiplier(db *gorm.DB, nodeID string) (float64, error) {
	var node model.Node
	if err := db.Select("traffic_multiplier").First(&node, "id = ?", nodeID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 1, nil
		}
		return 0, fmt.Errorf("lookup node %s: %w", nodeID, err)
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
	cfg.SpeedLimitUpBps = node.SpeedLimitUpBps
	cfg.SpeedLimitDownBps = node.SpeedLimitDownBps
	return cfg, nil
}
