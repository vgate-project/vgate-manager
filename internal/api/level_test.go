package api_test

import (
	"strings"
	"testing"

	"github.com/vgate-project/vgate-manager/internal/model"
	"github.com/vgate-project/vgate-manager/internal/service"
)

// TestNodeUserLevelGate verifies the access rule:
//   - a user can use an assigned node whose level <= user.level
//   - an admin may assign a higher-level node (SetUserNodes auto-sets Override)
//   - an assigned node whose level exceeds the user's AND has no override is
//     excluded from both the user's view and the node's user fetch.
func TestNodeUserLevelGate(t *testing.T) {
	db := setupTestDB(t)

	user := model.User{ID: "u-level", Email: "lvl@example.com", SubToken: "lvltok", Level: 1, Enabled: true}
	if err := db.Create(&user).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}

	// level 0 (within), level 5 (above user), level 9 (way above user).
	nIn := model.Node{ID: "n-in", Name: "in", Address: "in:443", Port: 443, Network: "tcp", Security: "none", Token: "tok-in", Level: 0, Enabled: true}
	nHigh := model.Node{ID: "n-high", Name: "high", Address: "high:443", Port: 443, Network: "tcp", Security: "none", Token: "tok-high", Level: 5, Enabled: true}
	nVH := model.Node{ID: "n-vh", Name: "vh", Address: "vh:443", Port: 443, Network: "tcp", Security: "none", Token: "tok-vh", Level: 9, Enabled: true}
	// level 0 (within) but intentionally NOT assigned via user_nodes — proves the
	// level tier grants access without any assignment row.
	nInFree := model.Node{ID: "n-in-free", Name: "infree", Address: "infree:443", Port: 443, Network: "tcp", Security: "none", Token: "tok-infree", Level: 0, Enabled: true}
	for _, n := range []model.Node{nIn, nHigh, nVH, nInFree} {
		if err := db.Create(&n).Error; err != nil {
			t.Fatalf("create node %s: %v", n.ID, err)
		}
	}

	userSvc := service.NewUserService(db, nil)
	subSvc := service.NewSubscriptionService(db)
	serverSvc := service.NewServerService(db)

	// Admin assigns n-in (within) and n-high (above) via SetUserNodes; the
	// higher one should get Override auto-set.
	if err := userSvc.SetUserNodes(user.ID, []string{nIn.ID, nHigh.ID}); err != nil {
		t.Fatalf("SetUserNodes: %v", err)
	}
	var unHigh model.UserNode
	if err := db.Where("user_id = ? AND node_id = ?", user.ID, nHigh.ID).First(&unHigh).Error; err != nil {
		t.Fatalf("load assignment: %v", err)
	}
	if !unHigh.Override {
		t.Errorf("expected Override=true for high node assigned to low-level user")
	}

	// A high node assigned WITHOUT override (simulates a legacy/explicit row).
	if err := db.Create(&model.UserNode{UserID: user.ID, NodeID: nVH.ID, Override: false}).Error; err != nil {
		t.Fatalf("seed non-override assignment: %v", err)
	}

	// --- Subscription (user → nodes) ---
	links, err := subSvc.BuildLinks(&user)
	if err != nil {
		t.Fatalf("BuildLinks: %v", err)
	}
	joined := strings.Join(links, "\n")
	if !strings.Contains(joined, nIn.Address) {
		t.Errorf("within-level node %s missing from subscription: %v", nIn.ID, links)
	}
	if !strings.Contains(joined, nInFree.Address) {
		t.Errorf("within-level unassigned node %s should be included by level tier: %v", nInFree.ID, links)
	}
	if !strings.Contains(joined, nHigh.Address) {
		t.Errorf("override node %s missing from subscription: %v", nHigh.ID, links)
	}
	if strings.Contains(joined, nVH.Address) {
		t.Errorf("above-level non-override node %s should be excluded: %v", nVH.ID, links)
	}

	// --- ListNodesForUser ---
	nodes, err := userSvc.ListNodesForUser(user.ID)
	if err != nil {
		t.Fatalf("ListNodesForUser: %v", err)
	}
	got := map[string]bool{}
	for _, n := range nodes {
		got[n.ID] = true
	}
	if !got[nIn.ID] || !got[nHigh.ID] || !got[nInFree.ID] {
		t.Errorf("ListNodesForUser missing expected nodes: %v", got)
	}
	if got[nVH.ID] {
		t.Errorf("ListNodesForUser should exclude non-override high node %s", nVH.ID)
	}

	// --- FetchUsers (node → users) ---
	highUsers, err := serverSvc.FetchUsers(nHigh.ID)
	if err != nil {
		t.Fatalf("FetchUsers high: %v", err)
	}
	if len(highUsers) != 1 || highUsers[0].Email != user.Email {
		t.Errorf("FetchUsers(%s) expected the user via override, got %v", nHigh.ID, highUsers)
	}

	// A within-level node with NO assignment row must still fetch the user via
	// the level tier (proves user_nodes is not a mandatory gate).
	freeUsers, err := serverSvc.FetchUsers(nInFree.ID)
	if err != nil {
		t.Fatalf("FetchUsers infree: %v", err)
	}
	if len(freeUsers) != 1 || freeUsers[0].Email != user.Email {
		t.Errorf("FetchUsers(%s) expected the user via level tier (no assignment), got %v", nInFree.ID, freeUsers)
	}

	vhUsers, err := serverSvc.FetchUsers(nVH.ID)
	if err != nil {
		t.Fatalf("FetchUsers vh: %v", err)
	}
	if len(vhUsers) != 0 {
		t.Errorf("FetchUsers(%s) expected 0 users (above level, no override), got %d", nVH.ID, len(vhUsers))
	}
}
