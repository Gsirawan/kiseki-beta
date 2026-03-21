package status

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
)

// BuildInfo holds version information passed from main at build time.
type BuildInfo struct {
	Version string
	Commit  string
	Date    string
}

// RunStatusCLI handles the "kiseki status" CLI command.
func RunStatusCLI(args []string, kisekiDB, ollamaHost, embedModel string, build BuildInfo) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output status as JSON")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	database, err := db.InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	ollamaClient := ollama.NewOllamaClient("http://"+ollamaHost, embedModel)

	st := Status(database, ollamaClient, embedModel)

	if *jsonOut {
		type jsonDateRange struct {
			Earliest string `json:"earliest"`
			Latest   string `json:"latest"`
		}
		payload, err := json.Marshal(map[string]any{
			"ollama_status": func() string {
				if st.OllamaHealthy {
					return "ok"
				}
				return "error"
			}(),
			"db_path":              kisekiDB,
			"embed_model":          st.EmbedModel,
			"schema_version":       st.SchemaVersion,
			"embed_dim_stored":     st.StoredEmbedDim,
			"embed_dim_configured": st.ConfiguredEmbedDim,
			"chunk_count":          st.TotalChunks,
			"thought_count":        st.TotalThoughts,
			"date_range": jsonDateRange{
				Earliest: st.EarliestValidAt,
				Latest:   st.LatestValidAt,
			},
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: marshal json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(payload))
		return
	}

	fmt.Println("Kiseki Status")
	fmt.Println("─────────────")
	fmt.Printf("Version:     v%s (commit: %s, built: %s)\n", build.Version, build.Commit, build.Date)

	ollamaStatus := "unhealthy"
	if st.OllamaHealthy {
		ollamaStatus = "healthy"
	}
	fmt.Printf("Ollama:      %s (%s)\n", ollamaStatus, ollamaHost)
	fmt.Printf("Embed Model: %s\n", st.EmbedModel)
	fmt.Printf("sqlite-vec:  %s\n", st.SqliteVecVersion)
	fmt.Printf("Chunks:      %d\n", st.TotalChunks)
	fmt.Printf("Thoughts:    %d\n", st.TotalThoughts)
	fmt.Printf("Schema:      v%d\n", st.SchemaVersion)
	if st.StoredEmbedDim > 0 {
		dimStatus := "ok"
		if st.StoredEmbedDim != st.ConfiguredEmbedDim {
			dimStatus = "MISMATCH"
		}
		fmt.Printf("Embed Dim:   %d (configured: %d) [%s]\n", st.StoredEmbedDim, st.ConfiguredEmbedDim, dimStatus)
	} else {
		fmt.Printf("Embed Dim:   %d (not yet stored)\n", st.ConfiguredEmbedDim)
	}

	dateRange := "none"
	if st.EarliestValidAt != "" && st.LatestValidAt != "" {
		dateRange = fmt.Sprintf("%s → %s", st.EarliestValidAt, st.LatestValidAt)
	} else if st.EarliestValidAt != "" {
		dateRange = st.EarliestValidAt
	}
	fmt.Printf("Date Range:  %s\n", dateRange)
}
