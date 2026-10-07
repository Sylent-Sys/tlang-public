// Command aggregate reads the benchmark JSONL summary (one object per
// (leg,route) measurement, written by the e2e harness per design §C.5) and emits
// a Markdown fragment to stdout: per cell a median + min–max range table, the
// per-SLO-threshold max-sustained-rate ceiling, and a <details> block with the
// raw per-run values so nothing is hidden behind the median (design §C.6).
//
// It is a standalone stdlib-only package main — NOT part of the //go:build e2e
// harness — depending only on the C.5 JSONL schema. It never touches a server
// and has no test dependencies.
//
// Usage:
//
//	go run ./tests/bench/aggregate <summary.jsonl>
//	go run ./tests/bench/aggregate -in <summary.jsonl>
//
// A missing or empty input file is a fatal CLI error (nonzero exit + stderr);
// this never aborts a test because the CLI is independent of the harness.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

// sweepStepJSON mirrors the per-rung object nested in a summary line's sweep[]
// (design §C.5 / the harness summaryLine writer). Field names/tags MUST match
// the writer exactly.
type sweepStepJSON struct {
	Rate     int     `json:"rate"`
	P99Ms    float64 `json:"p99_ms"`
	Failed   int64   `json:"failed"`
	HoldsSLO []bool  `json:"holds_slo"`
	GenBound bool    `json:"gen_bound"`
}

// summaryLine mirrors one JSONL object per (leg,route) measurement (design
// §C.5). Resource fields are pointers so an omitted ("ladder") value stays nil
// rather than reading as a real zero.
type summaryLine struct {
	Run       int     `json:"run"`
	Pass      string  `json:"pass"` // "fixed" | "ladder"
	LegOrder  string  `json:"leg_order"`
	Route     string  `json:"route"`
	Leg       string  `json:"leg"`
	Version   string  `json:"version"`
	Rate      int     `json:"rate"`
	P50Ms     float64 `json:"p50_ms"`
	P90Ms     float64 `json:"p90_ms"`
	P99Ms     float64 `json:"p99_ms"`
	P999Ms    float64 `json:"p999_ms"`
	MaxMs     float64 `json:"max_ms"`
	Failed    int64   `json:"failed"`
	Total     int64   `json:"total"`
	RespBytes int     `json:"resp_bytes"`

	ResourceRate *int     `json:"resource_rate,omitempty"`
	PeakRSSMiB   *float64 `json:"peak_rss_mib,omitempty"`
	CPUSeconds   *float64 `json:"cpu_s,omitempty"`
	CPUSPerMreq  *float64 `json:"cpu_s_per_mreq,omitempty"`
	TreeProcs    *int     `json:"tree_procs,omitempty"`
	ServingUnits *int     `json:"serving_units,omitempty"`
	Threads      *int     `json:"threads,omitempty"`

	CPUTopology   string          `json:"cpu_topology"`
	Perreq        int             `json:"perreq"`
	ClockHz       int             `json:"clock_hz"`
	CPUSAvailable bool            `json:"cpu_s_available"`
	SweepSLOMs    []float64       `json:"sweep_slo_ms"`
	Sweep         []sweepStepJSON `json:"sweep,omitempty"`
}

// cellKey identifies one (route, leg) group.
type cellKey struct {
	route string
	leg   string
}

func main() {
	inFlag := flag.String("in", "", "path to the JSONL summary file (overrides a positional argument)")
	flag.Parse()

	path := *inFlag
	if path == "" && flag.NArg() > 0 {
		path = flag.Arg(0)
	}
	if path == "" {
		fmt.Fprintln(os.Stderr, "aggregate: no input file (pass a path as an argument or via -in)")
		os.Exit(2)
	}

	lines, err := readJSONL(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "aggregate: %v\n", err)
		os.Exit(1)
	}
	if len(lines) == 0 {
		fmt.Fprintf(os.Stderr, "aggregate: %s contained no JSON lines\n", path)
		os.Exit(1)
	}

	md := render(lines)
	fmt.Print(md)
}

