//go:build e2e

// Fixed-bucket logarithmic HDR-style histogram (tests-suites design §6.3), used
// by the benchmark suite to record nanosecond latencies and read high
// quantiles in O(1) per sample without storing every value. It is behind
// //go:build e2e so benchmark-only code stays out of the default host build
// while the quantile math is still unit-tested under -tags e2e.
//
// Layout: values are nanoseconds in [0, maxTrackable]. The histogram keeps a
// linear sub-bucket array of subBucketCount entries covering [0, subBucketCount)
// at the finest resolution, then one additional "half" sub-bucket array per
// power-of-two bucket above it, exactly the standard HDR layout. precisionBits
// is chosen so 2^precisionBits >= 10^3, giving 3 significant digits.
//
// Record clamps a value above the max to the top bucket and increments an
// overflow counter (surfaced via the report) rather than panicking. Errors
// records failed requests separately (they are not latencies).

package tests

import "math/bits"

const (
	// precisionBits = 10 -> subBucketCount = 1024 >= 10^3, i.e. 3 significant
	// digits (design §6.3).
	precisionBits  = 10
	subBucketCount = 1 << precisionBits // 1024

	// maxTrackable is 10 s expressed in nanoseconds (design §6.3). Values above
	// this are clamped to the top bucket and counted as overflows.
	maxTrackableNS = int64(10_000_000_000) // 1e10 ns
)

// histogram is a fixed-bucket logarithmic HDR-style histogram over nanosecond
// latencies. It is not safe for concurrent Record; the benchmark records from a
// single collector goroutine (design §6.2).
type histogram struct {
	// counts is indexed by the computed bucket index. Its length is derived
	// once in newHistogram from maxTrackableNS and the sub-bucket layout.
	counts []int64

	bucketCount    int // number of power-of-two buckets
	subBucketHalf  int // subBucketCount / 2
	subBucketShift uint

	total    int64 // number of recorded latency samples (clamped included)
	overflow int64 // samples at/above maxTrackable, clamped to the top bucket
	errors   int64 // failed requests (not latencies)
	max      int64 // largest recorded value (post-clamp)
}

// newHistogram builds an empty histogram sized for [0, maxTrackableNS] at the
// configured precision.
func newHistogram() *histogram {
	h := &histogram{
		subBucketHalf:  subBucketCount / 2,
		subBucketShift: 0,
	}
	// bucketCount is the number of power-of-two buckets needed so the top
	// bucket's upper bound covers maxTrackableNS. The first bucket covers
	// [0, subBucketCount); each subsequent bucket doubles the covered range.
	smallestUntrackable := int64(subBucketCount)
	h.bucketCount = 1
	for smallestUntrackable < maxTrackableNS {
		smallestUntrackable <<= 1
		h.bucketCount++
	}
	// Total slots: the first bucket contributes subBucketCount; every bucket
	// after it contributes subBucketHalf (the standard HDR packing).
	length := subBucketCount + (h.bucketCount-1)*h.subBucketHalf
	h.counts = make([]int64, length)
	return h
}

// bucketIndexOf returns the power-of-two bucket index for value (0 for values
// below subBucketCount).
func (h *histogram) bucketIndexOf(value int64) int {
	if value < int64(subBucketCount) {
		return 0
	}
	// The bucket index is how far the leading set bit is above precisionBits.
	msb := 63 - bits.LeadingZeros64(uint64(value))
	return msb - precisionBits + 1
}

// subBucketIndexOf returns the linear sub-bucket index within bucketIdx.
func (h *histogram) subBucketIndexOf(value int64, bucketIdx int) int {
	return int(value >> uint(bucketIdx))
}

// countsIndex maps a (bucket, sub-bucket) pair to the flat counts index.
func (h *histogram) countsIndex(bucketIdx, subIdx int) int {
	// Within bucket 0 the full [0, subBucketCount) range is addressable; for
	// higher buckets only the top half [subBucketHalf, subBucketCount) is new,
	// so the offset drops the first half.
	bucketBase := (bucketIdx + 1) * h.subBucketHalf
	offset := subIdx - h.subBucketHalf
	return bucketBase + offset
}

// indexOf maps a value to its flat counts index.
func (h *histogram) indexOf(value int64) int {
	b := h.bucketIndexOf(value)
	s := h.subBucketIndexOf(value, b)
	if b == 0 {
		return s
	}
	return h.countsIndex(b, s)
}

// lowestEquivalentForIndex returns the lower bound (inclusive) of the value
// range that maps to flat index i. ValueAtQuantile returns this bound.
func (h *histogram) valueFromIndex(i int) int64 {
	var bucketIdx, subIdx int
	if i < subBucketCount {
		bucketIdx = 0
		subIdx = i
	} else {
		// Invert countsIndex: i = (bucketIdx+1)*subBucketHalf + (subIdx-subBucketHalf)
		// with subIdx in [subBucketHalf, subBucketCount).
		rel := i - h.subBucketHalf // = bucketIdx*subBucketHalf + subIdx
		bucketIdx = rel / h.subBucketHalf
		subIdx = rel%h.subBucketHalf + h.subBucketHalf
	}
	return int64(subIdx) << uint(bucketIdx)
}

// Record adds a single latency sample in nanoseconds. A negative value is
// treated as 0; a value at/above maxTrackable is clamped to the top bucket and
// counted as an overflow. It never panics.
func (h *histogram) Record(ns int64) {
	if ns < 0 {
		ns = 0
	}
	if ns >= maxTrackableNS {
		h.overflow++
		ns = maxTrackableNS - 1 // clamp into the top addressable bucket
	}
	idx := h.indexOf(ns)
	if idx < 0 {
		idx = 0
	}
	if idx >= len(h.counts) {
		idx = len(h.counts) - 1
	}
	h.counts[idx]++
	h.total++
	if ns > h.max {
		h.max = ns
	}
}

// RecordError records a single failed request. Failures are counted separately
// from latencies and surfaced via Errors().
func (h *histogram) RecordError() {
	h.errors++
}

// ValueAtQuantile returns the lower bound (ns) of the bucket in which the
// ceil(q*count)-th sample falls. q is clamped to [0, 1]. Returns 0 when no
// samples were recorded.
func (h *histogram) ValueAtQuantile(q float64) int64 {
	if h.total == 0 {
		return 0
	}
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	// rank is the 1-based index of the target sample (ceil(q*count)).
	rank := int64(q*float64(h.total) + 0.5)
	if rank < 1 {
		rank = 1
	}
	if rank > h.total {
		rank = h.total
	}
	var cumulative int64
	for i, c := range h.counts {
		cumulative += c
		if cumulative >= rank {
			return h.valueFromIndex(i)
		}
	}
	return h.max
}

// Max returns the largest recorded latency (ns).
func (h *histogram) Max() int64 { return h.max }

// Count returns the number of recorded latency samples.
func (h *histogram) Count() int64 { return h.total }

// Errors returns the number of recorded failed requests.
func (h *histogram) Errors() int64 { return h.errors }

// Overflow returns the number of samples that exceeded maxTrackable and were
// clamped to the top bucket.
func (h *histogram) Overflow() int64 { return h.overflow }
