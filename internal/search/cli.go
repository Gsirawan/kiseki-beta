package search

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
)

// RunSearchCLI handles the "kiseki search" CLI command.
func RunSearchCLI(args []string, kisekiDB, ollamaHost, embedModel string) {
	fs := flag.NewFlagSet("search", flag.ExitOnError)
	asOf := fs.String("as-of", "", "optional date filter (YYYY-MM-DD)")
	limit := fs.Int("limit", 50, "min results per search layer")
	jsonOut := fs.Bool("json", false, "output results as JSON")
	vecThreshold := fs.Float64("vec-threshold", 1.5, "max vec distance to include")
	legacy := fs.Bool("legacy", false, "use legacy vec-only search")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: question required as first positional argument\n")
		os.Exit(1)
	}

	question := fs.Arg(0)

	database, err := db.InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	ollamaClient := ollama.NewOllamaClient("http://"+ollamaHost, embedModel)

	// Legacy mode: use old vec-only search for backward compat / comparison
	if *legacy {
		oldResults, err := Search(database, ollamaClient, question, *limit, *asOf)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: search: %v\n", err)
			os.Exit(1)
		}
		for _, r := range oldResults {
			fmt.Printf("[%.4f] %s \u2014 %s\n%s\n\n", r.Distance, r.SourceFile, r.SectionTitle, truncate(r.Text, 200))
		}
		return
	}

	// Multi-layer search (default)
	cfg := DefaultMultiLayerConfig()
	cfg.MinPerLayer = *limit
	cfg.VecThreshold = *vecThreshold
	cfg.AsOf = *asOf

	results, _, err := MultiLayerSearch(database, ollamaClient, question, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: search: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		payload, err := json.Marshal(map[string]any{"results": results, "count": len(results)})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: marshal json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(payload))
		return
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	fmt.Printf("Found %d results:\n\n", len(results))
	for _, r := range results {
		if r.IsStone {
			fmt.Printf("[STONE] %s (%s)\n", r.StoneTitle, fallbackText(r.StoneCategory, "uncategorized"))
			fmt.Printf("%s\n\n", truncate(r.Text, 200))
			continue
		}

		layerTag := fmt.Sprintf("%dL:%s", r.Layers, strings.Join(r.LayerSources, "+"))
		switch r.Type {
		case "chunk":
			validAt := fallbackText(r.ValidAt, "timeless")
			fmt.Printf("[%.4f] [%s] [%s] %s \u2014 %s\n", r.Distance, layerTag, validAt, r.SourceFile, r.SectionTitle)
		case "message":
			ts := formatTimestamp(r.Timestamp)
			fmt.Printf("[%.4f] [%s] [%s] %s:\n", r.Distance, layerTag, ts, r.Role)
		}
		fmt.Printf("%s\n\n", truncate(r.Text, 200))
	}
}

func fallbackText(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// RunSearchMessagesCLI handles the "kiseki search-msg" CLI command.
func RunSearchMessagesCLI(args []string, kisekiDB, ollamaHost, embedModel string) {
	fs := flag.NewFlagSet("search-msg", flag.ExitOnError)
	fts := fs.Bool("fts", false, "use FTS5 exact phrase matching instead of semantic search")
	contextMinutes := fs.Int("context", 3, "context window in minutes around matched messages")
	limit := fs.Int("limit", 5, "max results")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: query required as first positional argument\n")
		os.Exit(1)
	}

	query := fs.Arg(0)

	database, err := db.InitDB(kisekiDB)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer database.Close()

	ollamaClient := ollama.NewOllamaClient("http://"+ollamaHost, embedModel)

	if *fts {
		results, err := db.SearchMessagesFTS(database, query, *limit)
		if err != nil {
			log.Fatalf("fts search: %v", err)
		}

		if len(results) == 0 {
			fmt.Println("No exact matches found.")
			return
		}

		fmt.Printf("FTS5 matches for %q:\n\n", query)
		for _, r := range results {
			ts := formatTimestamp(r.Timestamp)
			fmt.Printf("[%s] %s:\n%s\n\n", ts, r.Role, truncate(r.Text, 300))
		}
	} else {
		contexts, err := db.SearchMessagesWithContext(database, ollamaClient, query, *limit, *contextMinutes)
		if err != nil {
			log.Fatalf("search messages: %v", err)
		}

		if len(contexts) == 0 {
			fmt.Println("No messages found.")
			return
		}

		fmt.Printf("Found %d conversation contexts:\n\n", len(contexts))
		for i, ctx := range contexts {
			fmt.Printf("─── Context %d ───\n", i+1)
			for _, m := range ctx {
				fmt.Printf("[%s] %s:\n%s\n\n", formatTimestamp(m.Timestamp), m.Role, truncate(m.Text, 400))
			}
			fmt.Println()
		}
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func formatTimestamp(ms int64) string {
	return time.Unix(ms/1000, 0).Format("2006-01-02 15:04")
}
