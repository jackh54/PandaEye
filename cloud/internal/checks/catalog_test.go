package checks

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	pb "github.com/jackh54/PandaEye/cloud/internal/wire/pandaeyev1"
)

// docRow is one row of the CHECKS.md section 4 table.
type docRow struct {
	id, where, category, conf string
	halfLife                  int
	cap                       float64
	certain, off              bool
}

func parseScoringTable(t *testing.T) ([]docRow, map[string]float64) {
	t.Helper()
	raw, err := os.ReadFile("../../../docs/CHECKS.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(raw)
	i := strings.Index(doc, "## 4. Scoring summary")
	if i < 0 {
		t.Fatal("CHECKS.md has no section 4")
	}
	section := doc[i:]
	if j := strings.Index(section[3:], "\n## "); j >= 0 {
		section = section[:j+3]
	}
	var rows []docRow
	for _, line := range strings.Split(section, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) != 10 {
			continue
		}
		for k := range cells {
			cells[k] = strings.TrimSpace(cells[k])
		}
		if cells[1] == "Check" || strings.HasPrefix(cells[1], "---") {
			continue
		}
		r := docRow{where: cells[2], category: cells[3], conf: cells[5], certain: cells[8] == "yes"}
		r.id, r.off = strings.CutSuffix(cells[1], " (off)")
		if r.halfLife, err = strconv.Atoi(cells[6]); err != nil {
			t.Fatalf("half-life of %s: %q", r.id, cells[6])
		}
		if r.cap, err = strconv.ParseFloat(cells[7], 64); err != nil {
			t.Fatalf("cap of %s: %q", r.id, cells[7])
		}
		if cells[8] != "" && cells[8] != "yes" {
			t.Fatalf("certain of %s: %q", r.id, cells[8])
		}
		rows = append(rows, r)
	}
	capsText := section[strings.Index(section, "Family caps"):]
	capsText = capsText[strings.Index(capsText, ":")+1:]
	capsText = capsText[:strings.Index(capsText, ".")]
	caps := map[string]float64{}
	for _, m := range regexp.MustCompile(`([a-z]+) (\d+)`).FindAllStringSubmatch(capsText, -1) {
		v, _ := strconv.ParseFloat(m[2], 64)
		caps[m[1]] = v
	}
	return rows, caps
}

func TestCatalogMatchesChecksMD(t *testing.T) {
	rows, caps := parseScoringTable(t)
	if len(rows) < 30 {
		t.Fatalf("parsed only %d rows from CHECKS.md section 4", len(rows))
	}
	cat := Catalog()
	if len(cat) != len(rows) {
		t.Errorf("catalog has %d checks, CHECKS.md %d", len(cat), len(rows))
	}
	for i, r := range rows {
		s, ok := Lookup(r.id)
		if !ok {
			t.Errorf("%s: missing from the catalog", r.id)
			continue
		}
		if i < len(cat) && cat[i].ID != r.id {
			t.Errorf("order: catalog[%d] = %s, CHECKS.md has %s", i, cat[i].ID, r.id)
		}
		if string(s.Where) != r.where {
			t.Errorf("%s: where %s, doc %s", r.id, s.Where, r.where)
		}
		if want := pb.Category(pb.Category_value["CATEGORY_"+r.category]); want == pb.Category_CATEGORY_UNSPECIFIED || s.Category != want {
			t.Errorf("%s: category %v, doc %s", r.id, s.Category, r.category)
		}
		if s.HalfLife != time.Duration(r.halfLife)*time.Second {
			t.Errorf("%s: half-life %v, doc %ds", r.id, s.HalfLife, r.halfLife)
		}
		if s.Cap != r.cap {
			t.Errorf("%s: cap %v, doc %v", r.id, s.Cap, r.cap)
		}
		if s.Certain != r.certain {
			t.Errorf("%s: certain %v, doc %v", r.id, s.Certain, r.certain)
		}
		if s.EnabledDefault == r.off {
			t.Errorf("%s: enabled by default %v, doc off=%v", r.id, s.EnabledDefault, r.off)
		}
		lo, hi, isRange := strings.Cut(r.conf, "..")
		if !isRange {
			hi = lo
		}
		if l, _ := strconv.ParseFloat(lo, 64); l != s.Confidence {
			t.Errorf("%s: confidence %v, doc %s", r.id, s.Confidence, r.conf)
		}
		if h, _ := strconv.ParseFloat(hi, 64); h != s.MaxConfidence {
			t.Errorf("%s: max confidence %v, doc %s", r.id, s.MaxConfidence, r.conf)
		}
		if c, ok := FamilyCap(s.Family); !ok || s.Family != Family(r.id) {
			t.Errorf("%s: family %q has no cap (%v)", r.id, s.Family, c)
		}
	}
	if len(caps) < 12 {
		t.Fatalf("parsed only %d family caps", len(caps))
	}
	got := FamilyCaps()
	for f, v := range caps {
		if got[f] != v {
			t.Errorf("family %s: cap %v, doc %v", f, got[f], v)
		}
	}
	if len(got) != len(caps) {
		t.Errorf("catalog has %d family caps, doc %d", len(got), len(caps))
	}
}

func TestCatalogHelpers(t *testing.T) {
	if Family("autoclicker.cps") != "autoclicker" || Family("probe") != "probe" {
		t.Fatal("Family")
	}
	if _, ok := Lookup("nope.x"); ok {
		t.Fatal("unknown id found")
	}
	if s, _ := Lookup("protocol.slot"); len(s.Variants) != 2 {
		t.Fatal("protocol.slot variants")
	}
	c := Catalog()
	c[0].Cap = -1
	if s, _ := Lookup(c[0].ID); s.Cap == -1 || Catalog()[0].Cap == -1 {
		t.Fatal("Catalog must return a copy")
	}
}

func TestExperimentalListMatchesTheDoc(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "CHECKS.md"))
	if err != nil {
		t.Fatal(err)
	}
	var line string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(l, "Experimental: ") {
			line = l
		}
	}
	idx := strings.Index(string(data), line)
	rest := string(data)[idx:]
	if end := strings.Index(rest, "\n\n"); end > 0 {
		rest = rest[:end]
	}
	doc := map[string]bool{}
	for _, f := range strings.Split(strings.TrimPrefix(rest, "Experimental: "), "`") {
		if strings.Contains(f, ".") && !strings.ContainsAny(f, " ,\n") {
			doc[f] = true
		}
	}
	for _, s := range Catalog() {
		if s.Experimental != doc[s.ID] {
			t.Errorf("%s: experimental %v in code, %v in CHECKS.md 4.1", s.ID, s.Experimental, doc[s.ID])
		}
	}
	if len(doc) == 0 {
		t.Fatal("no experimental list found in CHECKS.md 4.1")
	}
}
