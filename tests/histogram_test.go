//go:build e2e

// Host-tagged unit test for the HDR-style histogram (tests-suites design §6.3,
// §10). It asserts ValueAtQuantile against a known distribution within the
// histogram's precision tolerance, so the quantile math is verified under
// `go test -tags e2e` without needing a container or a running server. Behind
// //go:build e2e to match histogram.go (plan.md item 10 decision).

package tests

import (
	"testing"
)

// withinPrecision reports whether got is within the histogram's 3-significant-
// digit resolution of want. The HDR layout resolves to ~1 part in
// subBucketCount within a power-of-two band, so the worst-case relative error
// of a reported bucket lower bound vs. the true value is below 1/512 ~= 0.2%.
// A 1% tolerance is a comfortable, non-brittle bound around that.
func withinPrecision(t *testing.T, label string, got, want int64) {
	t.Helper()
	if want == 0 {
		if got != 0 {
			t.Errorf("%s: got %d, want 0", label, got)
		}
		return
	}
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	rel := float64(diff) / float64(want)
	if rel > 0.01 {
		t.Errorf("%s: got %d, want ~%d (relative error %.4f > 0.01)", label, got, want, rel)
	}
}

// TestHistogram_Quantiles records a known uniform distribution and asserts the
// reported quantiles land in the expected bucket within precision tolerance.
// Values 1..1000 ms are recorded once each (in ns); the q-th quantile of a
// uniform 1..N distribution is approximately q*N.
func TestHistogram_Quantiles(t *testing.T) {
	h := newHistogram()

	const n = 1000
	const msToNS = int64(1_000_000)
	for i := int64(1); i <= n; i++ {
		h.Record(i * msToNS)
	}

	if h.Count() != n {
		t.Fatalf("Count() = %d, want %d", h.Count(), n)
	}
	if h.Errors() != 0 {
		t.Fatalf("Errors() = %d, want 0", h.Errors())
	}
	if h.Overflow() != 0 {
		t.Fatalf("Overflow() = %d, want 0", h.Overflow())
	}

	cases := []struct {
		q      float64
		wantMS int64
	}{
		{0.50, 500},
		{0.90, 900},
		{0.99, 990},
		{0.999, 999},
		{1.0, 1000},
	}
	for _, c := range cases {
		got := h.ValueAtQuantile(c.q)
		withinPrecision(t, fmtQ(c.q), got, c.wantMS*msToNS)
	}

	// Max is the largest recorded value exactly (no bucketing on Max).
	if h.Max() != n*msToNS {
		t.Fatalf("Max() = %d, want %d", h.Max(), n*msToNS)
	}
}

// TestHistogram_Empty asserts the zero-sample behavior: quantiles and Max are 0.
func TestHistogram_Empty(t *testing.T) {
	h := newHistogram()
	if got := h.ValueAtQuantile(0.99); got != 0 {
		t.Errorf("ValueAtQuantile on empty = %d, want 0", got)
	}
	if h.Max() != 0 {
		t.Errorf("Max on empty = %d, want 0", h.Max())
	}
	if h.Count() != 0 {
		t.Errorf("Count on empty = %d, want 0", h.Count())
	}
}

// TestHistogram_OverflowClamp asserts a value above maxTrackable is clamped to
// the top bucket, counted as an overflow, and never panics.
func TestHistogram_OverflowClamp(t *testing.T) {
	h := newHistogram()
	h.Record(maxTrackableNS)           // exactly at max -> overflow
	h.Record(maxTrackableNS + 1_000)   // above max -> overflow
	h.Record(maxTrackableNS * 100)     // far above -> overflow
	h.Record(5 * int64(1_000_000_000)) // 5s, in range

	if h.Count() != 4 {
		t.Fatalf("Count() = %d, want 4", h.Count())
	}
	if h.Overflow() != 3 {
		t.Fatalf("Overflow() = %d, want 3", h.Overflow())
	}
	// The top quantile must not exceed the max trackable value.
	if got := h.ValueAtQuantile(1.0); got >= maxTrackableNS {
		t.Errorf("ValueAtQuantile(1.0) = %d, want < %d (clamped)", got, maxTrackableNS)
	}
}

// TestHistogram_Errors asserts RecordError is independent of latency samples.
func TestHistogram_Errors(t *testing.T) {
	h := newHistogram()
	h.Record(1 * int64(1_000_000))
	h.RecordError()
	h.RecordError()
	if h.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", h.Count())
	}
	if h.Errors() != 2 {
		t.Fatalf("Errors() = %d, want 2", h.Errors())
	}
}

// fmtQ formats a quantile for a test label.
func fmtQ(q float64) string {
	switch q {
	case 0.50:
		return "p50"
	case 0.90:
		return "p90"
	case 0.99:
		return "p99"
	case 0.999:
		return "p99.9"
	case 1.0:
		return "p100"
	default:
		return "pX"
	}
}
