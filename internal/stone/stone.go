package stone

import (
	"crypto/rand"
	"database/sql"
	"github.com/Gsirawan/kiseki-beta/internal/db"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

type Stone struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Category      string `json:"category"`
	Problem       string `json:"problem"`
	Solution      string `json:"solution"`
	Tags          string `json:"tags"`
	ChunkIDs      string `json:"chunk_ids"`
	KeyChunkIDs   string `json:"key_chunk_ids"`
	SourceSession string `json:"source_session"`
	CreatedAt     string `json:"created_at"`
	Week          string `json:"week"`
}

type StoneInput struct {
	Title         string
	Category      string
	Problem       string
	Solution      string
	Tags          string
	ChunkIDs      string
	KeyChunkIDs   string
	SourceSession string
}

type StoneSearchOptions struct {
	Query    string
	Category string
	Week     string
	Limit    int
}

func createStoneID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("stone_%d_%s", time.Now().UTC().Unix(), hex.EncodeToString(b)), nil
}

func isoWeekString(t time.Time) string {
	y, w := t.ISOWeek()
	return fmt.Sprintf("%04d-W%02d", y, w)
}

func normalizeCSV(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parts := strings.Split(value, ",")
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		clean = append(clean, p)
	}
	return strings.Join(clean, ",")
}

func formatCSVForDisplay(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	parts := strings.Split(value, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ", ")
}

