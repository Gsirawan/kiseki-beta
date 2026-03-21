package serve

import (
	"flag"
	"log"
	"os"
	"strings"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/entity"
	"github.com/Gsirawan/kiseki-beta/internal/mailbox"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
)

// RunServeCLI handles the "kiseki serve" CLI command.
func RunServeCLI(args []string, kisekiDB, ollamaHost, embedModel, prefix, mailIdentity, mailPeers, mailDir string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	database, err := db.InitDB(kisekiDB)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer database.Close()

	ollamaClient := ollama.NewOllamaClient("http://"+ollamaHost, embedModel)

	var mb *mailbox.Mailbox
	if mailIdentity != "" && mailPeers != "" && mailDir != "" {
		peers := strings.Split(mailPeers, ",")
		mb = mailbox.NewMailbox(mailIdentity, peers, mailDir)
	}

	// Entity graph: load from YAML and ingest into DB on startup
	if entitiesPath := os.Getenv("KISEKI_ENTITIES"); entitiesPath != "" {
		graph, err := entity.LoadEntityGraph(entitiesPath)
		if err != nil {
			log.Fatalf("load entity graph: %v", err)
		}
		if err := entity.IngestEntities(database, graph); err != nil {
			log.Fatalf("ingest entities: %v", err)
		}
		log.Printf("entities loaded: %d entities from %s", len(graph.Entities), entitiesPath)
	}

	if err := RunMCPServer(database, ollamaClient, embedModel, prefix, mb); err != nil {
		log.Fatalf("run MCP server: %v", err)
	}
}
