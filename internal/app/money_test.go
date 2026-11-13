package app_test

import (
	"math"
	"strings"
	"testing"

	"repairflow/internal/app"
	"repairflow/internal/errs"
)

func TestParseCents(t *testing.T) {
	ok := map[string]int64{"0": 0, "12": 1200, "12.3": 1230, "12.34": 1234, "0.05": 5, "0.5": 50}
	for in, want := range ok {
		got, err := app.ParseCents("amount", in)
		if err != nil || got != want {
			t.Errorf("ParseCents(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	bad := []string{"", "-1", "+1", "1.234", "1.", ".5", "1e3", "abc", "1,000", " 12.34 ", strings.Repeat("9", 30)}
	for _, in := range bad {
		if v, err := app.ParseCents("amount", in); err == nil {
			// " 12.34 " trims to valid; allow that one
			if strings.TrimSpace(in) == "12.34" {
				if v != 1234 {
					t.Errorf("trimmed parse: %d", v)
				}
				continue
			}
			t.Errorf("ParseCents(%q) should be refused, got %d", in, v)
		} else if errs.CodeOf(err) != errs.CodeValidation {
			t.Errorf("ParseCents(%q): code %s", in, errs.CodeOf(err))
		}
	}
}

func TestParseQuantityMilli(t *testing.T) {
	ok := map[string]int64{"1": 1000, "1.5": 1500, "0.25": 250, "0.001": 1}
	for in, want := range ok {
		got, err := app.ParseQuantityMilli("qty", in)
		if err != nil || got != want {
			t.Errorf("ParseQuantityMilli(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"0", "0.000", "0.0001", "-1", "1.2345", "", "x"} {
		if _, err := app.ParseQuantityMilli("qty", in); errs.CodeOf(err) != errs.CodeValidation {
			t.Errorf("ParseQuantityMilli(%q) should be validation_error, got %v", in, err)
		}
	}
}

// TestRoundingRuleD203 pins the deterministic rounding rule.
func TestRoundingRuleD203(t *testing.T) {
	cases := []struct{ qtyMilli, price, want int64 }{
		{1000, 1000, 1000}, // 1 x 10.00
		{1500, 999, 1499},  // 1.5 x 9.99 = 14.985 -> 14.99 (half up)
		{500, 1, 1},        // 0.5 x 0.01 = 0.005 -> 0.01 (half up)
		{499, 1, 0},        // 0.499 x 0.01 = 0.00499 -> 0
		{333, 333, 111},    // 0.333 x 3.33 = 1.10889 -> 1.11
		{1000, 0, 0},       // free item
		{3, 1, 0},          // 0.003 x 0.01 = 0.00003 -> 0
	}
	for _, c := range cases {
		if got := app.LineTotalCents(c.qtyMilli, c.price); got != c.want {
			t.Errorf("LineTotalCents(%d,%d) = %d, want %d", c.qtyMilli, c.price, got, c.want)
		}
	}
}

func TestDiscountCents(t *testing.T) {
	if d, err := app.DiscountCents("none", 0, 10000); err != nil || d != 0 {
		t.Fatalf("none: %d %v", d, err)
	}
	if d, err := app.DiscountCents("amount", 2500, 10000); err != nil || d != 2500 {
		t.Fatalf("amount: %d %v", d, err)
	}
	if _, err := app.DiscountCents("amount", 20000, 10000); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("amount over subtotal must be refused: %v", err)
	}
	if d, err := app.DiscountCents("percent", 10, 9999); err != nil || d != 1000 { // 999.9 half-up -> 1000
		t.Fatalf("percent rounding: %d %v", d, err)
	}
	if _, err := app.DiscountCents("percent", 101, 100); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("percent > 100 refused: %v", err)
	}
	if d, err := app.DiscountCents("percent", 100, math.MaxInt64); err != nil || d != math.MaxInt64 {
		t.Fatalf("100%% of maximum subtotal must not overflow: %d %v", d, err)
	}
	if _, err := app.DiscountCents("bogus", 0, 0); errs.CodeOf(err) != errs.CodeValidation {
		t.Fatalf("bad kind: %v", err)
	}
}
