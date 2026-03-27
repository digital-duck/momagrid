package cli

import (
	"database/sql"
	"flag"
	"fmt"
	"strings"

	"github.com/digital-duck/momagrid/internal/hub"
)

// Migrate implements "mg hub migrate --from <sqlite> --to <postgres>".
func Migrate(args []string) error {
	fs := flag.NewFlagSet("hub migrate", flag.ExitOnError)
	from := fs.String("from", ".igrid/hub.sqlite3", "Source SQLite database path")
	to := fs.String("to", "", "Destination Postgres connection string")
	fs.Parse(args)

	if *to == "" {
		return fmt.Errorf("destination (--to) Postgres connection string is required")
	}

	fmt.Printf("Migrating data:\n  From (SQLite): %s\n  To (Postgres): %s\n\n", *from, *to)

	src, err := hub.InitDB(*from)
	if err != nil {
		return fmt.Errorf("open source db: %w", err)
	}
	defer src.Close()

	dst, err := hub.InitDB(*to)
	if err != nil {
		return fmt.Errorf("open destination db: %w", err)
	}
	defer dst.Close()

	tables := []string{"hub_config", "operators", "agents", "peer_hubs", "tasks", "pulse_log", "reward_ledger"}

	for _, table := range tables {
		fmt.Printf("Migrating table %-15s ... ", table)
		count, err := migrateTable(src, dst, table)
		if err != nil {
			fmt.Printf("ERROR: %v\n", err)
		} else {
			fmt.Printf("OK (%d rows)\n", count)
		}
	}

	fmt.Println("\nMigration complete.")
	return nil
}

func migrateTable(src, dst *sql.DB, table string) (int, error) {
	rows, err := src.Query(fmt.Sprintf("SELECT * FROM %s", table))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	cols, _ := rows.Columns()
	placeholders := make([]string, len(cols))
	for i := range placeholders {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING",
		table, strings.Join(cols, ","), strings.Join(placeholders, ","))

	count := 0
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}

		if err := rows.Scan(ptrs...); err != nil {
			return count, err
		}

		// Handle SQLite types for Postgres
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}

		if _, err := dst.Exec(query, vals...); err != nil {
			return count, fmt.Errorf("insert row: %w", err)
		}
		count++
	}

	return count, nil
}
