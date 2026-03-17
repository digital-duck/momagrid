package hub

import (
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

//go:embed hub_ddl_sqlite.sql
var ddlSQLite string

//go:embed hub_ddl_postgresql.sql
var ddlPostgres string

// InitDB opens (or creates) the database and applies the schema.
// Supports:
//   - SQLite: pass a file path (e.g. ".igrid/hub.db")
//   - Postgres: pass a connection string starting with "postgres://" or "postgresql://"
func InitDB(connStr string) (*sql.DB, error) {
	var driver, dsn string
	var ddl string

	if strings.HasPrefix(connStr, "postgres://") || strings.HasPrefix(connStr, "postgresql://") {
		driver = "postgres"
		dsn = connStr
		ddl = ddlPostgres
	} else {
		driver = "sqlite"
		dsn = connStr
		ddl = ddlSQLite
		
		// Create directory for SQLite if needed
		dir := filepath.Dir(dsn)
		if dir != "" && dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
	}

	db, err := sql.Open(driver, dsn)
	if err != nil {
		return nil, err
	}

	if driver == "sqlite" {
		db.SetMaxOpenConns(1)
		// WAL mode allows concurrent readers alongside the single writer,
		// eliminating SQLITE_BUSY errors during burst pulse storms.
		// NORMAL sync gives a good durability/throughput balance under WAL.
		db.Exec("PRAGMA journal_mode=WAL;")
		db.Exec("PRAGMA synchronous=NORMAL;")
		db.Exec("PRAGMA busy_timeout=5000;")
	} else {
		// PostgreSQL pool: 20 open, 10 idle, recycle every 5 min.
		// Increase MaxOpenConns for grids with 50+ simultaneous agents.
		db.SetMaxOpenConns(20)
		db.SetMaxIdleConns(10)
		db.SetConnMaxLifetime(5 * time.Minute)
	}

	if _, err := db.Exec(ddl); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply ddl (%s): %w", driver, err)
	}

	// Migrations for existing databases (compatibility with older Python versions)
	if driver == "sqlite" {
		migrateSQLite(db)
	}

	return db, nil
}

func migrateSQLite(db *sql.DB) {
	// Check for missing columns in agents table
	rows, err := db.Query("PRAGMA table_info(agents)")
	if err != nil {
		return
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var (
			cid, notnull, pk             int
			name, dtype, dflt_value      sql.NullString
		)
		rows.Scan(&cid, &name, &dtype, &notnull, &dflt_value, &pk)
		if name.Valid {
			cols[name.String] = true
		}
	}

	if !cols["pull_mode"] {
		db.Exec("ALTER TABLE agents ADD COLUMN pull_mode INTEGER NOT NULL DEFAULT 0")
	}
	if !cols["name"] {
		db.Exec("ALTER TABLE agents ADD COLUMN name TEXT NOT NULL DEFAULT ''")
	}
	// Ed25519 public key — empty string means unsigned agent (legacy / trusted LAN)
	if !cols["public_key"] {
		db.Exec("ALTER TABLE agents ADD COLUMN public_key TEXT NOT NULL DEFAULT ''")
	}

	// callback_url stored for crash-recovery: if hub restarts mid-forward, the
	// dispatcher can re-fire the callback when the peer result arrives.
	taskCols := make(map[string]bool)
	trows, terr := db.Query("PRAGMA table_info(tasks)")
	if terr == nil {
		defer trows.Close()
		for trows.Next() {
			var cid, notnull, pk int
			var name, dtype, dflt sql.NullString
			trows.Scan(&cid, &name, &dtype, &notnull, &dflt, &pk)
			if name.Valid {
				taskCols[name.String] = true
			}
		}
	}
	if !taskCols["callback_url"] {
		db.Exec("ALTER TABLE tasks ADD COLUMN callback_url TEXT NOT NULL DEFAULT ''")
	}

	// Ensure watchlist table exists on older databases
	db.Exec(`CREATE TABLE IF NOT EXISTS watchlist (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		entity_type TEXT    NOT NULL,
		entity_id   TEXT    NOT NULL,
		reason      TEXT    NOT NULL DEFAULT '',
		action      TEXT    NOT NULL DEFAULT 'SUSPENDED',
		created_at  TEXT    NOT NULL DEFAULT (datetime('now')),
		expires_at  TEXT,
		UNIQUE(entity_type, entity_id)
	)`)
}
