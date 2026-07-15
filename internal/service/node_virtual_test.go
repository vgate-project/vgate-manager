package service

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/datatypes"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func vdb(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(
		&model.Node{}, &model.User{}, &model.UserNode{}, &model.UserNodeTraffic{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func vuser(t *testing.T, db *gorm.DB, email, cred string, level int) *model.User {
	t.Helper()
	u := &model.User{
		ID:         cred, // reuse cred as PK for simplicity in tests
		Credential: cred,
		Email:      email,
		SubToken:   "sub-" + cred,
		Level:      level,
		Enabled:    true,
	}
	if err := db.Create(u).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

// TestVirtualNodeSubscriptionInheritsParent verifies that a virtual child node
// appears in the subscription using its own address but the parent's transport
// config (port / security / TLS SNI).
func TestVirtualNodeSubscriptionInheritsParent(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)
	ss := NewSubscriptionService(db)

	parent := &model.Node{
		Name: "parent", Address: "parent.example.com:8443", Port: 8443,
		Network: "tcp", Security: "tls", TLSConfig: new(datatypes.JSON(`{"server_name":"example.com"}`)),
		Level: 9, Enabled: true, // high level so only the child is user-visible
	}
	if err := ns.Create(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := &model.Node{
		Name: "child-ip", Address: "1.2.3.4", ParentID: &parent.ID,
		Level: 0, Enabled: true,
	}
	if err := ns.Create(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	user := vuser(t, db, "u@example.com", "uuu", 0)
	specs, err := ss.BuildProxySpecs(user)
	if err != nil {
		t.Fatalf("BuildProxySpecs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("want 1 spec (child only), got %d", len(specs))
	}
	s := specs[0]
	if s.Address != "1.2.3.4" {
		t.Errorf("Address = %q, want child address 1.2.3.4", s.Address)
	}
	if s.Host != "1.2.3.4" {
		t.Errorf("Host = %q, want 1.2.3.4", s.Host)
	}
	if s.Port != 8443 {
		t.Errorf("Port = %d, want parent port 8443", s.Port)
	}
	if s.Security != "tls" {
		t.Errorf("Security = %q, want inherited tls", s.Security)
	}
	if s.SNI != "example.com" {
		t.Errorf("SNI = %q, want inherited example.com", s.SNI)
	}
}

// TestVirtualNodeCustomPort verifies that a non-zero child port overrides the
// inherited parent port in the subscription, while 0 means inherit.
func TestVirtualNodeCustomPort(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)
	ss := NewSubscriptionService(db)

	parent := &model.Node{
		Name: "parent", Address: "parent.example.com:8443", Port: 8443,
		Network: "tcp", Security: "none", Level: 9, Enabled: true,
	}
	if err := ns.Create(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := &model.Node{
		Name: "child-ip", Address: "1.2.3.4", ParentID: &parent.ID,
		Port:  2053, // custom override
		Level: 0, Enabled: true,
	}
	if err := ns.Create(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	user := vuser(t, db, "u@example.com", "uuu", 0)
	specs, err := ss.BuildProxySpecs(user)
	if err != nil {
		t.Fatalf("BuildProxySpecs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("want 1 spec, got %d", len(specs))
	}
	if specs[0].Port != 2053 {
		t.Errorf("Port = %d, want custom override 2053", specs[0].Port)
	}
}

// TestFetchUsersIncludesChildEligibleUsers verifies that the parent server's
// user list is the union over the parent and its virtual children, so a user
// granted only via a child IP can still connect to the parent server.
func TestFetchUsersIncludesChildEligibleUsers(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)
	ss := NewServerService(db)

	parent := &model.Node{
		Name: "parent", Address: "p:443", Port: 443, Network: "tcp",
		Security: "none", Level: 10, Enabled: true,
	}
	if err := ns.Create(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := &model.Node{
		Name: "child", Address: "1.2.3.4", ParentID: &parent.ID,
		Level: 0, Enabled: true,
	}
	if err := ns.Create(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	// userA is only eligible via the child (level 0); userB via both.
	vuser(t, db, "a@example.com", "aaa", 0)
	vuser(t, db, "b@example.com", "bbb", 20)

	users, err := ss.FetchUsers(parent.ID)
	if err != nil {
		t.Fatalf("FetchUsers: %v", err)
	}
	got := map[string]bool{}
	for _, u := range users {
		got[u.Email] = true
	}
	if !got["a@example.com"] {
		t.Errorf("userA (child-only eligible) missing from parent FetchUsers")
	}
	if !got["b@example.com"] {
		t.Errorf("userB missing from parent FetchUsers")
	}
}

// TestDeleteCascadesVirtualChildren verifies that deleting a parent also
// removes its virtual children and their user assignments.
func TestDeleteCascadesVirtualChildren(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)

	parent := &model.Node{
		Name: "parent", Address: "p:443", Port: 443, Network: "tcp",
		Security: "none", Level: 0, Enabled: true,
	}
	if err := ns.Create(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	c1 := &model.Node{Name: "c1", Address: "1.1.1.1", ParentID: &parent.ID, Level: 0, Enabled: true}
	c2 := &model.Node{Name: "c2", Address: "2.2.2.2", ParentID: &parent.ID, Level: 0, Enabled: true}
	if err := ns.Create(c1); err != nil {
		t.Fatalf("create c1: %v", err)
	}
	if err := ns.Create(c2); err != nil {
		t.Fatalf("create c2: %v", err)
	}
	// Attach a user to c1 so we can verify the user_nodes row is cascaded.
	u := vuser(t, db, "u@example.com", "uuu", 0)
	if err := db.Create(&model.UserNode{UserID: u.ID, NodeID: c1.ID, Override: true}).Error; err != nil {
		t.Fatalf("seed user_node: %v", err)
	}

	if err := ns.Delete(parent.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	var n, un int64
	db.Model(&model.Node{}).Count(&n)
	db.Model(&model.UserNode{}).Count(&un)
	if n != 0 {
		t.Errorf("nodes remaining after cascade delete: %d, want 0", n)
	}
	if un != 0 {
		t.Errorf("user_nodes remaining after cascade delete: %d, want 0", un)
	}
}

// TestVirtualNodeOnlineReflectsParent verifies that a virtual node's online
// state is derived from its parent's last-seen timestamp.
func TestVirtualNodeOnlineReflectsParent(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)

	parent := &model.Node{
		Name: "parent", Address: "p:443", Port: 443, Network: "tcp",
		Security: "none", Level: 0, Enabled: true,
	}
	if err := ns.Create(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	now := time.Now()
	if err := db.Model(&model.Node{}).Where("id = ?", parent.ID).Update("last_seen_at", now).Error; err != nil {
		t.Fatalf("set last_seen: %v", err)
	}
	child := &model.Node{Name: "c", Address: "1.1.1.1", ParentID: &parent.ID, Level: 0, Enabled: true}
	if err := ns.Create(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	got, err := ns.Get(child.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Online {
		t.Errorf("virtual child Online = false, want true (parent recently seen)")
	}

	nodes, _, err := ns.List(1, 20)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, n := range nodes {
		if n.ID == child.ID && !n.Online {
			t.Errorf("List: virtual child Online = false, want true")
		}
	}
}

// TestVirtualNodeOnlineInUserList verifies that ListNodesForUser derives a
// virtual child's online state from its parent's last-seen timestamp, not the
// child's own (always-nil) LastSeenAt. This guards the user-facing
// GET /api/v1/user/nodes path, which previously reported virtual nodes as
// permanently offline (regression for missing hydrateVirtualOnline).
func TestVirtualNodeOnlineInUserList(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)
	us := NewUserService(db, nil)

	parent := &model.Node{
		Name: "parent", Address: "p:443", Port: 443, Network: "tcp",
		Security: "none", Level: 0, Enabled: true,
	}
	if err := ns.Create(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	// Mark the parent as just-seen so it (and its child) should read online.
	if err := db.Model(&model.Node{}).Where("id = ?", parent.ID).
		Update("last_seen_at", time.Now()).Error; err != nil {
		t.Fatalf("set last_seen: %v", err)
	}
	child := &model.Node{Name: "c", Address: "1.1.1.1", ParentID: &parent.ID, Level: 0, Enabled: true}
	if err := ns.Create(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	user := vuser(t, db, "u@example.com", "uuu", 0)
	nodes, err := us.ListNodesForUser(user.ID)
	if err != nil {
		t.Fatalf("ListNodesForUser: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("want 2 nodes (parent + child), got %d", len(nodes))
	}
	for _, n := range nodes {
		if n.ID == child.ID && !n.IsOnline() {
			t.Errorf("virtual child IsOnline() = false, want true (parent recently seen)")
		}
	}
}

// TestVirtualNodeValidation verifies virtual-node-specific validation: name and
// address are required, and a virtual node cannot be the parent of another.
func TestVirtualNodeValidation(t *testing.T) {
	db := vdb(t)
	ns := NewNodeService(db)

	parent := &model.Node{Name: "parent", Address: "p:443", Port: 443, Network: "tcp", Security: "none", Level: 0, Enabled: true}
	if err := ns.Create(parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	// address required
	noAddr := &model.Node{Name: "x", ParentID: &parent.ID, Level: 0, Enabled: true}
	if err := ns.Create(noAddr); err == nil {
		t.Errorf("expected error for virtual node with empty address")
	}

	// valid virtual child
	child := &model.Node{Name: "c", Address: "1.1.1.1", ParentID: &parent.ID, Level: 0, Enabled: true}
	if err := ns.Create(child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	// child-of-child must be rejected (no nested virtual nodes)
	grandchild := &model.Node{Name: "g", Address: "2.2.2.2", ParentID: &child.ID, Level: 0, Enabled: true}
	if err := ns.Create(grandchild); err == nil {
		t.Errorf("expected error creating a virtual node whose parent is itself virtual")
	}
}