// readJSONL reads one summaryLine per non-blank line. A missing/unreadable file
// or any malformed line is an error (fatal at the CLI level).
func readJSONL(path string) ([]summaryLine, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []summaryLine
	sc := bufio.NewScanner(f)
	// Summary lines with large sweep[] arrays can exceed the default 64 KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var line summaryLine
		if err := json.Unmarshal([]byte(raw), &line); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNo, err)
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Pure aggregation helpers (unit-tested in main_test.go)
// ---------------------------------------------------------------------------

// medianLowerMiddle returns the median of vals using the lower-middle value at
// even N (NO interpolation — honest about discreteness at small N). The input is
// not mutated. It panics only on an empty slice; callers guard for len==0.
func medianLowerMiddle(vals []float64) float64 {
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	// Lower-middle: for N=4 the index is 1 (0-based), i.e. the 2nd of 4.
	return s[(len(s)-1)/2]
}

// medianRungLowerMiddle is the integer-rung analogue of medianLowerMiddle for
// the sustained-ceiling ladder rungs.
func medianRungLowerMiddle(vals []int) int {
	s := append([]int(nil), vals...)
	sort.Ints(s)
	return s[(len(s)-1)/2]
}

// minMax returns the smallest and largest of vals.
func minMax(vals []float64) (lo, hi float64) {
	lo, hi = vals[0], vals[0]
	for _, v := range vals[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

func minMaxInt(vals []int) (lo, hi int) {
	lo, hi = vals[0], vals[0]
	for _, v := range vals[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

// sustainedRun is one run's outcome for one SLO threshold, as fed to the
// cross-run ceiling rule.
type sustainedRun struct {
	// ceilingRung is the highest ladder rate with holds_slo[t]==true &&
	// gen_bound==false for this run (0 if no rung qualified).
	ceilingRung int
	// decidingGenBound is true when the run's deciding (first failing/stopping)
	// rung was gen_bound — this run yields only a lower bound (≥ lowerBound) and
	// is excluded from the median (design §C.6).
	decidingGenBound bool
	// lowerBound is the rung the lower bound is reported at for a gen-bound run
	// (the highest clean gen_bound==true rung, i.e. "≥ R").
	lowerBound int
}

// sustainedResult is the cross-run ceiling for one SLO threshold.
type sustainedResult struct {
	// serverBound counts runs that produced a real server ceiling (fed the median).
	serverBound int
	// genBound counts runs excluded as gen-bound (lower bound only).
	genBound int
	total    int

	medianRung  int  // valid only when serverBound > 0
	loRung      int  // min server-bound rung
	hiRung      int  // max server-bound rung
	hasMedian   bool // false when every run was gen-bound (lower bound only)
	lowerBound  int  // the "≥ R" rung reported when hasMedian is false
	allGenBound bool // true when the whole cell is a generator ceiling
}

// sustainedCeiling applies the design §C.6 rule for one SLO threshold across the
// per-run outcomes: median rung (lower-middle at even N) + min–max rung range
// over the server-bound runs; gen-bound runs are excluded with a lower bound. If
// every run is gen-bound, the cell is a generator ceiling and only a lower bound
// (≥ highest clean gen-bound rung) is reported.
func sustainedCeiling(runs []sustainedRun) sustainedResult {
	res := sustainedResult{total: len(runs)}
	var serverRungs []int
	maxLowerBound := 0
	for _, r := range runs {
		if r.decidingGenBound {
			res.genBound++
			if r.lowerBound > maxLowerBound {
				maxLowerBound = r.lowerBound
			}
			continue
		}
		if r.ceilingRung > 0 {
			res.serverBound++
			serverRungs = append(serverRungs, r.ceilingRung)
		}
	}
	if len(serverRungs) > 0 {
		res.hasMedian = true
		res.medianRung = medianRungLowerMiddle(serverRungs)
		res.loRung, res.hiRung = minMaxInt(serverRungs)
		return res
	}
	// No server-bound run: a generator ceiling, lower bound only.
	res.allGenBound = true
	res.lowerBound = maxLowerBound
	return res
}

// perRunSustained derives one run's sustainedRun for SLO-threshold index t from
// its ladder sweep[] (design §C.6):
//   - ceilingRung = highest rung with holds_slo[t]==true && gen_bound==false;
//   - the deciding rung is the first rung (ascending) that stops the ladder:
//     the first rung with failed>0, else the first that fails the SLO. If that
//     deciding rung is gen_bound the run is a lower-bound-only result.
//   - lowerBound = the highest clean gen_bound==true rung (what "≥ R" reports).
func perRunSustained(sweep []sweepStepJSON, t int) sustainedRun {
	var run sustainedRun
	// Rungs are stored ascending by the harness; sort defensively on rate.
	steps := append([]sweepStepJSON(nil), sweep...)
	sort.Slice(steps, func(i, j int) bool { return steps[i].Rate < steps[j].Rate })

	for _, s := range steps {
		holds := t < len(s.HoldsSLO) && s.HoldsSLO[t]
		if holds && !s.GenBound {
			if s.Rate > run.ceilingRung {
				run.ceilingRung = s.Rate
			}
		}
		if s.GenBound && s.Rate > run.lowerBound {
			run.lowerBound = s.Rate
		}
	}

	// Deciding rung: the first (lowest-rate) rung that stops/fails. A rung stops
	// the ladder when failed>0; otherwise the first rung that misses the SLO is
	// the deciding one. If that rung is gen_bound, this run is lower-bound only.
	for _, s := range steps {
		stops := s.Failed > 0
		missesSLO := !(t < len(s.HoldsSLO) && s.HoldsSLO[t])
		if stops || missesSLO {
			if s.GenBound {
				run.decidingGenBound = true
			}
			break
		}
	}
	return run
}

// ---------------------------------------------------------------------------
// Rendering
// ---------------------------------------------------------------------------

func render(lines []summaryLine) string {
	// Partition by pass, then group by cell preserving first-seen order.
	var order []cellKey
	seen := map[cellKey]bool{}
	fixed := map[cellKey][]summaryLine{}
	ladder := map[cellKey][]summaryLine{}
	for _, l := range lines {
		k := cellKey{route: l.Route, leg: l.Leg}
		if !seen[k] {
			seen[k] = true
			order = append(order, k)
		}
		switch l.Pass {
		case "ladder":
			ladder[k] = append(ladder[k], l)
		default: // "fixed" (and any unspecified) feed the headline tables
			fixed[k] = append(fixed[k], l)
		}
	}

	var b strings.Builder
	b.WriteString("## Fixed-rate latency, RSS, CPU (median + min–max across runs)\n\n")
	b.WriteString("One vote per run per cell. Median is the lower-middle value at even N (no interpolation).\n\n")
	b.WriteString("| route | leg | runs | p50 ms | p90 ms | p99 ms | p99.9 ms | max ms | peak RSS MiB | CPU-s/Mreq |\n")
	b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
	for _, k := range order {
		runs := fixed[k]
		if len(runs) == 0 {
			continue
		}
		b.WriteString(renderFixedRow(k, runs))
	}
	b.WriteString("\n")

	// Max-sustained-rate-under-SLO section (from ladder-pass lines).
	hasLadder := false
	for _, k := range order {
		if len(ladder[k]) > 0 {
			hasLadder = true
			break
		}
	}
	if hasLadder {
		b.WriteString("## Max sustained rate under SLO (per threshold, median rung + min–max)\n\n")
		b.WriteString("Per run, the sustained ceiling for a threshold is the highest ladder rung holding the SLO with failed==0 and not generator-bound. Gen-bound runs give a lower bound only and are excluded from the median.\n\n")
		for _, k := range order {
			if len(ladder[k]) == 0 {
				continue
			}
			b.WriteString(renderSustained(k, ladder[k]))
		}
	}

	// Raw per-run <details> block so nothing hides behind the median.
	b.WriteString("## Raw per-run values\n\n")
	for _, k := range order {
		b.WriteString(renderRawDetails(k, fixed[k], ladder[k]))
	}
	return b.String()
}

// renderFixedRow aggregates one cell's fixed-rate runs into a Markdown table row
// plus any inline notes (resource_rate divergence, CPU-s availability).
func renderFixedRow(k cellKey, runs []summaryLine) string {
	p50 := collect(runs, func(l summaryLine) float64 { return l.P50Ms })
	p90 := collect(runs, func(l summaryLine) float64 { return l.P90Ms })
	p99 := collect(runs, func(l summaryLine) float64 { return l.P99Ms })
	p999 := collect(runs, func(l summaryLine) float64 { return l.P999Ms })
	maxL := collect(runs, func(l summaryLine) float64 { return l.MaxMs })

	// RSS: only runs that carry a resource sample.
	var rss []float64
	var resRates []int
	for _, l := range runs {
		if l.PeakRSSMiB != nil {
			rss = append(rss, *l.PeakRSSMiB)
		}
		if l.ResourceRate != nil {
			resRates = append(resRates, *l.ResourceRate)
		}
	}
	// CPU-s/Mreq: only resource-bearing runs with cpu_s_available==true.
	var cpu []float64
	cpuAvail := 0
	cpuTotalResource := 0
	for _, l := range runs {
		if l.CPUSPerMreq == nil {
			continue
		}
		cpuTotalResource++
		if l.CPUSAvailable {
			cpu = append(cpu, *l.CPUSPerMreq)
			cpuAvail++
		}
	}

	var notes []string
	if len(resRates) > 0 {
		lo, hi := minMaxInt(resRates)
		if lo != hi {
			notes = append(notes, fmt.Sprintf("resource_rate DIVERGES across runs (%d..%d) — RSS/CPU median spans mixed load regimes", lo, hi))
		}
	}
	if cpuTotalResource > 0 && cpuAvail < cpuTotalResource {
		notes = append(notes, fmt.Sprintf("CPU-s: %d of %d runs", cpuAvail, cpuTotalResource))
	}

	row := fmt.Sprintf("| %s | %s | %d | %s | %s | %s | %s | %s | %s | %s |\n",
		k.route, k.leg, len(runs),
		cell(p50), cell(p90), cell(p99), cell(p999), cell(maxL),
		cell(rss), cell(cpu))

	if len(notes) > 0 {
		row += fmt.Sprintf("| | | | | | | | | | %s |\n", strings.Join(notes, "; "))
	}
	return row
}

// renderSustained renders the per-SLO-threshold ceiling rows for one cell.
func renderSustained(k cellKey, ladderRuns []summaryLine) string {
	// Determine the SLO thresholds (take the first run's list; they are shared).
	var slos []float64
	for _, l := range ladderRuns {
		if len(l.SweepSLOMs) > 0 {
			slos = append([]float64(nil), l.SweepSLOMs...)
			break
		}
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("### %s · %s\n\n", k.route, k.leg))
	b.WriteString("| SLO p99 ms | median rung req/s | rung range | server-bound runs | gen-bound runs |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for ti, slo := range slos {
		var runs []sustainedRun
		for _, l := range ladderRuns {
			runs = append(runs, perRunSustained(l.Sweep, ti))
		}
		r := sustainedCeiling(runs)
		medianCell := "—"
		rangeCell := "—"
		if r.hasMedian {
			medianCell = fmt.Sprintf("%d", r.medianRung)
			if r.loRung == r.hiRung {
				rangeCell = fmt.Sprintf("%d", r.loRung)
			} else {
				rangeCell = fmt.Sprintf("%d–%d", r.loRung, r.hiRung)
			}
		} else if r.allGenBound {
			medianCell = fmt.Sprintf("≥ %d (gen ceiling)", r.lowerBound)
		}
		note := ""
		if r.genBound > 0 {
			note = fmt.Sprintf("sustained: %d of %d runs server-bound; %d runs gen-bound", r.serverBound, r.total, r.genBound)
			if r.allGenBound {
				note += " — generator ceiling, lower bound only"
			}
		}
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %d | %d |\n",
			trimFloat(slo), medianCell, rangeCell, r.serverBound, r.genBound))
		if note != "" {
			b.WriteString(fmt.Sprintf("| | | | | %s |\n", note))
		}
	}
	b.WriteString("\n")
	return b.String()
}

// renderRawDetails emits a <details> block listing every run's raw values for a
// cell so the median never hides an outlier.
func renderRawDetails(k cellKey, fixed, ladder []summaryLine) string {
	if len(fixed) == 0 && len(ladder) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("<details><summary>%s · %s raw values</summary>\n\n", k.route, k.leg))
	if len(fixed) > 0 {
		b.WriteString("Fixed-rate runs:\n\n")
		b.WriteString("| run | leg_order | rate | p50 | p90 | p99 | p99.9 | max | failed | total | peak RSS | CPU-s/Mreq | CPU-s avail | resource_rate |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n")
		for _, l := range fixed {
			b.WriteString(fmt.Sprintf("| %d | %s | %d | %s | %s | %s | %s | %s | %d | %d | %s | %s | %t | %s |\n",
				l.Run, l.LegOrder, l.Rate,
				trimFloat(l.P50Ms), trimFloat(l.P90Ms), trimFloat(l.P99Ms), trimFloat(l.P999Ms), trimFloat(l.MaxMs),
				l.Failed, l.Total,
				ptrFloat(l.PeakRSSMiB), ptrFloat(l.CPUSPerMreq), l.CPUSAvailable, ptrInt(l.ResourceRate)))
		}
		b.WriteString("\n")
	}
	if len(ladder) > 0 {
		b.WriteString("Ladder runs (per rung):\n\n")
		b.WriteString("| run | leg_order | rung req/s | p99 ms | failed | holds_slo | gen_bound |\n")
		b.WriteString("| --- | --- | --- | --- | --- | --- | --- |\n")
		for _, l := range ladder {
			for _, s := range l.Sweep {
				b.WriteString(fmt.Sprintf("| %d | %s | %d | %s | %d | %s | %t |\n",
					l.Run, l.LegOrder, s.Rate, trimFloat(s.P99Ms), s.Failed,
					boolList(s.HoldsSLO), s.GenBound))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("</details>\n\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// Small formatting helpers
// ---------------------------------------------------------------------------

func collect(runs []summaryLine, f func(summaryLine) float64) []float64 {
	out := make([]float64, 0, len(runs))
	for _, l := range runs {
		out = append(out, f(l))
	}
	return out
}

// cell formats a continuous scalar as "median (min–max)" or "—" when empty.
func cell(vals []float64) string {
	if len(vals) == 0 {
		return "—"
	}
	med := medianLowerMiddle(vals)
	lo, hi := minMax(vals)
	if lo == hi {
		return trimFloat(med)
	}
	return fmt.Sprintf("%s (%s–%s)", trimFloat(med), trimFloat(lo), trimFloat(hi))
}

// trimFloat prints a float without a trailing ".000" tail.
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	if s == "" || s == "-0" {
		return "0"
	}
	return s
}

func ptrFloat(p *float64) string {
	if p == nil {
		return "—"
	}
	return trimFloat(*p)
}

func ptrInt(p *int) string {
	if p == nil {
		return "—"
	}
	return fmt.Sprintf("%d", *p)
}

func boolList(bs []bool) string {
	parts := make([]string, len(bs))
	for i, v := range bs {
		if v {
			parts[i] = "T"
		} else {
			parts[i] = "F"
		}
	}
	return strings.Join(parts, ",")
}
