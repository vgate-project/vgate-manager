package cmd

import (
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/vgate-project/vgate-manager/internal/model"
)

func migrateDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&model.Node{}, &model.UserNode{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestMigrationsOrphanCleanupAndTokenNormalize verifies the data-cleanup half
// of migrateNodesParentConstraints on SQLite: orphaned virtual nodes (parent
// already gone) are removed along with their user assignments, and surviving
// virtual rows get their token normalized to the ID placeholder.
func TestMigrationsOrphanCleanupAndTokenNormalize(t *testing.T) {
	db := migrateDB(t)

	parent := &model.Node{ID: "parent-id-01", Name: "parent", Token: "secret-01", Address: "p:443", Port: 443, Security: "none", Enabled: true}
	child := &model.Node{ID: "child-id-001", Name: "child", Token: "child-id-001", Address: "1.1.1.1", ParentID: &parent.ID, Security: "none", Enabled: true}
	orphan := &model.Node{ID: "orphan-id-01", Name: "orphan", Token: "orphan-secret", Address: "9.9.9.9", Security: "none", Enabled: true}
	missing := "gone-id-0001"
	orphan.ParentID = &missing
	for _, n := range []*model.Node{parent, child, orphan} {
		if err := db.Create(n).Error; err != nil {
			t.Fatalf("seed node %s: %v", n.Name, err)
		}
	}
	un := &model.UserNode{UserID: "user-id-0001", NodeID: orphan.ID, Override: true}
	if err := db.Create(un).Error; err != nil {
		t.Fatalf("seed user_node: %v", err)
	}

	migrations(db)

	var n int64
	db.Model(&model.Node{}).Where("id = ?", orphan.ID).Count(&n)
	if n != 0 {
		t.Errorf("orphaned virtual node still present after migration")
	}
	db.Model(&model.UserNode{}).Where("node_id = ?", orphan.ID).Count(&n)
	if n != 0 {
		t.Errorf("orphaned virtual node's user_nodes still present after migration")
	}
	var got model.Node
	if err := db.First(&got, "id = ?", child.ID).Error; err != nil {
		t.Fatalf("surviving child missing: %v", err)
	}
	if got.Token != child.ID {
		t.Errorf("surviving virtual child token = %q, want normalized ID %q", got.Token, child.ID)
	}
	if err := db.First(&model.Node{}, "id = ?", parent.ID).Error; err != nil {
		t.Fatalf("real parent must survive: %v", err)
	}
}

// TestMigrationsSiblingAddressUnique verifies the unique index semantics: two
// virtual siblings may not share (address, port), siblings differing in port
// are allowed, and real nodes (NULL parent) never collide.
func TestMigrationsSiblingAddressUnique(t *testing.T) {
	db := migrateDB(t)
	migrations(db)

	parent := &model.Node{ID: "parent-id-01", Name: "parent", Token: "secret-01", Address: "p:443", Port: 443, Security: "none", Enabled: true}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	sib := func(id, name, addr string, port int) *model.Node {
		return &model.Node{ID: id, Name: name, Token: id, Address: addr, Port: port, ParentID: &parent.ID, Security: "none", Enabled: true}
	}
	if err := db.Create(sib("sib-a-id-001", "a", "1.1.1.1", 0)).Error; err != nil {
		t.Fatalf("seed sibling a: %v", err)
	}
	// Same address, same port (0) — must be rejected by the index.
	if err := db.Create(sib("sib-b-id-001", "b", "1.1.1.1", 0)).Error; err == nil {
		t.Errorf("expected duplicate sibling (address,port) to be rejected")
	}
	// Same address, different port — legitimate NAT override, must pass.
	if err := db.Create(sib("sib-c-id-001", "c", "1.1.1.1", 2053)).Error; err != nil {
		t.Fatalf("sibling with distinct port must be allowed: %v", err)
	}
	// Real nodes (parent_id NULL) share addresses freely.
	for i, name := range []string{"r1", "r2"} {
		rn := &model.Node{ID: name + "-id-0001", Name: name, Token: "tok-" + name, Address: "real.example.com", Port: 443, Security: "none", Enabled: true}
		if i == 1 {
			rn.Token = "tok-r2x"
		}
		if err := db.Create(rn).Error; err != nil {
			t.Fatalf("real node %s must be allowed to share address: %v", name, err)
		}
	}
}

// TestMigrationsIdempotent verifies migrations can run repeatedly without
// error or duplicate-constraint failures (startup runs them every time).
func TestMigrationsIdempotent(t *testing.T) {
	db := migrateDB(t)
	parent := &model.Node{ID: "parent-id-01", Name: "parent", Token: "secret-01", Address: "p:443", Port: 443, Security: "none", Enabled: true}
	if err := db.Create(parent).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	for i := 0; i < 3; i++ {
		migrations(db) // must not panic or fail
	}
	var n int64
	db.Model(&model.Node{}).Count(&n)
	if n != 1 {
		t.Errorf("node count after repeated migrations = %d, want 1", n)
	}
}
