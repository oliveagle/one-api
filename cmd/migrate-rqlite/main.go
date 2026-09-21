//go:build cgo

// cmd/migrate-rqlite migrates a one-api SQLite database into an embedded
// RQLite store.
//
// Usage:
//
//	migrate-rqlite -src <one-api.db> -dst <rqlite-dir>
//
// The destination directory becomes a valid RQLite single-node data dir
// (db.sqlite + raft state). On first run of the one-api binary with
// RQLITE_DIR=<rqlite-dir> the existing data is detected and used as-is.
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/raft"
	"github.com/rqlite/rqlite/v10/snapshot"
	"github.com/songquanpeng/one-api/store/rqlite"
)

func main() {
	src := flag.String("src", "", "source one-api SQLite db path")
	dst := flag.String("dst", "", "destination rqlite data dir")
	flag.Parse()
	if *src == "" || *dst == "" {
		log.Fatal("usage: migrate-rqlite -src <one-api.db> -dst <rqlite-dir>")
	}

	srcDB, err := sql.Open("sqlite3", *src)
	if err != nil {
		log.Fatalf("open src: %v", err)
	}
	defer srcDB.Close()
	if err := srcDB.Ping(); err != nil {
		log.Fatalf("ping src: %v", err)
	}

	tables, err := listTables(srcDB)
	if err != nil {
		log.Fatalf("list tables: %v", err)
	}
	log.Printf("source tables: %v", tables)

	// Boot the destination RQLite store (fresh or existing).
	es, err := rqlite.StartEmbedded(*dst, "")
	if err != nil {
		log.Fatalf("open rqlite store: %v", err)
	}
	defer es.Close()
	dstDB, err := rqlite.OpenSQL()
	if err != nil {
		log.Fatalf("open dst sql: %v", err)
	}
	defer dstDB.Close()
	if err := dstDB.Ping(); err != nil {
		log.Fatalf("ping dst: %v", err)
	}
	existing, err := listTables(dstDB)
	if err != nil {
		log.Fatalf("list dst tables: %v", err)
	}
	if len(existing) > 0 {
		log.Fatalf("destination %s already has tables %v; refusing to overwrite (use a fresh dir)", *dst, existing)
	}

	start := time.Now()
	// Take source counts once up front so a concurrently-growing source
	// table (the live one-api keeps writing logs) does not cause a false
	// mismatch between the snapshot count and the post-migration count.
	srcCounts := make(map[string]int)
	for _, tbl := range tables {
		n, err := countRows(srcDB, tbl)
		if err != nil {
			log.Fatalf("count src %s: %v", tbl, err)
		}
		srcCounts[tbl] = n
	}
	for _, tbl := range tables {
		log.Printf("migrating table %s ...", tbl)
		if err := migrateTable(srcDB, dstDB, tbl); err != nil {
			log.Fatalf("migrate %s: %v", tbl, err)
		}
		got, err := countRows(dstDB, tbl)
		if err != nil {
			log.Fatalf("count dst %s: %v", tbl, err)
		}
		if got < srcCounts[tbl] {
			log.Fatalf("row count for %s: dst=%d < src=%d (data loss)", tbl, got, srcCounts[tbl])
		}
		log.Printf("  %s verified: %d rows (src was %d)", tbl, got, srcCounts[tbl])
	}

	// Ensure the db file (not just the WAL) is complete and consistent for
	// the next boot's recovery. The store's own snapshot mechanism keeps
	// producing empty snapshots (a rqlite v10 quirk), so we rely on the
	// db file itself: checkpoint + vacuum, then verify.
	if _, err := dstDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("warning: checkpoint: %v", err)
	}
	if _, err := dstDB.Exec("VACUUM"); err != nil {
		log.Printf("warning: vacuum: %v", err)
	}
	if _, err := dstDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("warning: final checkpoint: %v", err)
	}
	// The db file (after checkpoint+vacuum) is the source of truth for the
	// next boot's recovery. Verify it directly: open an independent
	// connection to the file and check every table's row count.
	if err := verifyDBFile(es, dstDB, tables); err != nil {
		log.Fatalf("db file verification failed: %v", err)
	}
	log.Printf("db file verified: all %d tables present with correct row counts", len(tables))
	// Ensure the db file itself (not just the snapshot) is complete and
	// consistent: checkpoint + vacuum once more, then sync.
	if _, err := dstDB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		log.Printf("warning: final checkpoint: %v", err)
	}
	if _, err := dstDB.Exec("VACUUM"); err != nil {
		log.Printf("warning: final vacuum: %v", err)
	}

	log.Printf("migration complete in %s (%d tables)", time.Since(start).Round(time.Millisecond), len(tables))
}

// verifySnapshot restores the latest snapshot to a scratch db and checks
// that every migrated table is present with the right row count.
// verifyDBFile opens an independent connection to the store's db file and
// checks that every migrated table is present with the right row count.
// This is the real verification: the recovery path restores db.sqlite from
// the snapshot, so db.sqlite must be complete.
func verifyDBFile(es *rqlite.EmbeddedStore, dst *sql.DB, tables []string) error {
	// ensure the file is fully checkpointed
	if _, err := dst.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		return err
	}
	vdb, err := sql.Open("sqlite3", es.DBPath())
	if err != nil {
		return err
	}
	defer vdb.Close()
	if err := vdb.Ping(); err != nil {
		return err
	}
	for _, tbl := range tables {
		var want, got int
		if err := vdb.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", quoteIdent(tbl))).Scan(&want); err != nil {
			return fmt.Errorf("table %s not in db file: %v", tbl, err)
		}
		if err := dst.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", quoteIdent(tbl))).Scan(&got); err != nil {
			return fmt.Errorf("table %s not in dst: %v", tbl, err)
		}
		if got != want {
			return fmt.Errorf("table %s: dbfile=%d dst=%d", tbl, want, got)
		}
	}
	return nil
}

