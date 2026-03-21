package entity

import (
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"sort"

	"gopkg.in/yaml.v3"
)

// Entity represents a named concept with aliases for query expansion
type Entity struct {
	Name        string   `yaml:"name"`
	Type        string   `yaml:"type"`
	Aliases     []string `yaml:"aliases"`
	Description string   `yaml:"description"`
}

// unused struct — relationships table never populated (yaml nests under entities, not top-level)
// type Relationship struct {
// 	From        string `yaml:"from"`
// 	To          string `yaml:"to"`
// 	Type        string `yaml:"type"`
// 	Description string `yaml:"description"`
// }

// EntityGraph holds the entity list loaded from entities.yaml
type EntityGraph struct {
	Entities []Entity `yaml:"entities"`
}

// LoadEntityGraph reads and parses entities.yaml
func LoadEntityGraph(path string) (*EntityGraph, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read entities file: %w", err)
	}

	var graph EntityGraph
	if err := yaml.Unmarshal(data, &graph); err != nil {
		return nil, fmt.Errorf("parse entities YAML: %w", err)
	}

	return &graph, nil
}

// aliasPattern returns a regex that matches the alias respecting word boundaries.
// For ASCII aliases, uses \b to prevent substring matches (e.g., "AB" inside "ABC").
// For non-ASCII aliases (Arabic, Japanese), uses literal match since \b only handles ASCII word chars.
func aliasPattern(alias string) string {
	quoted := regexp.QuoteMeta(alias)
	for _, c := range alias {
		if c > 127 {
			return `(?i)` + quoted
		}
	}
	return `(?i)\b` + quoted + `\b`
}

// IngestEntities loads entities.yaml into the database
func IngestEntities(db *sql.DB, graph *EntityGraph) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Clear existing data (full reload each time)
	if _, err := tx.Exec(`DELETE FROM relationships`); err != nil {
		return fmt.Errorf("clear relationships: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM entity_aliases`); err != nil {
		return fmt.Errorf("clear aliases: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM entities`); err != nil {
		return fmt.Errorf("clear entities: %w", err)
	}

	// Insert entities + aliases
	for _, e := range graph.Entities {
		res, err := tx.Exec(
			`INSERT INTO entities (name, type, description) VALUES (?, ?, ?)`,
			e.Name, e.Type, e.Description,
		)
		if err != nil {
			return fmt.Errorf("insert entity %s: %w", e.Name, err)
		}
		id, _ := res.LastInsertId()

		for _, alias := range e.Aliases {
			_, err := tx.Exec(
				`INSERT INTO entity_aliases (entity_id, alias) VALUES (?, ?)`,
				id, alias,
			)
			if err != nil {
				return fmt.Errorf("insert alias %s for %s: %w", alias, e.Name, err)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// AliasMatch represents a found alias and its canonical entity
type AliasMatch struct {
	Alias      string
	EntityName string
	EntityType string
	Note       string
}

// LookupAliases finds all entity aliases mentioned in the query.
// Uses word boundaries for ASCII aliases to prevent substring false positives
// (e.g., alias "AB" must not match inside "ABC").
func LookupAliases(db *sql.DB, query string) ([]AliasMatch, error) {
	rows, err := db.Query(`
		SELECT ea.alias, e.name, e.type, ea.note
		FROM entity_aliases ea
		JOIN entities e ON ea.entity_id = e.id
	`)
	if err != nil {
		return nil, fmt.Errorf("query aliases: %w", err)
	}
	defer rows.Close()

	var matches []AliasMatch
	for rows.Next() {
		var alias, name, typ string
		var note sql.NullString
		if err := rows.Scan(&alias, &name, &typ, &note); err != nil {
			continue
		}

		// Word-boundary-aware match (ASCII aliases use \b, non-ASCII use literal)
		re := regexp.MustCompile(aliasPattern(alias))
		if re.MatchString(query) {
			matches = append(matches, AliasMatch{
				Alias:      alias,
				EntityName: name,
				EntityType: typ,
				Note:       note.String,
			})
		}
	}

	return matches, nil
}

// ExpandQuery replaces aliases with canonical names and adds context.
// Sorts matches by alias length (longest first) to prevent partial corruption
// (e.g., "the tree" must be replaced before "tree" gets a chance).
func ExpandQuery(db *sql.DB, query string) (string, []AliasMatch, error) {
	matches, err := LookupAliases(db, query)
	if err != nil {
		return query, nil, err
	}

	if len(matches) == 0 {
		return query, nil, nil
	}

	// Sort by alias length descending — longest matches first
	sort.Slice(matches, func(i, j int) bool {
		return len(matches[i].Alias) > len(matches[j].Alias)
	})

	expanded := query
	for _, m := range matches {
		pattern := regexp.MustCompile(aliasPattern(m.Alias))
		expanded = pattern.ReplaceAllString(expanded, m.EntityName)
	}

	return expanded, matches, nil
}

// unused function — description field exists in DB but no search layer reads it
// func GetEntityContext(db *sql.DB, name string) (string, error) {
// 	var description sql.NullString
// 	err := db.QueryRow(
// 		`SELECT description FROM entities WHERE name = ? COLLATE NOCASE`,
// 		name,
// 	).Scan(&description)
// 	if err == sql.ErrNoRows {
// 		return "", nil
// 	}
// 	if err != nil {
// 		return "", err
// 	}
// 	return description.String, nil
// }

// unused function — relationships table is always empty (yaml nests relationships
// under entities, but IngestEntities expects a top-level Relationships slice)
// func GetRelationships(db *sql.DB, entityName string) ([]string, error) {
// 	rows, err := db.Query(`
// 		SELECT e2.name, r.relation_type, r.description
// 		FROM relationships r
// 		JOIN entities e1 ON r.entity_a = e1.id
// 		JOIN entities e2 ON r.entity_b = e2.id
// 		WHERE e1.name = ? COLLATE NOCASE
// 	`, entityName)
// 	if err != nil {
// 		return nil, err
// 	}
// 	defer rows.Close()
//
// 	var rels []string
// 	for rows.Next() {
// 		var name, relType string
// 		var desc sql.NullString
// 		if err := rows.Scan(&name, &relType, &desc); err != nil {
// 			continue
// 		}
// 		if desc.String != "" {
// 			rels = append(rels, fmt.Sprintf("%s (%s): %s", name, relType, desc.String))
// 		} else {
// 			rels = append(rels, fmt.Sprintf("%s (%s)", name, relType))
// 		}
// 	}
// 	return rels, nil
// }

// unused function — entity stats not exposed by any MCP tool or CLI command
// type EntityStats struct {
// 	TotalEntities      int
// 	TotalAliases       int
// 	TotalRelationships int
// 	ByType             map[string]int
// }
//
// func GetEntityStats(db *sql.DB) (*EntityStats, error) {
// 	stats := &EntityStats{ByType: make(map[string]int)}
// 	db.QueryRow(`SELECT COUNT(*) FROM entities`).Scan(&stats.TotalEntities)
// 	db.QueryRow(`SELECT COUNT(*) FROM entity_aliases`).Scan(&stats.TotalAliases)
// 	db.QueryRow(`SELECT COUNT(*) FROM relationships`).Scan(&stats.TotalRelationships)
// 	rows, err := db.Query(`SELECT type, COUNT(*) FROM entities GROUP BY type`)
// 	if err != nil {
// 		return stats, err
// 	}
// 	defer rows.Close()
// 	for rows.Next() {
// 		var typ string
// 		var count int
// 		if err := rows.Scan(&typ, &count); err == nil {
// 			stats.ByType[typ] = count
// 		}
// 	}
// 	return stats, nil
// }
