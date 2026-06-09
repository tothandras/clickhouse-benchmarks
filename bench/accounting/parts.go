package accounting

import (
	"context"
	"fmt"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

// StorageSnapshot captures the table's active-part footprint at one instant.
// Taken at prepare, soak end, and drain end so settled bytes/event can be
// computed over the prepare -> drain-end compressed delta.
type StorageSnapshot struct {
	At                time.Time `json:"at"`
	Rows              uint64    `json:"rows"`
	Parts             uint64    `json:"parts"`
	Partitions        uint64    `json:"partitions"`
	CompressedBytes   uint64    `json:"compressed_bytes"`
	UncompressedBytes uint64    `json:"uncompressed_bytes"`
}

// SnapshotStorage reads the table's active parts.
func SnapshotStorage(ctx context.Context, conn driver.Conn, database, table string) (StorageSnapshot, error) {
	s := StorageSnapshot{At: time.Now().UTC()}
	err := conn.QueryRow(ctx, `
SELECT
  toUInt64(sum(rows)),
  toUInt64(count()),
  toUInt64(uniqExact(partition)),
  toUInt64(sum(data_compressed_bytes)),
  toUInt64(sum(data_uncompressed_bytes))
FROM system.parts
WHERE active AND database = ? AND table = ?`, database, table).Scan(
		&s.Rows, &s.Parts, &s.Partitions, &s.CompressedBytes, &s.UncompressedBytes)
	if err != nil {
		return s, fmt.Errorf("accounting: storage snapshot: %w", err)
	}
	return s, nil
}

// ActivePartCount is the cheap poll the soak-phase plateau gate uses.
func ActivePartCount(ctx context.Context, conn driver.Conn, database, table string) (int, error) {
	var n uint64
	err := conn.QueryRow(ctx,
		"SELECT count() FROM system.parts WHERE active AND database = ? AND table = ?",
		database, table).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("accounting: part count: %w", err)
	}
	return int(n), nil
}

// DatabaseSize is a non-harness database with data on the service.
type DatabaseSize struct {
	Database string `json:"database"`
	Bytes    uint64 `json:"bytes"`
}

// ForeignDatabases lists other databases with nontrivial size (> minBytes).
// COGS methodology requires a dedicated service: foreign data means foreign
// merges and queries polluting coverage and reconciliation, so the runner
// warns when this returns anything.
func ForeignDatabases(ctx context.Context, conn driver.Conn, ownDatabase string, minBytes uint64) ([]DatabaseSize, error) {
	rows, err := conn.Query(ctx, `
SELECT database, toUInt64(sum(bytes_on_disk)) AS bytes
FROM system.parts
WHERE active
  AND database NOT IN (?, 'system', 'INFORMATION_SCHEMA', 'information_schema')
GROUP BY database
HAVING bytes > ?
ORDER BY bytes DESC`, ownDatabase, minBytes)
	if err != nil {
		return nil, fmt.Errorf("accounting: foreign databases: %w", err)
	}
	defer rows.Close()
	var out []DatabaseSize
	for rows.Next() {
		var d DatabaseSize
		if err := rows.Scan(&d.Database, &d.Bytes); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
