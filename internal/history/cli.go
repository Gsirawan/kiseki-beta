package history

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/Gsirawan/kiseki-beta/internal/db"
)

// RunHistoryCLI handles the "kiseki history" CLI command.
func RunHistoryCLI(args []string, kisekiDB string) {
	fs := flag.NewFlagSet("history", flag.ExitOnError)
	limit := fs.Int("limit", 20, "max chunks to retrieve")
	jsonOut := fs.Bool("json", false, "output results as JSON")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	if fs.NArg() < 1 {
		fmt.Fprintf(os.Stderr, "Error: entity name required as first positional argument\n")
		os.Exit(1)
	}

	entity := fs.Arg(0)

	database, err := db.InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	results, err := History(database, entity, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: history: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		type jsonMention struct {
			ChunkText  string `json:"chunk_text"`
			ValidAt    string `json:"valid_at"`
			SourceFile string `json:"source_file"`
		}
		mentions := make([]jsonMention, len(results))
		for i, r := range results {
			mentions[i] = jsonMention{
				ChunkText:  r.Text,
				ValidAt:    r.ValidAt,
				SourceFile: r.SourceFile,
			}
		}
		payload, err := json.Marshal(map[string]any{"entity": entity, "mentions": mentions})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: marshal json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(payload))
		return
	}

	for _, result := range results {
		validAtLabel := result.ValidAt
		if validAtLabel == "" {
			validAtLabel = "timeless"
		}

		fmt.Printf("[%s] %s — %s\n",
			validAtLabel, result.SourceFile, result.SectionTitle)

		text := result.Text
		if len(text) > 300 {
			text = text[:300] + "..."
		}
		fmt.Printf("%s\n", text)
		fmt.Println("---")
	}
}
