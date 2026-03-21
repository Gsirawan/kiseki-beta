package keywords

import (
	"slices"
	"testing"
)

func TestExtract_MeaningfulKeywords(t *testing.T) {
	text := "The scheduler component is running. The scheduler always runs. It manages the queue and processes the pending tasks."
	got := Extract(text, 5)

	if len(got) == 0 {
		t.Fatal("expected keywords, got none")
	}
	if !slices.Contains(got, "scheduler") {
		t.Errorf("expected 'scheduler' in keywords, got %v", got)
	}
}

func TestExtract_StopWordsRemoved(t *testing.T) {
	text := "The scheduler component is running. The scheduler always runs. It manages the queue and processes the pending tasks."
	got := Extract(text, 10)

	stopList := []string{"the", "is", "and", "he", "are", "was"}
	for _, stop := range stopList {
		if slices.Contains(got, stop) {
			t.Errorf("stop word %q should not appear in keywords %v", stop, got)
		}
	}
}

func TestExtract_EmptyInput(t *testing.T) {
	got := Extract("", 10)
	if got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
}

func TestExtract_ZeroTopN(t *testing.T) {
	got := Extract("some text here with words", 0)
	if got != nil {
		t.Errorf("expected nil for topN=0, got %v", got)
	}
}

func TestExtract_RespectsTopNLimit(t *testing.T) {
	text := "apple banana cherry date elderberry fig grape honeydew kiwi lemon mango nectarine orange papaya quince"
	got := Extract(text, 5)
	if len(got) > 5 {
		t.Errorf("expected at most 5 keywords, got %d: %v", len(got), got)
	}
}

func TestExtract_TopNLargerThanVocab(t *testing.T) {
	text := "apple banana cherry"
	got := Extract(text, 100)
	if len(got) > 3 {
		t.Errorf("expected at most 3 keywords for 3-word input, got %d: %v", len(got), got)
	}
}

func TestExtract_PunctuationStripped(t *testing.T) {
	text := "hello! world. testing, punctuation: removal;"
	got := Extract(text, 10)

	for _, kw := range got {
		for _, ch := range []string{"!", ".", ",", ":", ";"} {
			if slices.Contains([]string{ch}, string(kw[len(kw)-1])) {
				t.Errorf("keyword %q still has punctuation", kw)
			}
		}
	}
	if !slices.Contains(got, "hello") {
		t.Errorf("expected 'hello' (stripped of '!') in %v", got)
	}
	if !slices.Contains(got, "world") {
		t.Errorf("expected 'world' (stripped of '.') in %v", got)
	}
}

func TestExtract_ShortWordsFiltered(t *testing.T) {
	// "go" (2 chars), "is" (stop+2 chars), "ok" (2 chars) should all be excluded
	text := "go is ok but programming language great"
	got := Extract(text, 10)

	for _, kw := range got {
		if len(kw) < 3 {
			t.Errorf("keyword %q is shorter than 3 characters", kw)
		}
	}
}

func TestExtract_SortedByFrequency(t *testing.T) {
	// "scheduler" appears 3x, "pipeline" appears 2x, "workers" appears 1x
	text := "scheduler scheduler scheduler pipeline pipeline workers"
	got := Extract(text, 3)

	if len(got) < 3 {
		t.Fatalf("expected 3 keywords, got %d: %v", len(got), got)
	}
	if got[0] != "scheduler" {
		t.Errorf("expected 'scheduler' first (highest freq), got %q", got[0])
	}
	if got[1] != "pipeline" {
		t.Errorf("expected 'pipeline' second, got %q", got[1])
	}
	if got[2] != "workers" {
		t.Errorf("expected 'workers' third, got %q", got[2])
	}
}

func TestExtractFromPair_CombinesBothTexts(t *testing.T) {
	user := "scheduler scheduler scheduler"
	assistant := "pipeline pipeline workers"
	got := ExtractFromPair(user, assistant, 3)

	if len(got) < 1 {
		t.Fatal("expected keywords from pair, got none")
	}
	if got[0] != "scheduler" {
		t.Errorf("expected 'scheduler' first (3 occurrences), got %q", got[0])
	}
	if !slices.Contains(got, "pipeline") {
		t.Errorf("expected 'pipeline' in pair keywords %v", got)
	}
}

func TestExtractFromPair_EmptyBoth(t *testing.T) {
	got := ExtractFromPair("", "", 10)
	if got != nil {
		t.Errorf("expected nil for empty pair, got %v", got)
	}
}
