package cmd

import (
	"fmt"
	"strings"

	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

// migrations runs idempotent data migrations after AutoMigrate. It is safe to
// call on every startup.
func migrations(db *gorm.DB) {
	migrateNodesParentConstraints(db)
	migrateTrafficHourlyStatNodeID(db)
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

// migrateTrafficHourlyStatNodeID upgrades traffic_hourly_stats from a
// (user_id, hour) primary key to (user_id, node_id, hour) so the dashboard
// series can be attributed per entry point (real node or virtual child).
// AutoMigrate creates the new shape on fresh databases but cannot re-create
// the primary key on existing ones, so both dialects are handled explicitly
// here. Rows written before the node dimension existed keep node_id = '' (the
// "unattributed" bucket): they count toward all-node totals but toward no
// individual node. Idempotent; every step logs and continues on failure.
func migrateTrafficHourlyStatNodeID(db *gorm.DB) {
	const table = "traffic_hourly_stats"

	// 1. Add the node_id column when missing ('' = legacy unattributed rows).
	if db.Dialector.Name() == "postgres" {
		if err := db.Exec(
			`ALTER TABLE traffic_hourly_stats ADD COLUMN IF NOT EXISTS node_id VARCHAR(26) NOT NULL DEFAULT ''`,
		).Error; err != nil {
			log.Warnf("migration: add %s.node_id: %v", table, err)
			return
		}
	} else {
		// Probe via table_info — cheaper and more explicit than relying on
		// error-on-alter to detect the column.
		var cols []struct{ Name string }
		if err := db.Raw("PRAGMA table_info(traffic_hourly_stats)").Scan(&cols).Error; err != nil {
			log.Warnf("migration: probe %s columns: %v", table, err)
			return
		}
		hasNodeID := false
		for _, c := range cols {
			if c.Name == "node_id" {
				hasNodeID = true
				break
			}
		}
		if !hasNodeID {
			if err := db.Exec(
				`ALTER TABLE traffic_hourly_stats ADD COLUMN node_id TEXT NOT NULL DEFAULT ''`,
			).Error; err != nil {
				log.Warnf("migration: add %s.node_id: %v", table, err)
				return
			}
		}
	}

	// 2. Defensive backfill: a pre-existing nullable node_id may hold NULLs;
	//    fold them into the '' unattributed bucket.
	if err := db.Exec(`UPDATE traffic_hourly_stats SET node_id = '' WHERE node_id IS NULL`).Error; err != nil {
		log.Warnf("migration: backfill %s.node_id: %v", table, err)
	}

	// 3. Ensure the primary key covers (user_id, node_id, hour).
	if db.Dialector.Name() == "postgres" {
		var pk struct {
			Name string
			Def  string
		}
		if err := db.Raw(`
			SELECT con.conname AS name, pg_get_constraintdef(con.oid) AS def
			FROM pg_constraint con
			JOIN pg_class c ON c.oid = con.conrelid
			WHERE c.relname = 'traffic_hourly_stats' AND con.contype = 'p'
		`).Scan(&pk).Error; err != nil {
			log.Warnf("migration: inspect %s PK: %v", table, err)
			return
		}
		if strings.Contains(pk.Def, "node_id") {
			return // already (user_id, node_id, hour)
		}
		if err := db.Exec(fmt.Sprintf(
			`ALTER TABLE traffic_hourly_stats DROP CONSTRAINT %s, ADD PRIMARY KEY (user_id, node_id, hour)`,
			pk.Name,
		)).Error; err != nil {
			log.Warnf("migration: rebuild %s PK: %v", table, err)
		}
		return
	}

	// SQLite cannot ALTER a primary key: rebuild the table (rename, create,
	// copy, drop) inside one transaction — DDL is transactional in SQLite, so
	// a failure mid-way rolls back to the original table untouched.
	var pkCols string
	if err := db.Raw(`
		SELECT group_concat(name, ',') FROM (
			SELECT name FROM pragma_table_info('traffic_hourly_stats') WHERE pk > 0 ORDER BY pk
		)
	`).Scan(&pkCols).Error; err != nil {
		log.Warnf("migration: inspect %s PK (sqlite): %v", table, err)
		return
	}
	if pkCols == "user_id,node_id,hour" {
		return // already migrated
	}
	err := db.Transaction(func(tx *gorm.DB) error {
		steps := []string{
			`ALTER TABLE traffic_hourly_stats RENAME TO traffic_hourly_stats_old`,
			// Column types/order mirror what AutoMigrate builds for the model,
			// so the next startup's AutoMigrate is a no-op.
			"CREATE TABLE `traffic_hourly_stats` (`user_id` text,`node_id` text,`hour` datetime,`up_total` integer DEFAULT 0,`down_total` integer DEFAULT 0,`created_at` datetime,PRIMARY KEY (`user_id`,`node_id`,`hour`))",
			`INSERT INTO traffic_hourly_stats (user_id, node_id, hour, up_total, down_total, created_at)
			 SELECT user_id, COALESCE(node_id, ''), hour, up_total, down_total, created_at FROM traffic_hourly_stats_old`,
			`DROP TABLE traffic_hourly_stats_old`,
		}
		for _, s := range steps {
			if err := tx.Exec(s).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Warnf("migration: rebuild %s PK (sqlite): %v", table, err)
		return
	}
	log.Info("migration: traffic_hourly_stats now keyed by (user_id, node_id, hour)")
}
