package cmd

import (
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// migrations runs idempotent data migrations after AutoMigrate. It is safe to
// call on every startup.
func migrations(db *gorm.DB) {
	migrateNodesParentConstraints(db)
}

// migrateNodesParentConstraints hardens the single-table real/virtual node
// model with DB-level guarantees the app previously enforced only in code:
// no dangling parent links, no duplicate sibling addresses, and (on Postgres)
// a foreign key so parent links cannot dangle even via raw deletes. Every step
// logs and continues on failure — these are hardening constraints, not
// correctness prerequisites, so dirty legacy data must not block startup.
func migrateNodesParentConstraints(db *gorm.DB) {
	// 1. Drop orphaned virtual nodes (parent already gone) plus their user
	//    assignments, so the FK and unique index below start from clean data.
	res := db.Exec(
		"DELETE FROM user_nodes WHERE node_id IN " +
			"(SELECT id FROM nodes WHERE parent_id IS NOT NULL AND parent_id NOT IN (SELECT id FROM nodes))")
	if res.Error != nil {
		log.Warnf("migration: cleanup orphaned virtual node assignments: %v", res.Error)
	}
	res = db.Exec("DELETE FROM nodes WHERE parent_id IS NOT NULL AND parent_id NOT IN (SELECT id FROM nodes)")
	if res.Error != nil {
		log.Warnf("migration: cleanup orphaned virtual nodes: %v", res.Error)
	} else if res.RowsAffected > 0 {
		log.Warnf("migration: removed %d orphaned virtual node(s) with missing parent", res.RowsAffected)
	}

	// 2. Normalize tokens of existing virtual nodes to their own ID. Virtual
	//    nodes never authenticate (NodeAuth matches real nodes only), so a
	//    minted random token on these rows is a dead credential; the ID is a
	//    unique placeholder that is obviously not a secret.
	res = db.Exec("UPDATE nodes SET token = id WHERE parent_id IS NOT NULL AND token <> id")
	if res.Error != nil {
		log.Warnf("migration: normalize virtual node tokens: %v", res.Error)
	}

	// 3. Sibling address uniqueness: two virtual children of the same parent
	//    must not share (address, port). NULL parent_id rows (real nodes) are
	//    mutually distinct in a unique index on both Postgres and SQLite, so
	//    real nodes are unaffected. Failure means legacy duplicate rows —
	//    surface them in the log instead of crashing startup.
	if err := db.Exec(
		"CREATE UNIQUE INDEX IF NOT EXISTS uk_nodes_sibling_address ON nodes (parent_id, address, port)",
	).Error; err != nil {
		log.Warnf("migration: create uk_nodes_sibling_address (duplicate sibling addresses present?): %v", err)
	}

	// 4. Postgres only: enforce the parent link with a real FK. The app's
	//    NodeService.Delete removes children before the parent, so the cascade
	//    only fires for raw/degenerate deletes. SQLite cannot add an FK via
	//    ALTER TABLE; step 1 plus the service-level validation cover it there.
	if db.Dialector.Name() != "postgres" {
		return
	}
	err := db.Exec(`DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_nodes_parent') THEN
        ALTER TABLE nodes ADD CONSTRAINT fk_nodes_parent
            FOREIGN KEY (parent_id) REFERENCES nodes(id) ON DELETE CASCADE;
    END IF;
END $$;`).Error
	if err != nil {
		log.Warnf("migration: add fk_nodes_parent: %v", err)
	}
}
