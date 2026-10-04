package guardrails

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestBenignScan measures false positives on real files an agent might read.
// BENIGN_DIRS=dir1:dir2 BENIGN_EXT=.md,.go go test -run TestBenignScan -v
func TestBenignScan(t *testing.T) {
	dirs := os.Getenv("BENIGN_DIRS")
	if dirs == "" {
		t.Skip("BENIGN_DIRS not set")
	}
	exts := strings.Split(os.Getenv("BENIGN_EXT"), ",")
	limit := 400_000
	var files, flagged int
	var hits []string
	for _, d := range strings.Split(dirs, ":") {
		_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			ok := false
			for _, x := range exts {
				if strings.HasSuffix(strings.ToLower(p), x) {
					ok = true
				}
			}
			if !ok {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil || len(b) == 0 || len(b) > limit {
				return nil
			}
			files++
			r := AnalyzeInjection(string(b), true)
			if r.Score >= 0.5 {
				flagged++
				hits = append(hits, strings.TrimPrefix(p, d)+"  "+topCategories(r.Categories)+"  "+trunc(r.Evidence, 80))
			}
			return nil
		})
	}
	sort.Strings(hits)
	for _, h := range hits {
		t.Log(h)
	}
	t.Logf("scanned %d files as untrusted tool output: %d flagged (%.2f%%)", files, flagged, 100*float64(flagged)/float64(max(1, files)))
}
