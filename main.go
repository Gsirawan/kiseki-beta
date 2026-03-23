package main

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/history"
	"github.com/Gsirawan/kiseki-beta/internal/ingest"
	"github.com/Gsirawan/kiseki-beta/internal/mark"
	"github.com/Gsirawan/kiseki-beta/internal/report"
	"github.com/Gsirawan/kiseki-beta/internal/review"
	"github.com/Gsirawan/kiseki-beta/internal/search"
	"github.com/Gsirawan/kiseki-beta/internal/serve"
	"github.com/Gsirawan/kiseki-beta/internal/status"
	"github.com/Gsirawan/kiseki-beta/internal/stone"
	"github.com/Gsirawan/kiseki-beta/internal/watch"
	"github.com/joho/godotenv"
)

// Version, Commit, and Date are set at build time via -ldflags
var Version = "0.6.0"
var Commit = "unknown"
var Date = "unknown"

func main() {
	_ = godotenv.Load()
	if home, err := os.UserHomeDir(); err == nil {
		_ = godotenv.Load(filepath.Join(home, ".config", "kiseki", ".env"))
	}
	db.LoadEmbedDimension()
	db.LoadEncryptionKey()
	history.LoadAliasesFromEnv()

	ollamaHost := getEnvOrDefault("OLLAMA_HOST", "localhost:11434")
	kisekiDB := getEnvOrDefault("KISEKI_DB", "kiseki.db")
	embedModel := getEnvOrDefault("EMBED_MODEL", "qwen3-embedding:0.6b")
	userAlias := getEnvOrDefault("USER_ALIAS", "User")
	assistantAlias := getEnvOrDefault("ASSISTANT_ALIAS", "Assistant")
	prefix := getEnvOrDefault("KISEKI_PREFIX", "kiseki")
	mailIdentity := getEnvOrDefault("KISEKI_MAIL_IDENTITY", "")
	mailPeers := getEnvOrDefault("KISEKI_MAIL_PEERS", "")
	mailDir := getEnvOrDefault("KISEKI_MAIL_DIR", "")
	entitiesPath := getEnvOrDefault("KISEKI_ENTITIES", "")
	typosPath := getEnvOrDefault("KISEKI_TYPOS", "")
	backupDir := getEnvOrDefault("KISEKI_BACKUP_DIR", "")
	if typosPath != "" {
		os.Setenv("KISEKI_TYPOS", typosPath)
	}
	if backupDir != "" {
		os.Setenv("KISEKI_BACKUP_DIR", backupDir)
	}
	_ = entitiesPath // Used by entity graph (loaded via DB tables, not directly in main)

	validateConfig(ollamaHost)
	validateMailConfig(mailIdentity, mailPeers, mailDir)
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "ingest":
		ingest.RunIngestCLI(os.Args[2:], kisekiDB, ollamaHost, embedModel)
	case "search":
		search.RunSearchCLI(os.Args[2:], kisekiDB, ollamaHost, embedModel)
	case "search-msg":
		search.RunSearchMessagesCLI(os.Args[2:], kisekiDB, ollamaHost, embedModel)
	case "history":
		history.RunHistoryCLI(os.Args[2:], kisekiDB)
	case "status":
		status.RunStatusCLI(os.Args[2:], kisekiDB, ollamaHost, embedModel, status.BuildInfo{
			Version: Version, Commit: Commit, Date: Date,
		})
	case "watch-oc":
		watch.RunWatch(os.Args[2:], kisekiDB, ollamaHost, embedModel, userAlias, assistantAlias)
	case "watch-cc":
		watch.RunWatchCC(os.Args[2:], kisekiDB, ollamaHost, embedModel, userAlias, assistantAlias)
	case "list":
		db.RunListCLI(os.Args[2:], kisekiDB)
	case "forget":
		db.RunForgetCLI(os.Args[2:], kisekiDB)
	case "mark":
		mark.RunMark(os.Args[2:], kisekiDB)
	case "stone":
		stone.RunStone(os.Args[2:], kisekiDB)
	case "review":
		review.ReviewCmd(os.Args[2:], kisekiDB)
	case "report":
		report.ReportCmd(os.Args[2:], kisekiDB)
	case "serve":
		serve.RunServeCLI(os.Args[2:], kisekiDB, ollamaHost, embedModel, prefix, mailIdentity, mailPeers, mailDir)
	case "batch-oc":
		watch.RunBatchOC(os.Args[2:], kisekiDB, ollamaHost, embedModel, userAlias, assistantAlias)
	case "batch-cc":
		watch.RunBatchCC(os.Args[2:], kisekiDB, ollamaHost, embedModel, userAlias, assistantAlias)
	case "re-embed":
		db.RunReEmbedCLI(os.Args[2:], kisekiDB, "http://"+ollamaHost, embedModel)
	case "migrate":
		db.RunMigrateCLI(os.Args[2:], kisekiDB)
	case "encrypt":
		db.RunEncryptCLI(os.Args[2:], kisekiDB)
	case "version", "-v", "--version":
		fmt.Printf("kiseki v%s (commit: %s, built: %s)\n", Version, Commit, Date)
		os.Exit(0)
	case "help", "-h", "--help":
		printUsage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `Kiseki - Personal memory system

Usage:
  kiseki <command> [options]

Commands:
  ingest     Parse and ingest file into vector database (markdown, text)
  search     Multi-layer search (entity + FTS5 + vec_chunks + vec_messages); use --legacy for vec-only
  search-msg Search messages directly (semantic + FTS5)
  history    Find all mentions of an entity in chronological order
  list       Show all ingested source files with chunk counts
  forget     Remove chunks from memory (by file, date, or all)
  mark       Set chunk/message importance (solution/key/normal)
  stone      Manage named solution/decision records
  review     Weekly review to score and mark message candidates
  report     Generate weekly business report from messages
  status     Show system status and health
  serve      Start MCP server
  watch-oc   Watch live OpenCode session and auto-ingest into Kiseki
  watch-cc   Watch live Claude Code session and auto-ingest into Kiseki
  batch-oc   Batch export OpenCode sessions to markdown + optional DB ingest
  batch-cc   Batch export Claude Code sessions to markdown + optional DB ingest
  re-embed   Re-embed all chunks and messages (for embedding model changes)
  migrate    Migrate data from legacy DB into Kiseki schema
  encrypt    Encrypt an existing plaintext database (requires KISEKI_DB_KEY)
  help       Show this help message

Examples:
  kiseki ingest --file notes.md --valid-at 2025-01-31
  kiseki ingest --file notes.txt --format text
  kiseki ingest --stdin --source "meeting-notes" < notes.txt
  kiseki search --as-of 2025-12-31 "key topic"
  kiseki search --json "key topic"
  kiseki search-msg --fts "hello world"
  kiseki history --limit 20 "person name"
  kiseki list
  kiseki list --json
  kiseki forget --file notes.md
  kiseki forget --before 2025-01-01
  kiseki mark chunk 42 solution
  kiseki mark message msg_123 key
  kiseki stone add --title "Azure Auth Fix" --category fix --tags "auth,azure"
  kiseki stone search "auth"
  kiseki stone read stone_1234567890_abcd1234
  kiseki stone list --week 2026-W08
  kiseki review --week 2026-W08
  kiseki review --from 2026-02-17 --to 2026-02-23
  kiseki report --week 2026-W08
  kiseki report --week 2026-W08 --format text
  kiseki status
  kiseki re-embed --dry-run
  kiseki re-embed --workers=4
  kiseki re-embed --force
  kiseki encrypt --dry-run
  kiseki encrypt --confirm
  kiseki batch-oc --out ./exports
  kiseki batch-cc --out ./exports --project myproject
  kiseki migrate --source nectar.db --dry-run
  kiseki migrate --source nectar.db --confirm
`)
}

