package domain

import (
	"errors"
	"math"
)

// Scale is the public fixed-point scale: all metrics are stored as signed
// 64-bit integers scaled by 10^6 (parts per million), e.g. a compression
// ratio of 0.512345 is stored as 512345.
const Scale int64 = 1_000_000

// Fixed is a signed 64-bit fixed-point value.
type Fixed int64

// Errors returned by fixed-point arithmetic. These map to stable domain error
// codes in the error catalogue.
var (
	ErrFixedOverflow     = errors.New("fixed-point overflow")
	ErrFixedDivideByZero = errors.New("fixed-point divide by zero")
)

// Mul returns (a*b)/Scale with half-away-from-zero rounding and overflow checks.
func Mul(a, b Fixed) (Fixed, error) {
	if mulOverflows(int64(a), int64(b)) {
		return 0, ErrFixedOverflow
	}
	return Fixed(roundDiv(int64(a)*int64(b), Scale)), nil
}

// Div returns (a*Scale)/b with half-away-from-zero rounding, divide-by-zero and
// overflow checks.
func Div(a, b Fixed) (Fixed, error) {
	if b == 0 {
		return 0, ErrFixedDivideByZero
	}
	if scaleMulOverflows(int64(a)) {
		return 0, ErrFixedOverflow
	}
	return Fixed(roundDiv(int64(a)*Scale, int64(b))), nil
}

// Add returns a+b with overflow detection.
func Add(a, b Fixed) (Fixed, error) {
	s := int64(a) + int64(b)
	if (b > 0 && s < int64(a)) || (b < 0 && s > int64(a)) {
		return 0, ErrFixedOverflow
	}
	return Fixed(s), nil
}

// MulInt returns a*n with overflow detection. It multiplies a fixed-point value
// by a raw integer count (e.g. a time interval) without rescaling.
func MulInt(a Fixed, n int64) (Fixed, error) {
	if mulOverflows(int64(a), n) {
		return 0, ErrFixedOverflow
	}
	return Fixed(int64(a) * n), nil
}

// DivInt returns a/n with half-away-from-zero rounding and a divide-by-zero
// check. It divides a fixed-point value by a raw integer count (e.g. a time
// interval), keeping the fixed-point scale unchanged.
func DivInt(a Fixed, n int64) (Fixed, error) {
	if n == 0 {
		return 0, ErrFixedDivideByZero
	}
	return Fixed(roundDiv(int64(a), n)), nil
}

// FromRatio constructs a Fixed from an integer numerator and denominator using
// half-away-from-zero rounding.
func FromRatio(num, den int64) (Fixed, error) {
	if den == 0 {
		return 0, ErrFixedDivideByZero
	}
	if scaleMulOverflows(num) {
		return 0, ErrFixedOverflow
	}
	return Fixed(roundDiv(num*Scale, den)), nil
}

// mulOverflows reports whether a*b overflows int64.
func mulOverflows(a, b int64) bool {
	if a == 0 || b == 0 {
		return false
	}
	switch {
	case a > 0 && b > 0:
		return a > math.MaxInt64/b
	case a > 0 && b < 0:
		return b < math.MinInt64/a
	case a < 0 && b > 0:
		return a < math.MinInt64/b
	default: // both negative
		return a < math.MaxInt64/b
	}
}

// scaleMulOverflows reports whether v*Scale overflows int64.
func scaleMulOverflows(v int64) bool {
	return v > math.MaxInt64/Scale || v < math.MinInt64/Scale
}

// roundDiv returns num/den rounded half-away-from-zero.
func roundDiv(num, den int64) int64 {
	q := num / den
	r := num % den
	if r == 0 {
		return q
	}
	ar := r
	if ar < 0 {
		ar = -ar
	}
	ad := den
	if ad < 0 {
		ad = -ad
	}
	if ar*2 >= ad {
		if (num > 0) == (den > 0) {
			q++
		} else {
			q--
		}
	}
	return q
}
