package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMedianLowerMiddleOddN(t *testing.T) {
	// runs [1,2,3,4,5] -> median 3, range 1-5 (acceptance criterion)
	vals := []float64{1, 2, 3, 4, 5}
	if got := medianLowerMiddle(vals); got != 3 {
		t.Fatalf("median = %v, want 3", got)
	}
	lo, hi := minMax(vals)
	if lo != 1 || hi != 5 {
		t.Fatalf("range = %v-%v, want 1-5", lo, hi)
	}
}

func TestMedianLowerMiddleUnsortedInput(t *testing.T) {
	vals := []float64{5, 1, 4, 2, 3}
	if got := medianLowerMiddle(vals); got != 3 {
		t.Fatalf("median = %v, want 3", got)
	}
	// Input must not be mutated by the helper.
	if vals[0] != 5 {
		t.Fatalf("input mutated: %v", vals)
	}
}

func TestMedianLowerMiddleEvenN(t *testing.T) {
	// N=4: lower-middle is the 2nd of 4 sorted values (no interpolation).
	if got := medianLowerMiddle([]float64{10, 20, 30, 40}); got != 20 {
		t.Fatalf("even-N median = %v, want 20 (lower-middle)", got)
	}
	// N=6: lower-middle is the 3rd of 6.
	if got := medianLowerMiddle([]float64{1, 2, 3, 4, 5, 6}); got != 3 {
		t.Fatalf("even-N median = %v, want 3 (lower-middle)", got)
	}
}

func TestMedianRungLowerMiddle(t *testing.T) {
	if got := medianRungLowerMiddle([]int{20000, 40000, 60000}); got != 40000 {
		t.Fatalf("median rung = %d, want 40000", got)
	}
	// Even N -> lower-middle (2nd of 4).
	if got := medianRungLowerMiddle([]int{20000, 40000, 60000, 80000}); got != 40000 {
		t.Fatalf("even-N median rung = %d, want 40000", got)
	}
}

func TestCellFormatting(t *testing.T) {
	if got := cell(nil); got != "—" {
		t.Fatalf("empty cell = %q, want em-dash", got)
	}
	if got := cell([]float64{3, 3, 3}); got != "3" {
		t.Fatalf("constant cell = %q, want 3", got)
	}
	if got := cell([]float64{1, 2, 3, 4, 5}); got != "3 (1–5)" {
		t.Fatalf("cell = %q, want \"3 (1–5)\"", got)
	}
}

// A clean server ceiling: every run server-bound, median rung from them.
func TestSustainedCeilingServerBound(t *testing.T) {
	runs := []sustainedRun{
		{ceilingRung: 40000},
		{ceilingRung: 60000},
		{ceilingRung: 60000},
	}
	r := sustainedCeiling(runs)
	if !r.hasMedian {
		t.Fatalf("expected a median")
	}
	if r.medianRung != 60000 {
		t.Fatalf("median rung = %d, want 60000", r.medianRung)
	}
	if r.loRung != 40000 || r.hiRung != 60000 {
		t.Fatalf("range = %d-%d, want 40000-60000", r.loRung, r.hiRung)
	}
	if r.serverBound != 3 || r.genBound != 0 {
		t.Fatalf("serverBound=%d genBound=%d, want 3/0", r.serverBound, r.genBound)
	}
}

// A gen-bound deciding rung is excluded from the median with a lower-bound note.
func TestSustainedCeilingGenBoundExcluded(t *testing.T) {
	runs := []sustainedRun{
		{ceilingRung: 40000},
		{ceilingRung: 60000},
		{decidingGenBound: true, lowerBound: 80000}, // excluded, lower bound only
	}
	r := sustainedCeiling(runs)
	if !r.hasMedian {
		t.Fatalf("expected a median from the two server-bound runs")
	}
	if r.serverBound != 2 {
		t.Fatalf("serverBound = %d, want 2", r.serverBound)
	}
	if r.genBound != 1 {
		t.Fatalf("genBound = %d, want 1", r.genBound)
	}
	// Lower-middle of [40000,60000] is 40000.
	if r.medianRung != 40000 {
		t.Fatalf("median rung = %d, want 40000 (lower-middle of 2)", r.medianRung)
	}
}

// Every run gen-bound -> generator ceiling, lower bound only.
func TestSustainedCeilingAllGenBound(t *testing.T) {
	runs := []sustainedRun{
		{decidingGenBound: true, lowerBound: 60000},
		{decidingGenBound: true, lowerBound: 80000},
	}
	r := sustainedCeiling(runs)
	if r.hasMedian {
		t.Fatalf("expected no median when all runs gen-bound")
	}
	if !r.allGenBound {
		t.Fatalf("expected allGenBound")
	}
	if r.lowerBound != 80000 {
		t.Fatalf("lower bound = %d, want 80000 (highest clean gen-bound rung)", r.lowerBound)
	}
}

