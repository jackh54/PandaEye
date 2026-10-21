package ml

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestFeatureTableMatchesDoc parses ml/FEATURES.md section 4 and asserts that names, order, defaults,
// clip ranges and the tick marker match the Go table, and that the version matches.
func TestFeatureTableMatchesDoc(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "ml", "FEATURES.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(data)
	if !strings.Contains(doc, "**Feature version: "+strconv.Itoa(FeatureVersion)+"**") {
		t.Fatalf("FEATURES.md does not declare feature version %d", FeatureVersion)
	}
	row := regexp.MustCompile("^\\| (\\d+) \\| `([a-z0-9_]+)` \\|")
	clip := regexp.MustCompile(`^\[(-?[0-9.]+), (-?[0-9.]+)\]$`)
	n := 0
	for _, line := range strings.Split(doc, "\n") {
		m := row.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx, _ := strconv.Atoi(m[1])
		if idx != n {
			t.Fatalf("row %d out of order (want %d)", idx, n)
		}
		cols := strings.Split(strings.Trim(line, "| "), " | ")
		if len(cols) < 8 {
			t.Fatalf("row %d: %d columns", idx, len(cols))
		}
		spec := table[idx]
		if m[2] != spec.name {
			t.Errorf("feature %d: doc %q, code %q", idx, m[2], spec.name)
		}
		def, err := strconv.ParseFloat(strings.TrimSpace(cols[len(cols)-2]), 32)
		if err != nil || float32(def) != spec.def {
			t.Errorf("feature %d %s: default doc %q, code %v", idx, spec.name, cols[len(cols)-2], spec.def)
		}
		c := clip.FindStringSubmatch(strings.TrimSpace(cols[len(cols)-1]))
		if c == nil {
			t.Fatalf("feature %d: clip %q", idx, cols[len(cols)-1])
		}
		lo, _ := strconv.ParseFloat(c[1], 32)
		hi, _ := strconv.ParseFloat(c[2], 32)
		if float32(lo) != spec.lo || float32(hi) != spec.hi {
			t.Errorf("feature %d %s: clip doc [%v, %v], code [%v, %v]", idx, spec.name, lo, hi, spec.lo, spec.hi)
		}
		definition := strings.Join(cols[3:len(cols)-4], " | ")
		if tick := strings.HasPrefix(definition, "tick:"); tick != spec.tick {
			t.Errorf("feature %d %s: tick-only doc %v, code %v", idx, spec.name, tick, spec.tick)
		}
		n++
	}
	if n != NumFeatures {
		t.Fatalf("doc has %d features, code %d", n, NumFeatures)
	}
	if !strings.Contains(doc, "`N = "+strconv.Itoa(NumFeatures)+"` features") {
		t.Error("FEATURES.md N differs")
	}
}

func TestFeatureNamesUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, n := range FeatureNames() {
		if seen[n] {
			t.Fatalf("duplicate feature %s", n)
		}
		seen[n] = true
	}
	if FeatureNames()[FGapBurstCount] != "gap_burst_count" || FeatureNames()[FAimPrecisionMean] != "aim_precision_mean" {
		t.Fatal("index constants out of step with the table")
	}
}
