package ingest

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/Gsirawan/kiseki-beta/internal/ollama"
)

// RunIngestCLI handles the "kiseki ingest" CLI command.
func RunIngestCLI(args []string, kisekiDB, ollamaHost, embedModel string) {
	fs := flag.NewFlagSet("ingest", flag.ExitOnError)
	file := fs.String("file", "", "path to file to ingest")
	validAt := fs.String("valid-at", "", "optional date for valid_at field (YYYY-MM-DD)")
	formatFlag := fs.String("format", "", "file format: markdown or text (auto-detected from extension if omitted)")
	stdinFlag := fs.Bool("stdin", false, "read content from stdin instead of --file")
	sourceFlag := fs.String("source", "", "source name when using --stdin (required with --stdin)")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	var data []byte
	var sourceName string
	var format string

	if *stdinFlag {
		// Stdin mode
		if *sourceFlag == "" {
			fmt.Fprintf(os.Stderr, "Error: --source is required when using --stdin\n")
			os.Exit(1)
		}
		if *file != "" {
			fmt.Fprintf(os.Stderr, "Error: --file and --stdin are mutually exclusive\n")
			os.Exit(1)
		}
		var err error
		data, err = io.ReadAll(os.Stdin)
		if err != nil {
			log.Fatalf("read stdin: %v", err)
		}
		sourceName = *sourceFlag
		format = "text" // default for stdin
		if *formatFlag != "" {
			format = *formatFlag
		}
	} else {
		// File mode
		if *file == "" {
			fmt.Fprintf(os.Stderr, "Error: --file is required (or use --stdin)\n")
			os.Exit(1)
		}
		var err error
		data, err = os.ReadFile(*file)
		if err != nil {
			log.Fatalf("read file: %v", err)
		}
		sourceName = *file

		if *formatFlag != "" {
			format = *formatFlag
		} else {
			detected, warn := DetectFormat(*file)
			format = detected
			if warn {
				fmt.Fprintf(os.Stderr, "Warning: unknown extension, treating as plain text. Use --format to override.\n")
			}
		}
	}

	sections := ParseContent(string(data), sourceName, format)

	fmt.Printf("Sections found in %s (format: %s):\n", sourceName, format)
	for _, section := range sections {
		wordCount := len(strings.Fields(section.Content))
		headerStr := strings.Repeat("#", section.HeaderLevel)
		marker := ""
		if wordCount > 600 {
			marker = " [will be sub-chunked]"
		}
		fmt.Printf("  %d. [%s] \"%s\" (%d words)%s\n",
			section.Sequence, headerStr, section.Title, wordCount, marker)
	}

	fmt.Print("\nProceed? [y/n]: ")
	reader := bufio.NewReader(os.Stdin)
	response, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("read input: %v", err)
	}

	response = strings.TrimSpace(strings.ToLower(response))
	if response != "y" && response != "yes" {
		fmt.Println("Cancelled.")
		os.Exit(0)
	}

	database, err := db.InitDB(kisekiDB)
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer database.Close()

	ollamaClient := ollama.NewOllamaClient("http://"+ollamaHost, embedModel)

	var result IngestResult
	if *stdinFlag {
		result, err = IngestContent(database, ollamaClient, string(data), sourceName, *validAt, format)
	} else {
		result, err = IngestFile(database, ollamaClient, sourceName, *validAt, format)
	}
	if err != nil {
		log.Fatalf("ingest: %v", err)
	}

	fmt.Printf("\nIngest complete:\n")
	fmt.Printf("  Sections: %d\n", result.SectionsFound)
	fmt.Printf("  Chunks: %d\n", result.ChunksCreated)
	fmt.Printf("  Sub-chunks: %d\n", result.SubChunksCreated)
}
