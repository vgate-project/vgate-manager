package service

import (
	"fmt"
	"testing"
	"time"

	"github.com/vgate-project/vgate-manager/internal/model"
)

// TestStatsNodeCountsExcludesVirtualNodes verifies that the overview's node
// counts cover real nodes only: virtual children never poll and have no data
// of their own, so they must not inflate node_count / node_online even when
// their parent is online.
func TestStatsNodeCountsExcludesVirtualNodes(t *testing.T) {
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
			Name: fmt.Sprintf("virtual%d", i), Address: fmt.Sprintf("10.0.0.%d", i+1),
			Enabled:  true,
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
	if total != 1 {
		t.Errorf("total = %d, want 1 (virtual children excluded)", total)
	}
	if online != 1 {
		t.Errorf("online = %d, want 1 (real node only)", online)
	}

	// When the real node goes stale, the counts still track it alone.
	if err := db.Model(&model.Node{}).Where("id = ?", real.ID).
		Update("last_seen_at", time.Now().Add(-time.Hour)).Error; err != nil {
		t.Fatalf("stale parent: %v", err)
	}
	total, online, err = ss.nodeCounts()
	if err != nil {
		t.Fatalf("nodeCounts: %v", err)
	}
	if total != 1 {
		t.Errorf("total = %d, want 1", total)
	}
	if online != 0 {
		t.Errorf("online = %d, want 0 (real node stale)", online)
	}
}
