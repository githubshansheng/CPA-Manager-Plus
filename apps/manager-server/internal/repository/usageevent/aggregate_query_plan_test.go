package usageevent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	sqliterepo "github.com/seakee/cpa-manager-plus/apps/manager-server/internal/repository/sqlite"
)

func TestTopModelsQueryUsesTimestampIndexBeforePricingMaterialization(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("prepare post-listen indexes: %v", err)
	}

	rows, err := db.Query(`explain query plan `+topModelsSQL, int64(1_000), int64(2_000), 5)
	if err != nil {
		t.Fatalf("explain top models query: %v", err)
	}
	defer rows.Close()

	details := make([]string, 0, 8)
	usesTimestampIndex := false
	fullUsageScan := false
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
		usesTimestampIndex = usesTimestampIndex || strings.Contains(detail, "SEARCH usage_events USING INDEX idx_usage_events_timestamp")
		fullUsageScan = fullUsageScan || strings.Contains(detail, "SCAN usage_events")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	if !usesTimestampIndex || fullUsageScan {
		t.Fatalf("top models query did not constrain usage_events with the timestamp index: %v", details)
	}
}

func TestChannelModelStatsQueryFiltersBeforePricingMaterialization(t *testing.T) {
	db, err := sqliterepo.Open(filepath.Join(t.TempDir(), "usage.sqlite"))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := sqliterepo.RunDerivedStartupMaintenance(context.Background(), db); err != nil {
		t.Fatalf("prepare post-listen indexes: %v", err)
	}

	query, args := channelModelStatsQuery(AnalyticsFilter{
		FromMS:        1_000,
		ToMS:          2_000,
		IncludeFailed: true,
	}, false)
	pricingBaseEnd := strings.Index(query, "), pricing_resolved_events as")
	if pricingBaseEnd < 0 {
		t.Fatal("channel query does not contain the pricing base CTE")
	}
	pricingBase := query[:pricingBaseEnd]
	if !strings.Contains(pricingBase, "where timestamp_ms >= ? and timestamp_ms < ?") {
		t.Fatalf("channel query does not filter usage_events inside the pricing base CTE: %s", pricingBase)
	}
	if strings.Contains(query[pricingBaseEnd:], "from banded_usage_events where timestamp_ms") {
		t.Fatal("channel query applies the timestamp predicate after pricing materialization")
	}

	rows, err := db.Query(`explain query plan `+query, args...)
	if err != nil {
		t.Fatalf("explain channel model stats query: %v", err)
	}
	defer rows.Close()

	details := make([]string, 0, 8)
	usesTimestampIndex := false
	fullUsageScan := false
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		if err := rows.Scan(&id, &parent, &notUsed, &detail); err != nil {
			t.Fatalf("scan query plan: %v", err)
		}
		details = append(details, detail)
		usesTimestampIndex = usesTimestampIndex || strings.Contains(detail, "SEARCH usage_events USING INDEX idx_usage_events_timestamp")
		fullUsageScan = fullUsageScan || strings.Contains(detail, "SCAN usage_events")
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("query plan rows: %v", err)
	}
	if !usesTimestampIndex || fullUsageScan {
		t.Fatalf("channel query did not constrain usage_events with the timestamp index: %v", details)
	}
}
