package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// TestStatsNodeCountsIncludesVirtualChildren verifies that the overview's
// node_online count attributes a virtual child's liveness to its parent:
// virtual nodes never poll, so they must be online exactly when their real
// parent is (and offline when the parent is missing or stale).
func TestStatsNodeCountsIncludesVirtualChildren(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)
	ss := NewStatsService(db)

	real := &model.Node{
		Name: "real", Address: "real.example.com:8443", Port: 8443,
		Network: "tcp", Security: "none", Enabled: true,
	}
	if err := ns.Create(real); err != nil {
		t.Fatalf("create real: %v", err)
	}
	// Mark the real node as recently seen (online).
	if err := db.Model(&model.Node{}).Where("id = ?", real.ID).
		Update("last_seen_at", time.Now()).Error; err != nil {
		t.Fatalf("set last_seen: %v", err)
	}
	for i := 0; i < 2; i++ {
		child := &model.Node{
			Name: fmt.Sprintf("virtual%d", i), Address: "real.example.com:8443", Port: 8443,
			Network: "tcp", Security: "none", Enabled: true,
			ParentID: &real.ID,
		}
		if err := ns.Create(child); err != nil {
			t.Fatalf("create virtual: %v", err)
		}
	}

	total, online, err := ss.nodeCounts()
	if err != nil {
		t.Fatalf("nodeCounts: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if online != 3 {
		t.Errorf("online = %d, want 3 (real + 2 virtuals inherit parent liveness)", online)
	}

	// When the parent goes stale, virtual children must go offline too.
	if err := db.Model(&model.Node{}).Where("id = ?", real.ID).
		Update("last_seen_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("stale parent: %v", err)
	}
	total, online, err = ss.nodeCounts()
	if err != nil {
		t.Fatalf("nodeCounts: %v", err)
	}
	if total != 3 {
		t.Errorf("total = %d, want 3", total)
	}
	if online != 0 {
		t.Errorf("online = %d, want 0 (parent stale => virtuals offline)", online)
	}
}