// perRunSustained derives the per-run ceiling and the gen-bound deciding flag
// from a sweep.
func TestPerRunSustained(t *testing.T) {
	// threshold index 0. Rungs: 20k holds; 40k holds; 60k misses SLO (server-bound stop).
	sweep := []sweepStepJSON{
		{Rate: 20000, HoldsSLO: []bool{true}, GenBound: false, Failed: 0},
		{Rate: 40000, HoldsSLO: []bool{true}, GenBound: false, Failed: 0},
		{Rate: 60000, HoldsSLO: []bool{false}, GenBound: false, Failed: 0},
	}
	run := perRunSustained(sweep, 0)
	if run.ceilingRung != 40000 {
		t.Fatalf("ceiling = %d, want 40000", run.ceilingRung)
	}
	if run.decidingGenBound {
		t.Fatalf("deciding rung was server-bound, not gen-bound")
	}
}

func TestPerRunSustainedGenBoundDeciding(t *testing.T) {
	// 20k holds server-bound; 40k is gen-bound and misses SLO -> deciding gen-bound.
	sweep := []sweepStepJSON{
		{Rate: 20000, HoldsSLO: []bool{true}, GenBound: false, Failed: 0},
		{Rate: 40000, HoldsSLO: []bool{false}, GenBound: true, Failed: 0},
	}
	run := perRunSustained(sweep, 0)
	if !run.decidingGenBound {
		t.Fatalf("expected deciding rung flagged gen-bound")
	}
	if run.ceilingRung != 20000 {
		t.Fatalf("ceiling = %d, want 20000", run.ceilingRung)
	}
	if run.lowerBound != 40000 {
		t.Fatalf("lower bound = %d, want 40000", run.lowerBound)
	}
}

// A gen-bound rung never counts as a server ceiling even if holds_slo is true.
func TestPerRunSustainedGenBoundNotCeiling(t *testing.T) {
	sweep := []sweepStepJSON{
		{Rate: 20000, HoldsSLO: []bool{true}, GenBound: false, Failed: 0},
		{Rate: 40000, HoldsSLO: []bool{true}, GenBound: true, Failed: 0}, // gen-bound: not a server ceiling
	}
	run := perRunSustained(sweep, 0)
	if run.ceilingRung != 20000 {
		t.Fatalf("ceiling = %d, want 20000 (gen-bound 40k excluded)", run.ceilingRung)
	}
}

// readJSONL + render over a hand-written sample emits a well-formed fragment.
func TestReadJSONLAndRender(t *testing.T) {
	sample := `{"run":1,"pass":"fixed","leg_order":"tlang,node,bun","route":"/json","leg":"tlang","version":"v","rate":20000,"p50_ms":0.17,"p90_ms":0.37,"p99_ms":1.1,"p999_ms":1.8,"max_ms":11.3,"failed":0,"total":200000,"resp_bytes":716,"resource_rate":20000,"peak_rss_mib":12.4,"cpu_s":3.1,"cpu_s_per_mreq":15.5,"tree_procs":1,"serving_units":1,"threads":4,"cpu_topology":"disjoint","perreq":1,"clock_hz":100,"cpu_s_available":true,"sweep_slo_ms":[2.5,5,10]}
{"run":2,"pass":"fixed","leg_order":"node,bun,tlang","route":"/json","leg":"tlang","version":"v","rate":20000,"p50_ms":0.19,"p90_ms":0.40,"p99_ms":1.3,"p999_ms":2.0,"max_ms":12.0,"failed":0,"total":200000,"resp_bytes":716,"resource_rate":20000,"peak_rss_mib":12.8,"cpu_s":3.2,"cpu_s_per_mreq":16.0,"tree_procs":1,"serving_units":1,"threads":4,"cpu_topology":"disjoint","perreq":1,"clock_hz":100,"cpu_s_available":true,"sweep_slo_ms":[2.5,5,10]}
{"run":1,"pass":"ladder","leg_order":"tlang,node,bun","route":"/json","leg":"tlang","version":"v","rate":40000,"p50_ms":0,"p90_ms":0,"p99_ms":0,"p999_ms":0,"max_ms":0,"failed":0,"total":0,"resp_bytes":716,"cpu_topology":"disjoint","perreq":1,"clock_hz":100,"cpu_s_available":true,"sweep_slo_ms":[2.5,5,10],"sweep":[{"rate":20000,"p99_ms":1.1,"failed":0,"holds_slo":[true,true,true],"gen_bound":false},{"rate":40000,"p99_ms":4.0,"failed":0,"holds_slo":[false,true,true],"gen_bound":false}]}`

	dir := t.TempDir()
	path := filepath.Join(dir, "sample.jsonl")
	if err := os.WriteFile(path, []byte(sample+"\n\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err := readJSONL(path)
	if err != nil {
		t.Fatalf("readJSONL: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("read %d lines, want 3 (blank line skipped)", len(lines))
	}

	md := render(lines)
	for _, want := range []string{
		"Fixed-rate latency",
		"| /json | tlang | 2 |",
		"Max sustained rate under SLO",
		"<details>",
		"raw values",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("rendered fragment missing %q\n---\n%s", want, md)
		}
	}
}

func TestReadJSONLMissingFile(t *testing.T) {
	if _, err := readJSONL(filepath.Join(t.TempDir(), "nope.jsonl")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestReadJSONLMalformedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(path, []byte("{not json}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSONL(path); err == nil {
		t.Fatal("expected an error for a malformed line")
	}
}