// validateConfig performs early validation of environment configuration.
// Catches misconfiguration before any command runs.
func validateConfig(ollamaHost string) {
	// Validate OLLAMA_HOST is a parseable host:port
	if ollamaHost != "" {
		// Try as host:port first
		host, port, err := net.SplitHostPort(ollamaHost)
		if err != nil {
			// Maybe it's just a hostname/IP without port — try parsing as URL
			u, urlErr := url.Parse("http://" + ollamaHost)
			if urlErr != nil || u.Host == "" {
				fmt.Fprintf(os.Stderr, "Error: OLLAMA_HOST %q is not a valid host:port (e.g. localhost:11434)\n", ollamaHost)
				os.Exit(1)
			}
		} else {
			if host == "" {
				fmt.Fprintf(os.Stderr, "Error: OLLAMA_HOST %q has empty hostname\n", ollamaHost)
				os.Exit(1)
			}
			if _, err := strconv.Atoi(port); err != nil {
				fmt.Fprintf(os.Stderr, "Error: OLLAMA_HOST %q has invalid port %q\n", ollamaHost, port)
				os.Exit(1)
			}
		}
	}

	// Validate EMBED_DIM is a positive integer (if set)
	if dim := os.Getenv("EMBED_DIM"); dim != "" {
		d, err := strconv.Atoi(dim)
		if err != nil || d <= 0 {
			fmt.Fprintf(os.Stderr, "Error: EMBED_DIM %q must be a positive integer\n", dim)
			os.Exit(1)
		}
	}

	// Validate KISEKI_DB path is writable (check parent directory)
	dbPath := os.Getenv("KISEKI_DB")
	if dbPath != "" {
		// Only validate if explicitly set — default "kiseki.db" uses cwd which is always valid
		dir := dbPath
		if idx := len(dir) - 1; idx >= 0 {
			for idx > 0 && dir[idx] != '/' && dir[idx] != '\\' {
				idx--
			}
			if idx > 0 {
				dir = dir[:idx]
				if info, err := os.Stat(dir); err != nil || !info.IsDir() {
					fmt.Fprintf(os.Stderr, "Error: KISEKI_DB directory %q does not exist or is not accessible\n", dir)
					os.Exit(1)
				}
			}
		}
	}
}

func validateMailConfig(identity, peers, dir string) {
	if identity != "" && peers == "" {
		fmt.Fprintf(os.Stderr, "Warning: KISEKI_MAIL_IDENTITY=%q set but KISEKI_MAIL_PEERS is empty — mail will have no recipients\n", identity)
	}
	if peers != "" && identity == "" {
		fmt.Fprintf(os.Stderr, "Warning: KISEKI_MAIL_PEERS=%q set but KISEKI_MAIL_IDENTITY is empty — mail needs an identity\n", peers)
	}
	if (identity != "" || peers != "") && dir == "" {
		fmt.Fprintf(os.Stderr, "Warning: KISEKI_MAIL_IDENTITY/PEERS set but KISEKI_MAIL_DIR is empty — mail disabled without a directory\n")
	}
}
