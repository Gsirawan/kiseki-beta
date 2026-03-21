package stone

import (
	"regexp"
	"testing"

	"github.com/Gsirawan/kiseki-beta/internal/db"
)

func TestCreateStoneIDFormat(t *testing.T) {
	id, err := createStoneID()
	if err != nil {
		t.Fatalf("createStoneID error: %v", err)
	}

	re := regexp.MustCompile(`^stone_\d+_[a-f0-9]{8}$`)
	if !re.MatchString(id) {
		t.Fatalf("stone id %q does not match expected format", id)
	}
}

func TestCreateStone(t *testing.T) {
	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer dbConn.Close()

	t.Run("all fields", func(t *testing.T) {
		stone, err := CreateStone(dbConn, StoneInput{
			Title:         "  Fix auth timeout  ",
			Category:      " fix ",
			Problem:       " users timeout ",
			Solution:      " increased timeout and retry ",
			Tags:          "auth, timeout , api",
			ChunkIDs:      " 10, 11 ,12 ",
			KeyChunkIDs:   "11, 12",
			SourceSession: " session-1 ",
		})
		if err != nil {
			t.Fatalf("CreateStone error: %v", err)
		}

		if stone.Title != "Fix auth timeout" {
			t.Fatalf("expected trimmed title, got %q", stone.Title)
		}
		if stone.Tags != "auth,timeout,api" {
			t.Fatalf("expected normalized tags, got %q", stone.Tags)
		}
		if stone.ChunkIDs != "10,11,12" {
			t.Fatalf("expected normalized chunk ids, got %q", stone.ChunkIDs)
		}
		if stone.KeyChunkIDs != "11,12" {
			t.Fatalf("expected normalized key chunk ids, got %q", stone.KeyChunkIDs)
		}
		if stone.SourceSession != "session-1" {
			t.Fatalf("expected trimmed source session, got %q", stone.SourceSession)
		}
		if stone.Week == "" {
			t.Fatalf("expected generated week")
		}
	})

	t.Run("minimal fields", func(t *testing.T) {
		stone, err := CreateStone(dbConn, StoneInput{Title: "Minimal stone"})
		if err != nil {
			t.Fatalf("CreateStone error: %v", err)
		}
		if stone.Title != "Minimal stone" {
			t.Fatalf("unexpected title: %q", stone.Title)
		}
		if stone.Category != "" || stone.Tags != "" || stone.SourceSession != "" {
			t.Fatalf("expected optional fields empty, got %+v", stone)
		}
	})
}

func TestStoneCRUDAndQueries(t *testing.T) {
	dbConn, err := db.InitDB(":memory:")
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	defer dbConn.Close()

	stoneAuth, err := CreateStone(dbConn, StoneInput{
		Title:    "Auth timeout fix",
		Category: "fix",
		Problem:  "timeout on login",
		Solution: "updated retry strategy",
		Tags:     "auth,timeout",
	})
	if err != nil {
		t.Fatalf("CreateStone auth: %v", err)
	}

	stoneCache, err := CreateStone(dbConn, StoneInput{
		Title:    "Caching strategy",
		Category: "decision",
		Problem:  "slow page",
		Solution: "selected redis",
		Tags:     "cache,redis",
	})
	if err != nil {
		t.Fatalf("CreateStone cache: %v", err)
	}

	if _, err := dbConn.Exec(`UPDATE stones SET week = ? WHERE id = ?`, "2026-W09", stoneAuth.ID); err != nil {
		t.Fatalf("set week auth: %v", err)
	}
	if _, err := dbConn.Exec(`UPDATE stones SET week = ? WHERE id = ?`, "2026-W10", stoneCache.ID); err != nil {
		t.Fatalf("set week cache: %v", err)
	}

	t.Run("search by title", func(t *testing.T) {
		got, err := SearchStones(dbConn, StoneSearchOptions{Query: "auth", Limit: 10})
		if err != nil {
			t.Fatalf("SearchStones by title: %v", err)
		}
		if len(got) != 1 || got[0].ID != stoneAuth.ID {
			t.Fatalf("expected auth stone, got %+v", got)
		}
	})

	t.Run("search by tags", func(t *testing.T) {
		got, err := SearchStones(dbConn, StoneSearchOptions{Query: "redis", Limit: 10})
		if err != nil {
			t.Fatalf("SearchStones by tags: %v", err)
		}
		if len(got) != 1 || got[0].ID != stoneCache.ID {
			t.Fatalf("expected cache stone, got %+v", got)
		}
	})

	t.Run("search by category", func(t *testing.T) {
		got, err := SearchStones(dbConn, StoneSearchOptions{Category: "FIX", Limit: 10})
		if err != nil {
			t.Fatalf("SearchStones by category: %v", err)
		}
		if len(got) != 1 || got[0].ID != stoneAuth.ID {
			t.Fatalf("expected auth stone, got %+v", got)
		}
	})

	t.Run("search by week", func(t *testing.T) {
		got, err := SearchStones(dbConn, StoneSearchOptions{Week: "2026-W10", Limit: 10})
		if err != nil {
			t.Fatalf("SearchStones by week: %v", err)
		}
		if len(got) != 1 || got[0].ID != stoneCache.ID {
			t.Fatalf("expected cache stone, got %+v", got)
		}
	})

	t.Run("read existing", func(t *testing.T) {
		got, err := GetStone(dbConn, stoneAuth.ID)
		if err != nil {
			t.Fatalf("GetStone existing: %v", err)
		}
		if got.ID != stoneAuth.ID || got.Title != stoneAuth.Title {
			t.Fatalf("unexpected stone: %+v", got)
		}
	})

	t.Run("read non-existent", func(t *testing.T) {
		_, err := GetStone(dbConn, "stone_missing")
		if err == nil {
			t.Fatalf("expected error for missing stone")
		}
	})

	t.Run("list all", func(t *testing.T) {
		got, err := ListStones(dbConn, "", 10)
		if err != nil {
			t.Fatalf("ListStones all: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 stones, got %d", len(got))
		}
	})

	t.Run("list filtered by week", func(t *testing.T) {
		got, err := ListStones(dbConn, "2026-W09", 10)
		if err != nil {
			t.Fatalf("ListStones week: %v", err)
		}
		if len(got) != 1 || got[0].ID != stoneAuth.ID {
			t.Fatalf("expected auth stone, got %+v", got)
		}
	})

	t.Run("delete existing", func(t *testing.T) {
		deleted, err := DeleteStone(dbConn, stoneAuth.ID)
		if err != nil {
			t.Fatalf("DeleteStone existing: %v", err)
		}
		if !deleted {
			t.Fatalf("expected deleted=true")
		}
	})

	t.Run("delete non-existent", func(t *testing.T) {
		deleted, err := DeleteStone(dbConn, "stone_missing")
		if err != nil {
			t.Fatalf("DeleteStone missing: %v", err)
		}
		if deleted {
			t.Fatalf("expected deleted=false")
		}
	})
}
