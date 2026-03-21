package normalize

import (
	"bufio"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/client9/misspell"
)

// TextMessage is a minimal message type used for typo scanning.
type TextMessage struct {
	Role string
	Text string
}

// Normalizer holds a misspell replacer and optional custom typos loaded from a
// file. All state is explicit — no globals, no init().
type Normalizer struct {
	replacer    *misspell.Replacer
	customTypos map[string]string
	mu          sync.RWMutex
}

// NewNormalizer creates a Normalizer. If typosPath is non-empty the file is
// loaded; a missing file is treated as a warning (not a fatal error), but any
// other I/O error is returned.
func NewNormalizer(typosPath string) (*Normalizer, error) {
	n := &Normalizer{
		replacer:    misspell.New(),
		customTypos: make(map[string]string),
	}
	if typosPath != "" {
		if err := n.loadCustomTypos(typosPath); err != nil {
			return nil, err
		}
	}
	return n, nil
}

// Normalize corrects misspellings in text using both the misspell dictionary
// and any custom typos that were loaded at construction time.
func (n *Normalizer) Normalize(text string) string {
	if text == "" {
		return text
	}
	normalized, _ := n.replacer.Replace(text)
	normalized = n.applyCustomTypos(normalized)
	return normalized
}

// FindTypos scans messages whose Role equals userName and returns a map of
// misspelled word → corrected word using the misspell dictionary.
func (n *Normalizer) FindTypos(messages []TextMessage, userName string) map[string]string {
	typoMap := make(map[string]string)
	r := misspell.New()

	for _, msg := range messages {
		if msg.Role != userName {
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

// UpdateTyposFile appends newTypos to typosPath, skipping entries that already
// exist. After writing it reloads the custom typos into the Normalizer.
func (n *Normalizer) UpdateTyposFile(typosPath string, newTypos map[string]string) error {
	if len(newTypos) == 0 {
		return nil
	}

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
		if !existingMap[strings.TrimSpace(line)] {
			if _, err := f.WriteString(line + "\n"); err != nil {
				return err
			}
			added++
		}
	}

	if added > 0 {
		log.Printf("normalize: added %d new typos to %s", added, typosPath)
		if err := n.loadCustomTypos(typosPath); err != nil {
			return err
		}
	}

	return nil
}

// loadCustomTypos reads typo entries from typosPath into n.customTypos.
// Format per line: "  typo → correct"
// A missing file is logged as a warning and treated as empty (no error).
func (n *Normalizer) loadCustomTypos(typosPath string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	data, err := os.ReadFile(typosPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("normalize: typos file not found at %s, skipping", typosPath)
			return nil
		}
		return err
	}

	n.customTypos = make(map[string]string)

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		parts := strings.Split(line, "→")
		if len(parts) != 2 {
			continue
		}

		typo := strings.TrimSpace(parts[0])
		correct := strings.TrimSpace(parts[1])
		if typo != "" && correct != "" {
			n.customTypos[typo] = correct
		}
	}

	log.Printf("normalize: loaded %d custom typos from %s", len(n.customTypos), typosPath)
	return nil
}

// applyCustomTypos replaces known custom typos (and their title-cased forms)
// in text. Caller must not hold n.mu.
func (n *Normalizer) applyCustomTypos(text string) string {
	n.mu.RLock()
	defer n.mu.RUnlock()

	result := text
	for typo, correct := range n.customTypos {
		result = strings.ReplaceAll(result, typo, correct)
		result = strings.ReplaceAll(result, strings.Title(typo), strings.Title(correct)) //nolint:staticcheck
	}
	return result
}