func CreateStone(db *sql.DB, input StoneInput) (Stone, error) {
	id, err := createStoneID()
	if err != nil {
		return Stone{}, fmt.Errorf("create stone id: %w", err)
	}

	now := time.Now().UTC()
	stone := Stone{
		ID:            id,
		Title:         strings.TrimSpace(input.Title),
		Category:      strings.TrimSpace(input.Category),
		Problem:       strings.TrimSpace(input.Problem),
		Solution:      strings.TrimSpace(input.Solution),
		Tags:          normalizeCSV(input.Tags),
		ChunkIDs:      normalizeCSV(input.ChunkIDs),
		KeyChunkIDs:   normalizeCSV(input.KeyChunkIDs),
		SourceSession: strings.TrimSpace(input.SourceSession),
		CreatedAt:     now.Format(time.RFC3339),
		Week:          isoWeekString(now),
	}

	if stone.Title == "" {
		return Stone{}, fmt.Errorf("title is required")
	}

	_, err = db.Exec(`
		INSERT INTO stones (id, title, category, problem, solution, tags, chunk_ids, key_chunk_ids, source_session, created_at, week)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, stone.ID, stone.Title, stone.Category, stone.Problem, stone.Solution, stone.Tags, stone.ChunkIDs, stone.KeyChunkIDs, stone.SourceSession, stone.CreatedAt, stone.Week)
	if err != nil {
		return Stone{}, fmt.Errorf("insert stone: %w", err)
	}

	return stone, nil
}

func scanStone(rows scanner) (Stone, error) {
	var s Stone
	var category sql.NullString
	var problem sql.NullString
	var solution sql.NullString
	var tags sql.NullString
	var chunkIDs sql.NullString
	var keyChunkIDs sql.NullString
	var sourceSession sql.NullString
	var week sql.NullString

	err := rows.Scan(&s.ID, &s.Title, &category, &problem, &solution, &tags, &chunkIDs, &keyChunkIDs, &sourceSession, &s.CreatedAt, &week)
	if err != nil {
		return Stone{}, err
	}

	if category.Valid {
		s.Category = category.String
	}
	if problem.Valid {
		s.Problem = problem.String
	}
	if solution.Valid {
		s.Solution = solution.String
	}
	if tags.Valid {
		s.Tags = tags.String
	}
	if chunkIDs.Valid {
		s.ChunkIDs = chunkIDs.String
	}
	if keyChunkIDs.Valid {
		s.KeyChunkIDs = keyChunkIDs.String
	}
	if sourceSession.Valid {
		s.SourceSession = sourceSession.String
	}
	if week.Valid {
		s.Week = week.String
	}

	return s, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func SearchStones(db *sql.DB, opts StoneSearchOptions) ([]Stone, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	base := `
		SELECT id, title, category, problem, solution, tags, chunk_ids, key_chunk_ids, source_session, created_at, week
		FROM stones
		WHERE 1=1`
	args := []any{}

	if strings.TrimSpace(opts.Query) != "" {
		q := strings.ToLower(strings.TrimSpace(opts.Query))
		base += ` AND (
			LOWER(title) LIKE ? OR
			LOWER(tags) LIKE ? OR
			LOWER(problem) LIKE ? OR
			LOWER(solution) LIKE ?
		)`
		like := "%" + q + "%"
		args = append(args, like, like, like, like)
	}
	if strings.TrimSpace(opts.Category) != "" {
		base += ` AND LOWER(category) = ?`
		args = append(args, strings.ToLower(strings.TrimSpace(opts.Category)))
	}
	if strings.TrimSpace(opts.Week) != "" {
		base += ` AND week = ?`
		args = append(args, strings.TrimSpace(opts.Week))
	}

	base += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := db.Query(base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stones := []Stone{}
	for rows.Next() {
		s, err := scanStone(rows)
		if err != nil {
			return nil, err
		}
		stones = append(stones, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stones, nil
}

func SearchStonesExact(db *sql.DB, query string, limit int) ([]Stone, error) {
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return []Stone{}, nil
	}
	if limit <= 0 {
		limit = 10
	}

	rows, err := db.Query(`
		SELECT id, title, category, problem, solution, tags, chunk_ids, key_chunk_ids, source_session, created_at, week
		FROM stones
		WHERE LOWER(title) = ?
		   OR INSTR(',' || REPLACE(LOWER(COALESCE(tags, '')), ' ', '') || ',', ',' || ? || ',') > 0
		ORDER BY created_at DESC
		LIMIT ?
	`, q, strings.ReplaceAll(q, " ", ""), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stones := []Stone{}
	for rows.Next() {
		s, err := scanStone(rows)
		if err != nil {
			return nil, err
		}
		stones = append(stones, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return stones, nil
}

func GetStone(db *sql.DB, id string) (Stone, error) {
	row := db.QueryRow(`
		SELECT id, title, category, problem, solution, tags, chunk_ids, key_chunk_ids, source_session, created_at, week
		FROM stones WHERE id = ?
	`, id)
	s, err := scanStone(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Stone{}, fmt.Errorf("stone not found: %s", id)
		}
		return Stone{}, err
	}
	return s, nil
}

func ListStones(db *sql.DB, week string, limit int) ([]Stone, error) {
	if limit <= 0 {
		limit = 100
	}
	if strings.TrimSpace(week) == "" {
		return SearchStones(db, StoneSearchOptions{Limit: limit})
	}
	return SearchStones(db, StoneSearchOptions{Week: strings.TrimSpace(week), Limit: limit})
}

func DeleteStone(db *sql.DB, id string) (bool, error) {
	res, err := db.Exec(`DELETE FROM stones WHERE id = ?`, id)
	if err != nil {
		return false, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected > 0, nil
}

func RunStone(args []string, kisekiDB string) {
	if len(args) < 1 {
		fmt.Fprintf(os.Stderr, "Usage: kiseki stone <add|search|read|list|delete> [options]\n")
		os.Exit(1)
	}

	cmd := args[0]
	subArgs := args[1:]

	db, err := db.InitDB(kisekiDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: init db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	switch cmd {
	case "add":
		runStoneAdd(db, subArgs)
	case "search":
		runStoneSearch(db, subArgs)
	case "read":
		runStoneRead(db, subArgs)
	case "list":
		runStoneList(db, subArgs)
	case "delete":
		runStoneDelete(db, subArgs)
	default:
		fmt.Fprintf(os.Stderr, "Unknown stone command: %s\n", cmd)
		os.Exit(1)
	}
}

func runStoneAdd(db *sql.DB, args []string) {
	fs := flag.NewFlagSet("stone add", flag.ExitOnError)
	title := fs.String("title", "", "stone title (required)")
	category := fs.String("category", "", "category (fix/decision/pattern/etc.)")
	problem := fs.String("problem", "", "problem statement")
	solution := fs.String("solution", "", "solution statement")
	tags := fs.String("tags", "", "comma-separated tags")
	chunks := fs.String("chunks", "", "comma-separated chunk/message IDs")
	keyChunks := fs.String("key-chunks", "", "comma-separated key chunk/message IDs")
	sourceSession := fs.String("source-session", "", "source session id")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse flags: %v\n", err)
		os.Exit(1)
	}

	if strings.TrimSpace(*title) == "" {
		fmt.Fprintln(os.Stderr, "Error: --title is required")
		os.Exit(1)
	}

	stone, err := CreateStone(db, StoneInput{
		Title:         *title,
		Category:      *category,
		Problem:       *problem,
		Solution:      *solution,
		Tags:          *tags,
		ChunkIDs:      *chunks,
		KeyChunkIDs:   *keyChunks,
		SourceSession: *sourceSession,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: add stone: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Stone added: %s\n", stone.ID)
}

func runStoneSearch(db *sql.DB, args []string) {
	fs := flag.NewFlagSet("stone search", flag.ExitOnError)
	category := fs.String("category", "", "filter by category")
	week := fs.String("week", "", "filter by week (YYYY-Www)")
	limit := fs.Int("limit", 20, "max results")
	jsonOut := fs.Bool("json", false, "output JSON")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse flags: %v\n", err)
		os.Exit(1)
	}

	query := ""
	if fs.NArg() > 0 {
		query = fs.Arg(0)
	}

	if strings.TrimSpace(query) == "" && strings.TrimSpace(*category) == "" && strings.TrimSpace(*week) == "" {
		fmt.Fprintln(os.Stderr, "Error: provide query or filter (--category/--week)")
		os.Exit(1)
	}

	stones, err := SearchStones(db, StoneSearchOptions{
		Query:    query,
		Category: *category,
		Week:     *week,
		Limit:    *limit,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: search stones: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		payload, err := json.Marshal(stones)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: marshal json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(payload))
		return
	}

	printStoneTable(stones)
}

func runStoneRead(db *sql.DB, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: kiseki stone read <id>")
		os.Exit(1)
	}

	stone, err := GetStone(db, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	printStoneDetails(stone)
}

func runStoneList(db *sql.DB, args []string) {
	fs := flag.NewFlagSet("stone list", flag.ExitOnError)
	week := fs.String("week", "", "filter by week (YYYY-Www)")
	limit := fs.Int("limit", 100, "max results")
	jsonOut := fs.Bool("json", false, "output JSON")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: parse flags: %v\n", err)
		os.Exit(1)
	}

	stones, err := ListStones(db, *week, *limit)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: list stones: %v\n", err)
		os.Exit(1)
	}

	if *jsonOut {
		payload, err := json.Marshal(stones)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: marshal json: %v\n", err)
			os.Exit(1)
		}
		fmt.Println(string(payload))
		return
	}

	printStoneTable(stones)
}

func runStoneDelete(db *sql.DB, args []string) {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: kiseki stone delete <id>")
		os.Exit(1)
	}

	deleted, err := DeleteStone(db, args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: delete stone: %v\n", err)
		os.Exit(1)
	}
	if !deleted {
		fmt.Fprintf(os.Stderr, "Error: stone not found: %s\n", args[0])
		os.Exit(1)
	}

	fmt.Printf("Stone deleted: %s\n", args[0])
}

func printStoneTable(stones []Stone) {
	if len(stones) == 0 {
		fmt.Println("No stones found.")
		return
	}

	headers := []string{"ID", "Title", "Category", "Created"}
	widths := []int{len(headers[0]), len(headers[1]), len(headers[2]), len(headers[3])}
	rows := make([][]string, 0, len(stones))

	for _, s := range stones {
		row := []string{s.ID, s.Title, s.Category, formatStoneDate(s.CreatedAt)}
		for i := range row {
			if len(row[i]) > widths[i] {
				widths[i] = len(row[i])
			}
		}
		rows = append(rows, row)
	}

	fmt.Printf("%s\n", tableBorder("┌", "┬", "┐", widths))
	fmt.Printf("%s\n", tableRow(headers, widths))
	fmt.Printf("%s\n", tableBorder("├", "┼", "┤", widths))
	for i, row := range rows {
		fmt.Printf("%s\n", tableRow(row, widths))
		if i == len(rows)-1 {
			fmt.Printf("%s\n", tableBorder("└", "┴", "┘", widths))
		}
	}
}

func printStoneDetails(s Stone) {
	width := 76

	fmt.Printf("%s\n", "╭"+strings.Repeat("─", width-2)+"╮")
	fmt.Printf("│ %s%s│\n", fmt.Sprintf("STONE: %s", s.Title), strings.Repeat(" ", max(0, width-3-len(fmt.Sprintf("STONE: %s", s.Title)))))
	fmt.Printf("%s\n", "├"+strings.Repeat("─", width-2)+"┤")

	printStoneLine(width, fmt.Sprintf("Category: %s", fallbackText(s.Category, "-")))
	printStoneLine(width, fmt.Sprintf("Created: %s", formatStoneDate(s.CreatedAt)))
	printStoneLine(width, fmt.Sprintf("Week: %s", fallbackText(s.Week, "-")))
	printStoneLine(width, "")
	printStoneLine(width, "Problem:")
	printWrappedStone(width, s.Problem)
	printStoneLine(width, "")
	printStoneLine(width, "Solution:")
	printWrappedStone(width, s.Solution)
	printStoneLine(width, "")
	printStoneLine(width, fmt.Sprintf("Tags: %s", fallbackText(formatCSVForDisplay(s.Tags), "-")))
	printStoneLine(width, fmt.Sprintf("Chunks: %s", fallbackText(formatCSVForDisplay(s.ChunkIDs), "-")))
	if strings.TrimSpace(s.KeyChunkIDs) != "" {
		printStoneLine(width, fmt.Sprintf("Key Chunks: %s", formatCSVForDisplay(s.KeyChunkIDs)))
	}

	fmt.Printf("%s\n", "╰"+strings.Repeat("─", width-2)+"╯")
}

func printStoneLine(width int, text string) {
	if len(text) > width-4 {
		text = text[:width-7] + "..."
	}
	fmt.Printf("│ %s%s│\n", text, strings.Repeat(" ", max(0, width-3-len(text))))
}

func printWrappedStone(width int, text string) {
	content := strings.TrimSpace(text)
	if content == "" {
		printStoneLine(width, "  -")
		return
	}
	for _, line := range wrapText(content, width-6) {
		printStoneLine(width, "  "+line)
	}
}

func wrapText(text string, maxWidth int) []string {
	if maxWidth <= 0 {
		return []string{text}
	}
	words := strings.Fields(text)
	if len(words) == 0 {
		return []string{""}
	}

	lines := []string{}
	current := words[0]
	for _, w := range words[1:] {
		if len(current)+1+len(w) <= maxWidth {
			current += " " + w
			continue
		}
		lines = append(lines, current)
		current = w
	}
	lines = append(lines, current)
	return lines
}

func tableBorder(left, mid, right string, widths []int) string {
	parts := make([]string, 0, len(widths)+2)
	parts = append(parts, left)
	for i, w := range widths {
		parts = append(parts, strings.Repeat("─", w+2))
		if i == len(widths)-1 {
			parts = append(parts, right)
		} else {
			parts = append(parts, mid)
		}
	}
	return strings.Join(parts, "")
}

func tableRow(cols []string, widths []int) string {
	parts := []string{"│"}
	for i, col := range cols {
		parts = append(parts, " "+col+strings.Repeat(" ", widths[i]-len(col)+1), "│")
	}
	return strings.Join(parts, "")
}

func formatStoneDate(createdAt string) string {
	if len(createdAt) >= 10 {
		return createdAt[:10]
	}
	return createdAt
}

func fallbackText(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
