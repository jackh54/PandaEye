package checks

import (
	"math"
	"sort"
)

// Ring is a fixed-capacity FIFO that overwrites its oldest element. Index 0 is the oldest.
type Ring[T any] struct {
	buf   []T
	start int
	n     int
}

// NewRing returns a ring holding at most capacity elements.
func NewRing[T any](capacity int) Ring[T] {
	if capacity < 1 {
		capacity = 1
	}
	return Ring[T]{buf: make([]T, capacity)}
}

// Push appends v, dropping the oldest element when full.
func (r *Ring[T]) Push(v T) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

// Len is the number of elements.
func (r *Ring[T]) Len() int { return r.n }

// Cap is the capacity.
func (r *Ring[T]) Cap() int { return len(r.buf) }

// At returns element i (0 = oldest). It panics when i is out of range.
func (r *Ring[T]) At(i int) T {
	if i < 0 || i >= r.n {
		panic("checks: ring index out of range")
	}
	return r.buf[(r.start+i)%len(r.buf)]
}

// Last returns the newest element and false when empty.
func (r *Ring[T]) Last() (T, bool) {
	var zero T
	if r.n == 0 {
		return zero, false
	}
	return r.At(r.n - 1), true
}

// FromEnd returns the element k positions before the newest (0 = newest).
func (r *Ring[T]) FromEnd(k int) (T, bool) {
	var zero T
	if k < 0 || k >= r.n {
		return zero, false
	}
	return r.At(r.n - 1 - k), true
}

// Slice copies the elements oldest first.
func (r *Ring[T]) Slice() []T {
	out := make([]T, r.n)
	for i := range out {
		out[i] = r.At(i)
	}
	return out
}

// Reset empties the ring.
func (r *Ring[T]) Reset() { r.start, r.n = 0, 0 }

// Mean of xs (0 for none).
func Mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// StdDev is the sample standard deviation (0 for fewer than 2 values).
func StdDev(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	m := Mean(xs)
	s := 0.0
	for _, x := range xs {
		d := x - m
		s += d * d
	}
	return math.Sqrt(s / float64(len(xs)-1))
}

// Skewness is the population skewness (0 when the deviation is 0 or fewer than 3 values).
func Skewness(xs []float64) float64 {
	if len(xs) < 3 {
		return 0
	}
	m := Mean(xs)
	var m2, m3 float64
	for _, x := range xs {
		d := x - m
		m2 += d * d
		m3 += d * d * d
	}
	n := float64(len(xs))
	m2 /= n
	m3 /= n
	if m2 == 0 {
		return 0
	}
	return m3 / math.Pow(m2, 1.5)
}

// ExcessKurtosis is the population excess kurtosis (0 when the deviation is 0 or fewer than 4 values).
func ExcessKurtosis(xs []float64) float64 {
	if len(xs) < 4 {
		return 0
	}
	m := Mean(xs)
	var m2, m4 float64
	for _, x := range xs {
		d := x - m
		m2 += d * d
		m4 += d * d * d * d
	}
	n := float64(len(xs))
	m2 /= n
	m4 /= n
	if m2 == 0 {
		return 0
	}
	return m4/(m2*m2) - 3
}

// Percentile returns the p-th percentile (0..100) with linear interpolation (0 for none).
func Percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if p <= 0 {
		return s[0]
	}
	if p >= 100 {
		return s[len(s)-1]
	}
	pos := p / 100 * float64(len(s)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	return s[lo] + (s[hi]-s[lo])*(pos-float64(lo))
}

// Median of xs.
func Median(xs []float64) float64 { return Percentile(xs, 50) }

// ModeShare returns the most frequent value of xs and the share of xs equal to it.
func ModeShare(xs []float64) (mode float64, share float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	counts := map[float64]int{}
	best := 0
	for _, x := range xs {
		counts[x]++
		if c := counts[x]; c > best || c == best && x < mode {
			best, mode = c, x
		}
	}
	return mode, float64(best) / float64(len(xs))
}

// Distinct counts distinct values.
func Distinct(xs []float64) int {
	seen := map[float64]struct{}{}
	for _, x := range xs {
		seen[x] = struct{}{}
	}
	return len(seen)
}

// Clamp limits x to [lo, hi].
func Clamp(x, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, x)) }
