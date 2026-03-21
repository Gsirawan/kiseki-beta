package normalize

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempTypos writes a typos file in the expected format to dir and returns
// its path.
func writeTempTypos(t *testing.T, dir string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, "typos.txt")
	var content string
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writeTempTypos: %v", err)
	}
	return path
}

// ---------------------------------------------------------------------------
// NewNormalizer
// ---------------------------------------------------------------------------

func TestNewNormalizer_EmptyPath(t *testing.T) {
	n, err := NewNormalizer("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n == nil {
		t.Fatal("expected non-nil Normalizer")
	}
	if n.replacer == nil {
		t.Error("replacer should be initialised")
	}
	if n.customTypos == nil {
		t.Error("customTypos map should be initialised")
	}
	if len(n.customTypos) != 0 {
		t.Errorf("expected 0 custom typos, got %d", len(n.customTypos))
	}
}

func TestNewNormalizer_WithTyposFile(t *testing.T) {
	dir := t.TempDir()
	path := writeTempTypos(t, dir, []string{
		"  teh → the",
		"  recieve → receive",
	})

	n, err := NewNormalizer(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(n.customTypos) != 2 {
		t.Errorf("expected 2 custom typos, got %d", len(n.customTypos))
	}
	if n.customTypos["teh"] != "the" {
		t.Errorf("expected teh→the, got %q", n.customTypos["teh"])
	}
	if n.customTypos["recieve"] != "receive" {
		t.Errorf("expected recieve→receive, got %q", n.customTypos["recieve"])
	}
}

func TestNewNormalizer_MissingFile_NoError(t *testing.T) {
	// A missing file should be treated as a warning, not an error.
	n, err := NewNormalizer("/nonexistent/path/typos.txt")
	if err != nil {
		t.Fatalf("expected no error for missing file, got: %v", err)
	}
	if n == nil {
		t.Fatal("expected non-nil Normalizer")
	}
}

// ---------------------------------------------------------------------------
// Normalize — misspell dictionary
// ---------------------------------------------------------------------------

func TestNormalize_FixesKnownMisspelling(t *testing.T) {
	n, err := NewNormalizer("")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// "recieve" is a well-known misspelling in the misspell dictionary.
	got := n.Normalize("I recieve the package")
	want := "I receive the package"
	if got != want {
		t.Errorf("Normalize(%q) = %q, want %q", "I recieve the package", got, want)
	}
}

func TestNormalize_EmptyString(t *testing.T) {
	n, _ := NewNormalizer("")
	if got := n.Normalize(""); got != "" {
		t.Errorf("Normalize(\"\") = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// Normalize — custom typos
// ---------------------------------------------------------------------------

func TestNormalize_AppliesCustomTypos(t *testing.T) {
	dir := t.TempDir()
	path := writeTempTypos(t, dir, []string{"  teh → the"})

	n, err := NewNormalizer(path)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	got := n.Normalize("teh quick brown fox")
	want := "the quick brown fox"
	if got != want {
		t.Errorf("Normalize(%q) = %q, want %q", "teh quick brown fox", got, want)
	}
}

func TestNormalize_CustomTypoTitleCase(t *testing.T) {
	dir := t.TempDir()
	path := writeTempTypos(t, dir, []string{"  teh → the"})

	n, err := NewNormalizer(path)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Title-cased form should also be corrected.
	got := n.Normalize("Teh quick brown fox")
	want := "The quick brown fox"
	if got != want {
		t.Errorf("Normalize(%q) = %q, want %q", "Teh quick brown fox", got, want)
	}
}

// ---------------------------------------------------------------------------
// FindTypos
// ---------------------------------------------------------------------------

func TestFindTypos_DetectsUserMisspellings(t *testing.T) {
	n, _ := NewNormalizer("")

	messages := []TextMessage{
		{Role: "user", Text: "I recieve the package"},
		{Role: "assistant", Text: "I recieve too"}, // should be ignored
	}

	typos := n.FindTypos(messages, "user")
	if _, ok := typos["recieve"]; !ok {
		t.Errorf("expected 'recieve' in typo map, got %v", typos)
	}
	// Assistant message must not contribute.
	if len(typos) > 1 {
		t.Errorf("expected only user typos, got %v", typos)
	}
}

func TestFindTypos_IgnoresNonUserMessages(t *testing.T) {
	n, _ := NewNormalizer("")

	messages := []TextMessage{
		{Role: "assistant", Text: "I recieve the package"},
	}

	typos := n.FindTypos(messages, "user")
	if len(typos) != 0 {
		t.Errorf("expected empty typo map, got %v", typos)
	}
}

func TestFindTypos_SkipsShortWords(t *testing.T) {
	n, _ := NewNormalizer("")

	// "th" is 2 chars — should be skipped even if it looks like a typo.
	messages := []TextMessage{
		{Role: "user", Text: "th"},
	}

	typos := n.FindTypos(messages, "user")
	if len(typos) != 0 {
		t.Errorf("expected empty typo map for short words, got %v", typos)
	}
}

func TestFindTypos_EmptyMessages(t *testing.T) {
	n, _ := NewNormalizer("")
	typos := n.FindTypos(nil, "user")
	if len(typos) != 0 {
		t.Errorf("expected empty map for nil messages, got %v", typos)
	}
}

// ---------------------------------------------------------------------------
// UpdateTyposFile
// ---------------------------------------------------------------------------

func TestUpdateTyposFile_AppendsNewTypos(t *testing.T) {
	dir := t.TempDir()
	path := writeTempTypos(t, dir, []string{"  teh → the"})

	n, err := NewNormalizer(path)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	newTypos := map[string]string{"recieve": "receive"}
	if err := n.UpdateTyposFile(path, newTypos); err != nil {
		t.Fatalf("UpdateTyposFile: %v", err)
	}

	// Both entries should now be in customTypos.
	if n.customTypos["teh"] != "the" {
		t.Errorf("original typo missing after update")
	}
	if n.customTypos["recieve"] != "receive" {
		t.Errorf("new typo not loaded after update")
	}
}

func TestUpdateTyposFile_NoDuplicates(t *testing.T) {
	dir := t.TempDir()
	path := writeTempTypos(t, dir, []string{"  teh → the"})

	n, err := NewNormalizer(path)
	if err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Attempt to add the same typo again.
	if err := n.UpdateTyposFile(path, map[string]string{"teh": "the"}); err != nil {
		t.Fatalf("UpdateTyposFile: %v", err)
	}

	data, _ := os.ReadFile(path)
	content := string(data)
	count := 0
	for _, line := range splitLines(content) {
		if line == "  teh → the" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 occurrence of 'teh → the', got %d\nfile:\n%s", count, content)
	}
}

func TestUpdateTyposFile_EmptyMap_NoOp(t *testing.T) {
	dir := t.TempDir()
	path := writeTempTypos(t, dir, []string{"  teh → the"})

	n, _ := NewNormalizer(path)
	if err := n.UpdateTyposFile(path, map[string]string{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUpdateTyposFile_CreatesFileIfMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new_typos.txt")

	n, _ := NewNormalizer("")
	if err := n.UpdateTyposFile(path, map[string]string{"teh": "the"}); err != nil {
		t.Fatalf("UpdateTyposFile: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file to be created: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