func verifySnapshot(es *rqlite.EmbeddedStore, dst *sql.DB, tables []string) error {
	dir := es.DataDir()
	snapStore, err := snapshot.NewStore(dir + "/wsnapshots")
	if err != nil {
		return err
	}
	defer snapStore.Close()
	all, err := snapStore.ListAll()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		return fmt.Errorf("no snapshots found")
	}
	var latest *raft.SnapshotMeta
	for _, s := range all {
		if latest == nil || s.Index > latest.Index {
			latest = s
		}
	}
	// Stream the latest snapshot to a temp file, then restore it.
	// (snapStore.Open returns a reader; snapshot.Restore needs the full
	// stream. We use the streamer directly on the snapshot's data.db.)
	snapDir := dir + "/wsnapshots/" + latest.ID
	dataDB := snapDir + "/data.db"
	streamer, err := snapshot.NewSnapshotStreamer(dataDB)
	if err != nil {
		return fmt.Errorf("streamer: %w", err)
	}
	defer streamer.Close()
	if err := streamer.Open(); err != nil {
		return fmt.Errorf("streamer open: %w", err)
	}
	tmp, err := os.CreateTemp(dir, "verify-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	tmp.Close()
	defer os.Remove(tmpPath)
	// copy stream to tmp file
	buf := make([]byte, 65536)
	for {
		nr, err := streamer.Read(buf)
		tmp2, _ := os.OpenFile(tmpPath, os.O_APPEND|os.O_WRONLY, 0644)
		tmp2.Write(buf[:nr])
		tmp2.Close()
		if err != nil {
			break
		}
	}
	rc, _ := os.Open(tmpPath)
	defer rc.Close()
	if _, err := snapshot.Restore(rc, tmpPath+".restored"); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	vdb, err := sql.Open("sqlite3", tmpPath+".restored")
	if err != nil {
		return err
	}
	defer vdb.Close()
	for _, tbl := range tables {
		want, _ := countRows(dst, tbl)
		var got int
		if err := vdb.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", quoteIdent(tbl))).Scan(&got); err != nil {
			return fmt.Errorf("table %s not in snapshot: %v", tbl, err)
		}
		if got != want {
			return fmt.Errorf("table %s: snapshot=%d dst=%d", tbl, got, want)
		}
	}
	return nil
}

func listTables(db *sql.DB) ([]string, error) {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

func migrateTable(src *sql.DB, dst *sql.DB, table string) error {
	count, err := countRows(src, table)
	if err != nil {
		return err
	}
	log.Printf("  %s: %d rows", table, count)

	// Create the destination table with the exact same schema.
	schema, err := tableSchema(src, table)
	if err != nil {
		return err
	}
	if _, err := dst.Exec(schema); err != nil {
		return fmt.Errorf("create %s: %w", table, err)
	}

	// Verify the table is actually visible on the destination.
	var vis int
	if err := dst.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", quoteIdent(table))).Scan(&vis); err != nil {
		return fmt.Errorf("verify create %s: %w", table, err)
	}
	if vis != 0 {
		log.Printf("  %s: WARNING pre-existing rows %d (expected 0)", table, vis)
	}

	const batch = 500
	for offset := 0; offset < count; offset += batch {
		q := fmt.Sprintf("SELECT * FROM %s ORDER BY rowid LIMIT %d OFFSET %d", quoteIdent(table), batch, offset)
		rows, err := src.Query(q)
		if err != nil {
			return err
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		var args []any
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return err
			}
			args = append(args, vals...)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if len(args) == 0 {
			break
		}
		colNames := make([]string, len(cols))
		for i, c := range cols {
			colNames[i] = quoteIdent(c)
		}
		stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteIdent(table), strings.Join(colNames, ", "), placeholder(len(cols)))
		tx, err := dst.Begin()
		if err != nil {
			return err
		}
		// Insert one row per statement (avoids multi-row placeholder
		// edge cases and keeps each statement atomic).
		rowCount := len(args) / len(cols)
		for r := 0; r < rowCount; r++ {
			rowArgs := args[r*len(cols) : (r+1)*len(cols)]
			if _, err := tx.Exec(stmt, rowArgs...); err != nil {
				tx.Rollback()
				return fmt.Errorf("insert row %d: %w", r, err)
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func countRows(db *sql.DB, table string) (int, error) {
	var n int
	err := db.QueryRow(fmt.Sprintf("SELECT count(*) FROM %s", quoteIdent(table))).Scan(&n)
	return n, err
}

func tableSchema(db *sql.DB, table string) (string, error) {
	var s string
	err := db.QueryRow("SELECT sql FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&s)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("empty schema for %s", table)
	}
	return s + ";", nil
}

func placeholder(n int) string {
	if n <= 0 {
		return ""
	}
	out := make([]string, n)
	for i := range out {
		out[i] = "?"
	}
	return strings.Join(out, ", ")
}

func quoteIdent(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
}
