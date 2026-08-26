package domain

import "testing"

func TestFromRatioHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		name string
		num  int64
		den  int64
		want Fixed
	}{
		{"half", 1, 2, 500000},
		{"two thirds rounds up", 2, 3, 666667},
		{"one third rounds down", 1, 3, 333333},
		{"negative half rounds away", -1, 2, -500000},
		{"negative third rounds away", -1, 3, -333333},
		{"zero numerator", 0, 7, 0},
		{"whole", 3, 1, 3000000},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := FromRatio(c.num, c.den)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Fatalf("FromRatio(%d,%d)=%d want %d", c.num, c.den, got, c.want)
			}
		})
	}
}

func TestFromRatioDivideByZero(t *testing.T) {
	if _, err := FromRatio(1, 0); err != ErrFixedDivideByZero {
		t.Fatalf("got %v want %v", err, ErrFixedDivideByZero)
	}
}

func TestMulOverflow(t *testing.T) {
	big := Fixed(1 << 40)
	if _, err := Mul(big, big); err != ErrFixedOverflow {
		t.Fatalf("got %v want %v", err, ErrFixedOverflow)
	}
}

func TestMulRounding(t *testing.T) {
	// 0.5 * 0.5 = 0.25 -> 250000 exactly.
	got, err := Mul(Fixed(500000), Fixed(500000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != Fixed(250000) {
		t.Fatalf("got %d want 250000", got)
	}
}

func TestDiv(t *testing.T) {
	// 1 / 2 = 0.5 -> 500000.
	got, err := Div(Fixed(1000000), Fixed(2000000))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != Fixed(500000) {
		t.Fatalf("got %d want 500000", got)
	}
}

func TestDivByZero(t *testing.T) {
	if _, err := Div(Fixed(1), Fixed(0)); err != ErrFixedDivideByZero {
		t.Fatalf("got %v want %v", err, ErrFixedDivideByZero)
	}
}

func TestAddOverflow(t *testing.T) {
	if _, err := Add(Fixed(1<<62), Fixed(1<<62)); err != ErrFixedOverflow {
		t.Fatalf("got %v want %v", err, ErrFixedOverflow)
	}
}

func TestMulIntKeepsScale(t *testing.T) {
	// 0.5 * 3 = 1.5 -> 1500000.
	got, err := MulInt(Fixed(500000), 3)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != Fixed(1500000) {
		t.Fatalf("got %d want 1500000", got)
	}
}

func TestDivIntByRawInteger(t *testing.T) {
	// 0.5 / 2 = 0.25 -> 250000 (scale unchanged).
	got, err := DivInt(Fixed(500000), 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != Fixed(250000) {
		t.Fatalf("got %d want 250000", got)
	}
	// divide by zero.
	if _, err := DivInt(Fixed(1), 0); err != ErrFixedDivideByZero {
		t.Fatalf("got %v want %v", err, ErrFixedDivideByZero)
	}
}
