package ingest

import (
	"path/filepath"
	"strings"
)

// DetectFormat returns the ingestion format based on file extension.
// Returns "markdown" for .md files, "text" for .txt, and "text" with a
// warning flag for unknown extensions.
func DetectFormat(filename string) (format string, warn bool) {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".md", ".markdown":
		return "markdown", false
	case ".txt":
		return "text", false
	default:
		return "text", true
	}
}

// ParsePlainText treats the entire content as a single section.
// The sourceName (typically the filename) is used as the section title.
func ParsePlainText(content string, sourceName string) []Section {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil
	}

	title := filepath.Base(sourceName)
	// Strip extension from title
	if ext := filepath.Ext(title); ext != "" {
		title = title[:len(title)-len(ext)]
	}

	return []Section{
		{
			Title:       title,
			HeaderLevel: 2,
			Content:     trimmed,
			Sequence:    1,
		},
	}
}

// ParseContent dispatches to the appropriate parser based on format.
// Valid formats: "markdown", "text". Defaults to "markdown" for unknown formats.
func ParseContent(content string, sourceName string, format string) []Section {
	switch strings.ToLower(format) {
	case "text", "txt", "plain":
		return ParsePlainText(content, sourceName)
	default:
		return ParseMarkdown(content)
	}
}
