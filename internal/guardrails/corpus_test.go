package guardrails

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

type corpusItem struct {
	ID, Split, Label, Category, Text string
	Untrusted                        bool
}

func loadCorpus(t testing.TB) []corpusItem {
	f, err := os.Open("testdata/injection_corpus.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []corpusItem
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		var it corpusItem
		if err := json.Unmarshal(sc.Bytes(), &it); err != nil {
			t.Fatal(err)
		}
		out = append(out, it)
	}
	return out
}

type evalStats struct{ tp, fp, tn, fn int }

func (s evalStats) String() string {
	p := float64(s.tp) / float64(max(1, s.tp+s.fp))
	r := float64(s.tp) / float64(max(1, s.tp+s.fn))
	fpr := float64(s.fp) / float64(max(1, s.fp+s.tn))
	return fmt.Sprintf("recall %5.1f%% (%d/%d)  precision %5.1f%%  false positives %4.1f%% (%d/%d)", 100*r, s.tp, s.tp+s.fn, 100*p, 100*fpr, s.fp, s.fp+s.tn)
}

// TestInjectionEval scores the detector on the labelled corpus at the default
// threshold. The test split is never used for tuning; its numbers are the ones
// to quote. Fails if either split regresses below the floors.
func TestInjectionEval(t *testing.T) {
	const threshold = 0.5
	items := loadCorpus(t)
	stats := map[string]*evalStats{"dev": {}, "test": {}}
	misses := map[string][]string{}
	for _, it := range items {
		r := AnalyzeInjection(it.Text, it.Untrusted)
		flagged := r.Score >= threshold
		s := stats[it.Split]
		switch {
		case it.Label == "attack" && flagged:
			s.tp++
		case it.Label == "attack":
			s.fn++
			misses[it.Split] = append(misses[it.Split], fmt.Sprintf("MISS %-22s %.2f %q", it.Category, r.Score, trunc(it.Text, 90)))
		case flagged:
			s.fp++
			misses[it.Split] = append(misses[it.Split], fmt.Sprintf("FP   %-22s %.2f %q (%s)", it.Category, r.Score, trunc(it.Text, 90), topCategories(r.Categories)))
		default:
			s.tn++
		}
	}
	for _, sp := range []string{"dev", "test"} {
		t.Logf("%-4s %s", sp, stats[sp])
		if os.Getenv("SHOW_ERRORS") != "" || sp == "dev" {
			sort.Strings(misses[sp])
			for _, m := range misses[sp] {
				t.Log("   " + m)
			}
		}
	}
	for sp, s := range stats {
		recall := float64(s.tp) / float64(max(1, s.tp+s.fn))
		fpr := float64(s.fp) / float64(max(1, s.fp+s.tn))
		if recall < recallFloor || fpr > fprCeiling {
			t.Errorf("%s split below floor: %s", sp, s)
		}
	}
}

// Floors are ratcheted up as the detector improves.
const (
	recallFloor = 0.88
	fprCeiling  = 0.04
)

func trunc(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", "⏎")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}
