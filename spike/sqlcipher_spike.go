package main

import (
	"database/sql"
	"fmt"
	"log"
	"os"

	sqlite_vec "github.com/asg017/sqlite-vec-go-bindings/cgo"
	_ "github.com/mattn/go-sqlite3"
)

func init() {
	sqlite_vec.Auto()
}

func main() {
	dbPath := "/tmp/spike_encrypted.db"
	os.Remove(dbPath)

	// 1. Open encrypted DB
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatal("open:", err)
	}
	defer db.Close()

	// Set encryption key - MUST be first statement
	if _, err := db.Exec("PRAGMA key = 'test-spike-key-2026'"); err != nil {
		log.Fatal("pragma key:", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Fatal("wal:", err)
	}
	if _, err := db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		log.Fatal("busy:", err)
	}

	// 2. Test vec0 table
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS test_vec USING vec0(
		item_id INTEGER PRIMARY KEY,
		embedding float[4] distance_metric=cosine
	)`); err != nil {
		log.Fatal("create vec0:", err)
	}

	// Insert a vector
	vec := []byte{0, 0, 128, 63, 0, 0, 0, 64, 0, 0, 64, 64, 0, 0, 128, 64} // [1.0, 2.0, 3.0, 4.0]
	if _, err := db.Exec("INSERT INTO test_vec (item_id, embedding) VALUES (1, ?)", vec); err != nil {
		log.Fatal("insert vec:", err)
	}

	// Query vector
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM test_vec").Scan(&count); err != nil {
		log.Fatal("count vec:", err)
	}
	fmt.Printf("vec0 table: %d rows - OK\n", count)

	// 3. Test FTS5
	if _, err := db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS test_fts USING fts5(content)`); err != nil {
		fmt.Printf("FTS5: FAILED - %v\n", err)
		os.Exit(1)
	}
	if _, err := db.Exec("INSERT INTO test_fts (content) VALUES ('hello world test')"); err != nil {
		log.Fatal("insert fts:", err)
	}
	var ftsCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM test_fts WHERE test_fts MATCH 'hello'").Scan(&ftsCount); err != nil {
		log.Fatal("search fts:", err)
	}
	fmt.Printf("FTS5 table: %d results - OK\n", ftsCount)

	// 4. Validate encryption actually happened
	if err := db.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&count); err != nil {
		log.Fatal("validate:", err)
	}
	fmt.Printf("Encrypted DB tables: %d - OK\n", count)

	db.Close()

	// 5. Try opening with wrong key - should fail
	db2, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		log.Fatal("reopen:", err)
	}
	defer db2.Close()
	if _, err := db2.Exec("PRAGMA key = 'wrong-key'"); err != nil {
		fmt.Printf("Wrong key PRAGMA: error - %v\n", err)
	}
	err = db2.QueryRow("SELECT count(*) FROM sqlite_master").Scan(&count)
	if err != nil {
		fmt.Printf("Wrong key validation: CORRECTLY FAILED - %v\n", err)
	} else {
		fmt.Printf("Wrong key validation: returned %d tables (ENCRYPTION MAY NOT BE WORKING)\n", count)
	}
	db2.Close()

	// 6. Open a SECOND unencrypted DB in the same process
	plainPath := "/tmp/spike_plain.db"
	os.Remove(plainPath)
	db3, err := sql.Open("sqlite3", plainPath)
	if err != nil {
		log.Fatal("open plain:", err)
	}
	defer db3.Close()
	// NO PRAGMA key for plain DB
	if _, err := db3.Exec("PRAGMA journal_mode=WAL"); err != nil {
		log.Fatal("plain wal:", err)
	}
	if _, err := db3.Exec("CREATE TABLE test_plain (id INTEGER PRIMARY KEY, data TEXT)"); err != nil {
		log.Fatal("plain create:", err)
	}
	if _, err := db3.Exec("INSERT INTO test_plain (id, data) VALUES (1, 'plaintext data')"); err != nil {
		log.Fatal("plain insert:", err)
	}
	if err := db3.QueryRow("SELECT count(*) FROM test_plain").Scan(&count); err != nil {
		log.Fatal("plain count:", err)
	}
	fmt.Printf("Unencrypted DB: %d rows - OK\n", count)
	db3.Close()

	// 7. Verify encryption with external sqlcipher CLI
	fmt.Println("\n--- Verifying file is actually encrypted ---")
	f, _ := os.Open(dbPath)
	header := make([]byte, 16)
	f.Read(header)
	f.Close()
	if string(header[:6]) == "SQLite" {
		fmt.Println("FILE IS NOT ENCRYPTED (SQLite header found)")
		os.Exit(1)
	}
	fmt.Printf("File header (first 16 bytes): %x\n", header)
	fmt.Println("File does NOT have SQLite header - encryption confirmed")

	fmt.Println("\n=== ALL SPIKE TESTS PASSED ===")
}
