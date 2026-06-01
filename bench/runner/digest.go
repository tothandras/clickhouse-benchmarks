package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/shopspring/decimal"
)

// DigestDecimals is the rounding precision applied to float/decimal result cells
// before hashing. Float aggregates (avg, and any sum that accumulates in
// Float64) can differ in their low-order bits between the `data String` and
// `data JSON` layouts because the rows are summed in a different physical order;
// rounding to a fixed number of decimals makes the digest robust to that while
// still catching any real value difference. Decimal128 aggregates are already
// order-independent, so rounding them is a harmless no-op at this precision.
const DigestDecimals = 6

// DigestResult runs sql (already param-rendered) against conn and returns a
// normalized fingerprint of its result set: a SHA-256 over the rows, with
// float/decimal cells rounded to DigestDecimals and the rows sorted so a
// non-unique GROUP BY's tie-ordering does not affect the hash. The row count is
// returned alongside. Two scenarios that hold the same events over the same
// window produce the same digest for the same query — regardless of whether
// `data` is stored as String or JSON.
//
// The result is byte-for-byte determined by the values only: column *names* and
// physical types are deliberately excluded, so `count(*)` and `JSON_VALUE`-vs-
// native-subcolumn formulations of the same logical query still match.
func DigestResult(ctx context.Context, conn driver.Conn, sql string) (QueryDigest, error) {
	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return QueryDigest{Error: err.Error()}, err
	}
	defer rows.Close()

	cts := rows.ColumnTypes()
	isFloat := make([]bool, len(cts))
	isDecimal := make([]bool, len(cts))
	for i, ct := range cts {
		// Nullable columns have a pointer ScanType (*float64, *decimal.Decimal);
		// unwrap it so numeric detection works for both Nullable and bare columns.
		st := ct.ScanType()
		for st.Kind() == reflect.Pointer {
			st = st.Elem()
		}
		switch st.Kind() {
		case reflect.Float32, reflect.Float64:
			isFloat[i] = true
		}
		// DatabaseTypeName is e.g. "Decimal(38, 4)" or "Nullable(Decimal(38, 4))".
		if strings.Contains(ct.DatabaseTypeName(), "Decimal") {
			isDecimal[i] = true
		}
	}

	var lines []string
	for rows.Next() {
		ptrs := make([]any, len(cts))
		for i, ct := range cts {
			ptrs[i] = reflect.New(ct.ScanType()).Interface()
		}
		if err := rows.Scan(ptrs...); err != nil {
			return QueryDigest{Error: err.Error()}, err
		}
		cells := make([]string, len(cts))
		for i, p := range ptrs {
			cells[i] = normalizeCell(reflect.ValueOf(p).Elem().Interface(), isFloat[i], isDecimal[i])
		}
		lines = append(lines, strings.Join(cells, "\x1f")) // unit separator: can't occur in values
	}
	if err := rows.Err(); err != nil {
		return QueryDigest{Error: err.Error()}, err
	}

	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return QueryDigest{Hash: hex.EncodeToString(h.Sum(nil)), Rows: int64(len(lines))}, nil
}

// normalizeCell renders one scanned value to its canonical string for hashing.
// Float and Decimal cells are rounded to DigestDecimals; everything else uses
// its default %v form (stable for ints, strings, dates, etc.).
//
// Nullable columns scan into pointer types (e.g. JSON_VALUE returns
// Nullable(String) → *string), while the equivalent non-null column scans into
// the bare type (native .:String → string). To make a Nullable and a non-null
// column with the same VALUES digest equal — they represent the same data, just
// differently typed by the two access paths — we deref pointers to their pointee
// (nil → SQL NULL) before formatting. Without this, %v would hash a pointer
// address and every Nullable-vs-bare column pair would falsely differ.
func normalizeCell(v any, isFloat, isDecimal bool) string {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "∅" // SQL NULL
		}
		v = rv.Elem().Interface()
	}
	switch {
	case v == nil:
		return "∅"
	case isDecimal:
		if d, ok := v.(decimal.Decimal); ok {
			return d.Round(DigestDecimals).String()
		}
	case isFloat:
		if f, ok := toFloat(v); ok {
			return fmt.Sprintf("%.*f", DigestDecimals, f)
		}
	}
	return fmt.Sprintf("%v", v)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	}
	return 0, false
}
