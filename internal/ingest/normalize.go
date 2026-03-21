package ingest

import (
	"bufio"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Gsirawan/kiseki-beta/internal/db"
	"github.com/client9/misspell"
)

var normalizer *misspell.Replacer
var customTypos map[string]string
var typosMutex sync.RWMutex

func init() {
	normalizer = misspell.New()
	loadCustomTypos()
}

func getTyposPath() string {
	if p := os.Getenv("KISEKI_TYPOS"); p != "" {
		return p
	}
	exe, err := os.Executable()
	if err != nil {
		return "typos.txt"
	}
	return filepath.Join(filepath.Dir(exe), "typos.txt")
}

func loadCustomTypos() {
	typosMutex.Lock()
	defer typosMutex.Unlock()

	customTypos = make(map[string]string)

	typosPath := getTyposPath()
	data, err := os.ReadFile(typosPath)
	if err != nil {
		return
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.Split(line, "→")
		if len(parts) != 2 {
			continue
		}

		typo := strings.TrimSpace(parts[0])
		correct := strings.TrimSpace(parts[1])
		if typo != "" && correct != "" {
			customTypos[typo] = correct
		}
	}

	if len(customTypos) > 0 {
		log.Printf("Loaded %d custom typos from %s", len(customTypos), typosPath)
	}
}

func NormalizeText(text string) string {
	if text == "" {
		return text
	}

	// Apply misspell library (common typos)
	normalized, _ := normalizer.Replace(text)

	// Apply custom typos from typos.txt
	normalized = applyCustomTypos(normalized)

	return normalized
}

func applyCustomTypos(text string) string {
	typosMutex.RLock()
	defer typosMutex.RUnlock()

	result := text
	for typo, correct := range customTypos {
		// Case-insensitive replacement
		result = strings.ReplaceAll(result, typo, correct)
		result = strings.ReplaceAll(result, strings.Title(typo), strings.Title(correct))
		result = strings.ReplaceAll(result, strings.ToUpper(typo), strings.ToUpper(correct))
	}

	return result
}

func FindTyposInMessages(messages []db.TextMessage) map[string]string {
	typoMap := make(map[string]string)
	r := misspell.New()

	for _, msg := range messages {
		if !msg.IsUser {
			continue
		}

		words := strings.Fields(msg.Text)
		for _, word := range words {
			clean := strings.Trim(word, ".,;:!?\"'()[]{}")
			if clean == "" || len(clean) < 3 {
				continue
			}

			corrected, _ := r.Replace(clean)
			if corrected != clean && corrected != "" {
				typoMap[clean] = corrected
			}
		}
	}

	return typoMap
}

func UpdateTyposFile(newTypos map[string]string) error {
	if len(newTypos) == 0 {
		return nil
	}

	typosPath := getTyposPath()

	existing, err := os.ReadFile(typosPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	existingMap := make(map[string]bool)
	scanner := bufio.NewScanner(strings.NewReader(string(existing)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			existingMap[line] = true
		}
	}

	f, err := os.OpenFile(typosPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	added := 0
	for typo, correct := range newTypos {
		line := "  " + typo + " → " + correct
		if !existingMap[line] {
			if _, err := f.WriteString(line + "\n"); err != nil {
				return err
			}
			added++
		}
	}

	if added > 0 {
		log.Printf("Added %d new typos to %s", added, typosPath)
		loadCustomTypos()
	}

	return nil
}
