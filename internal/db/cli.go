package db

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
)

// RunListCLI handles the "kiseki list" CLI command.
func RunListCLI(args []string, kisekiDB string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	jsonOut := fs.Bool("json", false, "output results as JSON")
	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	database, err := InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	files, err := ListFiles(database)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: list files: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		payload, err := json.Marshal(files)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: marshal json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(payload))
		return
	}

	if len(files) == 0 {
		fmt.Println("No files ingested yet.")
		return
	}

	totalChunks := 0
	for _, f := range files {
		totalChunks += f.ChunkCount
	}

	fmt.Printf("Source Files (%d files, %d chunks total)\n", len(files), totalChunks)
	fmt.Println(strings.Repeat("─", 60))
	for _, f := range files {
		dateRange := ""
		if f.EarliestDate != "" && f.LatestDate != "" && f.EarliestDate != f.LatestDate {
			dateRange = fmt.Sprintf("%s → %s", f.EarliestDate[:7], f.LatestDate[:7])
		} else if f.EarliestDate != "" {
			dateRange = f.EarliestDate[:7]
		}
		if dateRange != "" {
			fmt.Printf("  %-50s %4d chunks   %s\n", f.SourceFile, f.ChunkCount, dateRange)
		} else {
			fmt.Printf("  %-50s %4d chunks\n", f.SourceFile, f.ChunkCount)
		}
	}
}

// RunForgetCLI handles the "kiseki forget" CLI command.
func RunForgetCLI(args []string, kisekiDB string) {
	fs := flag.NewFlagSet("forget", flag.ExitOnError)
	fileFlag := fs.String("file", "", "delete all chunks from this source file")
	beforeFlag := fs.String("before", "", "delete all chunks with valid_at before this date (YYYY-MM-DD)")
	allFlag := fs.Bool("all", false, "wipe entire database")
	yesFlag := fs.Bool("yes", false, "skip confirmation prompt")

	if err := fs.Parse(args); err != nil {
		log.Fatalf("parse flags: %v", err)
	}

	set := 0
	if *fileFlag != "" {
		set++
	}
	if *beforeFlag != "" {
		set++
	}
	if *allFlag {
		set++
	}
	if set == 0 {
		fmt.Fprintf(os.Stderr, "Error: one of --file, --before, or --all is required\n")
		os.Exit(1)
	}
	if set > 1 {
		fmt.Fprintf(os.Stderr, "Error: only one of --file, --before, or --all may be specified\n")
		os.Exit(1)
	}

	database, err := InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer database.Close()

	var description string
	var count int

	switch {
	case *fileFlag != "":
		count, err = CountForgetByFile(database, *fileFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: count chunks: %v\n", err)
			os.Exit(1)
		}
		description = fmt.Sprintf("all chunks from file: %s", *fileFlag)
	case *beforeFlag != "":
		count, err = CountForgetBefore(database, *beforeFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: count chunks: %v\n", err)
			os.Exit(1)
		}
		description = fmt.Sprintf("all chunks with valid_at before %s", *beforeFlag)
	case *allFlag:
		if err := database.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count); err != nil {
			fmt.Fprintf(os.Stderr, "Error: count chunks: %v\n", err)
			os.Exit(1)
		}
		description = "ALL chunks in the database"
	}

	if count == 0 {
		fmt.Printf("Nothing to delete: no chunks match (%s).\n", description)
		return
	}

	fmt.Printf("This will remove %d chunks (%s).\n", count, description)

	if !*yesFlag {
		fmt.Print("Continue? [y/N]: ")
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
	}

	var result ForgetResult
	switch {
	case *fileFlag != "":
		result, err = ForgetByFile(database, *fileFlag)
	case *beforeFlag != "":
		result, err = ForgetBefore(database, *beforeFlag)
	case *allFlag:
		result, err = ForgetAll(database)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: forget: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Done: %s\n", result.Description)
}
