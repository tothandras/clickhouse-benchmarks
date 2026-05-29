package seed

import (
	"context"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// clickhouseSettingsCtx wraps ctx with ClickHouse query settings.
// Isolated so the call-site in seed.go stays readable.
func clickhouseSettingsCtx(ctx context.Context, settings map[string]any) context.Context {
	return clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings(settings)))
}
