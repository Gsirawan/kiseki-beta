package entity

import (
	"os"
	"testing"
)

func TestRealEntitiesYAML(t *testing.T) {
	path := os.Getenv("KISEKI_ENTITIES")
	if path == "" {
		t.Skip("KISEKI_ENTITIES not set, skipping integration test")
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Skip("entities.yaml not found (CI environment), skipping integration test")
	}

	// 1. Parse
	graph, err := LoadEntityGraph(path)
	if err != nil {
		t.Fatalf("LoadEntityGraph failed: %v", err)
	}
	t.Logf("Parsed: %d entities", len(graph.Entities))

	if len(graph.Entities) == 0 {
		t.Error("expected at least 1 entity, got 0")
	}

	// Count aliases
	totalAliases := 0
	for _, e := range graph.Entities {
		totalAliases += len(e.Aliases)
		t.Logf("  %s aliases=%v", e.Name, e.Aliases)
	}
	t.Logf("Total aliases: %d", totalAliases)

	// 2. Ingest into test DB
	db := newTestDB(t)
	defer db.Close()

	if err := IngestEntities(db, graph); err != nil {
		t.Fatalf("IngestEntities failed: %v", err)
	}

	// 3. Verify counts match what was parsed
	var entityCount, aliasCount int
	db.QueryRow("SELECT COUNT(*) FROM entities").Scan(&entityCount)
	db.QueryRow("SELECT COUNT(*) FROM entity_aliases").Scan(&aliasCount)

	t.Logf("Ingested — Entities: %d, Aliases: %d", entityCount, aliasCount)

	if entityCount != len(graph.Entities) {
		t.Errorf("DB entities: expected %d, got %d", len(graph.Entities), entityCount)
	}
	if aliasCount != totalAliases {
		t.Errorf("DB aliases: expected %d, got %d", totalAliases, aliasCount)
	}

	// 4. Test alias expansion dynamically — pick entities that have aliases
	// No hardcoded names: test cases are derived from the loaded yaml
	tested := 0
	for _, e := range graph.Entities {
		if len(e.Aliases) == 0 {
			continue
		}
		alias := e.Aliases[0]
		query := "tell me about " + alias

		expanded, matches, err := ExpandQuery(db, query)
		if err != nil {
			t.Errorf("ExpandQuery(%q): %v", query, err)
			continue
		}

		found := false
		for _, m := range matches {
			if m.Alias == alias && m.EntityName == e.Name {
				found = true
			}
		}
		if !found {
			t.Errorf("ExpandQuery(%q): wanted alias %q→%q, got matches=%v, expanded=%q",
				query, alias, e.Name, matches, expanded)
		} else {
			t.Logf("  %q → %q (%s→%s)", query, expanded, alias, e.Name)
		}

		tested++
		if tested >= 5 {
			break // test a sample, not all
		}
	}

	if tested == 0 {
		t.Error("no entities with aliases found — nothing was tested")
	}
}
