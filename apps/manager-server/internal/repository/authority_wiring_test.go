package repository_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/seakee/cpa-manager-plus/apps/manager-server/internal/database/schema"
)

// TestAuthoritativeRepositoryWiring is a readiness guard, not a substitute for
// trigger coverage. It makes every authoritative table declare its repository
// write owners and rejects newly introduced raw DB transactions in those
// owners. sqlite/migrate.go is deliberately excluded: schema upgrades run
// before a journal contract can be installed and cannot execute once the
// current manifest contract exists.
func TestAuthoritativeRepositoryWiring(t *testing.T) {
	owners := map[string][]string{
		"usage_events":                       {"usageevent/repository.go", "usageevent/backfill.go", "datamigration/repository.go"},
		"dead_letter_events":                 {"deadletter/repository.go"},
		"settings":                           {"setting/repository.go", "setting/update_check.go", "datamigration/repository.go"},
		"model_prices":                       {"modelprice/repository.go"},
		"model_price_context_tiers":          {"modelprice/repository.go"},
		"model_price_service_tiers":          {"modelprice/repository.go"},
		"api_key_aliases":                    {"apikeyalias/repository.go"},
		"account_action_candidates":          {"accountaction/repository.go"},
		"codex_inspection_runs":              {"codexinspection/repository.go", "codexinspection/lifecycle.go"},
		"codex_inspection_leases":            {"codexinspection/lifecycle.go"},
		"codex_inspection_results":           {"codexinspection/repository.go"},
		"codex_inspection_logs":              {"codexinspection/repository.go", "codexinspection/lifecycle.go"},
		"codex_inspection_disable_ownership": {"codexinspection/repository.go"},
		"quota_cooldowns":                    {"quotacooldown/repository.go"},
		"account_quota_observations":         {"quotasnapshot/repository.go", "quotasnapshot/lifecycle.go"},
		"account_quota_windows":              {"quotasnapshot/lifecycle.go"},
		"account_quota_window_activations":   {"quotasnapshot/lifecycle.go"},
		"account_quota_cycles":               {"quotasnapshot/lifecycle.go"},
		"account_quota_snapshots":            {"quotasnapshot/repository.go", "quotasnapshot/lifecycle.go"},
	}

	var canonical []string
	for _, table := range schema.Current().AuthoritativeTables() {
		canonical = append(canonical, table.Name)
		if len(owners[table.Name]) == 0 {
			t.Errorf("authoritative table %s has no declared repository write owner", table.Name)
		}
	}
	for table := range owners {
		found := false
		for _, name := range canonical {
			found = found || name == table
		}
		if !found {
			t.Errorf("wiring matrix contains non-authoritative table %s", table)
		}
	}
	if t.Failed() {
		return
	}

	checked := map[string]bool{}
	for _, files := range owners {
		for _, name := range files {
			if checked[name] {
				continue
			}
			checked[name] = true
			source, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			text := string(source)
			if !strings.Contains(text, "internal/outboxcontext") ||
				(!strings.Contains(text, "outboxcontext.Begin") && !strings.Contains(text, "outboxcontext.Exec")) {
				t.Errorf("repository write owner %s is not wired to authority transaction context", name)
			}
			if strings.Contains(text, ".BeginTx(ctx, nil)") {
				t.Errorf("repository write owner %s contains a raw write transaction", name)
			}
		}
	}

	for table, files := range owners {
		pattern := regexp.MustCompile(`(?is)(insert(?:\s+or\s+\w+)?\s+into|update|delete\s+from)\s+[` + "`\"" + `]?` + regexp.QuoteMeta(table) + `\b`)
		declared := map[string]bool{}
		for _, file := range files {
			declared[filepath.Clean(file)] = true
		}
		var discovered []string
		err := filepath.WalkDir(".", func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			clean := filepath.Clean(path)
			if entry.IsDir() {
				if clean == filepath.Clean("sqlite") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			source, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if pattern.Match(source) {
				discovered = append(discovered, clean)
				if !declared[clean] {
					t.Errorf("authoritative table %s has undeclared write source %s", table, clean)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		sort.Strings(discovered)
		if len(discovered) == 0 {
			t.Errorf("authoritative table %s has no discovered DML entry", table)
		}
	}
}
