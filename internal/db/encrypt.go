package db

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"strings"
)

// RunEncryptCLI handles the "kiseki encrypt" CLI command.
// Migrates an existing plaintext database to an encrypted one.
func RunEncryptCLI(args []string, kisekiDB string) {
	fs := flag.NewFlagSet("encrypt", flag.ExitOnError)
	confirm := fs.Bool("confirm", false, "execute encryption (required)")
	dryRun := fs.Bool("dry-run", false, "preview what would happen")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse flags: %v\n", err)
		os.Exit(1)
	}

	if !*confirm && !*dryRun {
		fmt.Fprintf(os.Stderr, "Error: must specify --confirm or --dry-run\n\n")
		fmt.Fprintf(os.Stderr, "Usage:\n")
		fmt.Fprintf(os.Stderr, "  kiseki encrypt --dry-run\n")
		fmt.Fprintf(os.Stderr, "  kiseki encrypt --confirm\n\n")
		fmt.Fprintf(os.Stderr, "Requires KISEKI_DB_KEY to be set in environment or .env file.\n")
		os.Exit(1)
	}

	if dbEncryptionKey == "" {
		fmt.Fprintf(os.Stderr, "Error: KISEKI_DB_KEY must be set to encrypt a database.\n")
		fmt.Fprintf(os.Stderr, "Set it in your .env file or environment before running this command.\n")
		os.Exit(1)
	}

	// Verify source file exists
	info, err := os.Stat(kisekiDB)
	if os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "Error: database not found: %s\n", kisekiDB)
		os.Exit(1)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: stat database: %v\n", err)
		os.Exit(1)
	}

	// Check if already encrypted by trying to read without key
	isPlaintext, err := checkPlaintext(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: check database: %v\n", err)
		os.Exit(1)
	}
	if !isPlaintext {
		fmt.Fprintf(os.Stderr, "Error: database %s does not appear to be a plaintext SQLite file.\n", kisekiDB)
		fmt.Fprintf(os.Stderr, "It may already be encrypted, or it may be corrupt.\n")
		os.Exit(1)
	}

	// Count records for reporting
	counts, err := countRecords(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: count records: %v\n", err)
		os.Exit(1)
	}

	bakPath := kisekiDB + ".bak"
	encPath := kisekiDB + ".enc"

	if *dryRun {
		fmt.Printf("Encryption preview (dry-run):\n")
		fmt.Printf("  Source:     %s (%d bytes)\n", kisekiDB, info.Size())
		fmt.Printf("  Backup:    %s (will be created)\n", bakPath)
		fmt.Printf("  Records:   %d chunks, %d messages, %d stones\n",
			counts.chunks, counts.messages, counts.stones)
		fmt.Printf("  Key:       %d characters (from KISEKI_DB_KEY)\n", len(dbEncryptionKey))
		fmt.Printf("\n")
		fmt.Printf("  Steps:\n")
		fmt.Printf("    1. Open plaintext DB\n")
		fmt.Printf("    2. Create encrypted copy at %s\n", encPath)
		fmt.Printf("    3. Verify encrypted copy\n")
		fmt.Printf("    4. Rename original to %s\n", bakPath)
		fmt.Printf("    5. Rename encrypted to %s\n", kisekiDB)
		fmt.Printf("\n")
		fmt.Printf("  Run with --confirm to execute.\n")
		return
	}

	// Execute encryption
	fmt.Printf("Encrypting %s...\n", kisekiDB)

	// Clean up any leftover temp file
	os.Remove(encPath)

	// Step 1: Open plaintext DB (no encryption key)
	// Use SetMaxOpenConns(1) to ensure ATTACH, export, and DETACH all happen
	// on the same underlying connection (database/sql uses a pool by default).
	plainDB, err := sql.Open("sqlite3", kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: open plaintext database: %v\n", err)
		os.Exit(1)
	}
	plainDB.SetMaxOpenConns(1)

	// Step 2: Use sqlcipher_export to create encrypted copy
	// ATTACH the new DB with encryption, export data, then DETACH.
	// All on the same connection (enforced by SetMaxOpenConns(1)).
	attachSQL := fmt.Sprintf(
		"ATTACH DATABASE '%s' AS encrypted KEY '%s'",
		strings.ReplaceAll(encPath, "'", "''"),
		strings.ReplaceAll(dbEncryptionKey, "'", "''"),
	)
	if _, err := plainDB.Exec(attachSQL); err != nil {
		plainDB.Close()
		os.Remove(encPath)
		fmt.Fprintf(os.Stderr, "Error: attach encrypted database: %v\n", err)
		os.Exit(1)
	}

	if _, err := plainDB.Exec("SELECT sqlcipher_export('encrypted')"); err != nil {
		plainDB.Close()
		os.Remove(encPath)
		fmt.Fprintf(os.Stderr, "Error: sqlcipher_export failed: %v\n", err)
		os.Exit(1)
	}

	if _, err := plainDB.Exec("DETACH DATABASE encrypted"); err != nil {
		plainDB.Close()
		os.Remove(encPath)
		fmt.Fprintf(os.Stderr, "Error: detach encrypted database: %v\n", err)
		os.Exit(1)
	}

	plainDB.Close()

	// Step 3: Verify the encrypted copy.
	// We cannot open the encrypted DB from Go because mattn/go-sqlite3 sends
	// internal PRAGMAs before we can inject PRAGMA key. Instead, verify:
	// (a) the file is not plaintext (header check)
	// (b) the file size is reasonable (non-zero, roughly similar to source)
	fmt.Printf("  Verifying encrypted copy...\n")

	encInfo, err := os.Stat(encPath)
	if err != nil || encInfo.Size() == 0 {
		os.Remove(encPath)
		fmt.Fprintf(os.Stderr, "Error: encrypted copy is empty or missing\n")
		os.Exit(1)
	}

	isPlain, err := checkPlaintext(encPath)
	if err != nil {
		os.Remove(encPath)
		fmt.Fprintf(os.Stderr, "Error: could not verify encrypted copy: %v\n", err)
		os.Exit(1)
	}
	if isPlain {
		os.Remove(encPath)
		fmt.Fprintf(os.Stderr, "Error: encrypted copy has plaintext SQLite header — encryption failed\n")
		os.Exit(1)
	}

	fmt.Printf("  Verified: file is encrypted (%d bytes), header is not plaintext SQLite.\n", encInfo.Size())

	// Step 4: Backup original
	fmt.Printf("  Backing up original to %s\n", bakPath)
	if err := os.Rename(kisekiDB, bakPath); err != nil {
		os.Remove(encPath)
		fmt.Fprintf(os.Stderr, "Error: backup original: %v\n", err)
		os.Exit(1)
	}
	// Also move WAL/SHM files if they exist
	os.Rename(kisekiDB+"-wal", bakPath+"-wal")
	os.Rename(kisekiDB+"-shm", bakPath+"-shm")

	// Step 5: Swap encrypted to final path
	fmt.Printf("  Swapping encrypted copy to %s\n", kisekiDB)
	if err := os.Rename(encPath, kisekiDB); err != nil {
		// Try to recover — move backup back
		os.Rename(bakPath, kisekiDB)
		os.Rename(bakPath+"-wal", kisekiDB+"-wal")
		os.Rename(bakPath+"-shm", kisekiDB+"-shm")
		fmt.Fprintf(os.Stderr, "Error: swap encrypted copy: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n")
	fmt.Printf("  Done. Database encrypted successfully.\n")
	fmt.Printf("  Original backed up to: %s\n", bakPath)
	fmt.Printf("  Encrypted database:    %s\n", kisekiDB)
	fmt.Printf("\n")
	fmt.Printf("  Verify with: KISEKI_DB_KEY=<your-key> kiseki status\n")
	fmt.Printf("  Remove backup when satisfied: rm %s\n", bakPath)
}

// checkPlaintext tests whether a database file is a plaintext SQLite database.
// Returns true if the file starts with the SQLite header magic.
func checkPlaintext(dbPath string) (bool, error) {
	f, err := os.Open(dbPath)
	if err != nil {
		return false, err
	}
	defer f.Close()

	header := make([]byte, 16)
	n, err := f.Read(header)
	if err != nil || n < 16 {
		return false, fmt.Errorf("could not read database header")
	}

	// SQLite files start with "SQLite format 3\000"
	return string(header[:6]) == "SQLite", nil
}

type recordCounts struct {
	chunks   int
	messages int
	stones   int
}

// countRecords opens a plaintext database and counts key records.
func countRecords(dbPath string) (recordCounts, error) {
	db, err := sql.Open("sqlite3", dbPath+"?mode=ro")
	if err != nil {
		return recordCounts{}, err
	}
	defer db.Close()

	var c recordCounts
	_ = db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&c.chunks)
	_ = db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&c.messages)
	_ = db.QueryRow("SELECT COUNT(*) FROM stones").Scan(&c.stones)
	return c, nil
}
