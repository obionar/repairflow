package app

import (
	"fmt"
	"strings"

	"repairflow/internal/errs"
)

// Money and quantity handling: integer cents, decimal quantities and explicit rounding.
//
// All persisted money is integer EUR cents; all quantities are integer
// thousandths (quantity_milli). Parsing is pure string arithmetic — floating
// point never touches money or quantities anywhere in this codebase.

const (
	// Currency is the fixed v0.1 currency binding.
	Currency = "EUR"
	// UnitPieces is the default unit of measure.
	UnitPieces = "pcs"

	maxCents = int64(100_000_000_00) // sanity bound: 100 million EUR
	maxMilli = int64(1_000_000_000)  // sanity bound: 1 million units
)

// ParseCents parses a non-negative decimal amount ("12", "12.34", "0.5") into
// integer cents. More than two decimals, signs, exponents and separators are
// refused.
func ParseCents(field, value string) (int64, error) {
	value = strings.TrimSpace(value)
	whole, frac, err := parseDecimal(field, value, 2)
	if err != nil {
		return 0, err
	}
	cents := whole*100 + frac
	if cents > maxCents {
		return 0, errs.New(errs.CodeValidation, "%s is out of range (max %s)", field, FormatCents(maxCents))
	}
	return cents, nil
}

// ParseQuantityMilli parses a positive quantity ("1", "1.5", "0.25") into
// integer thousandths (1 pc = 1000).
func ParseQuantityMilli(field, value string) (int64, error) {
	value = strings.TrimSpace(value)
	whole, frac, err := parseDecimal(field, value, 3)
	if err != nil {
		return 0, err
	}
	milli := whole*1000 + frac
	if milli <= 0 {
		return 0, errs.New(errs.CodeValidation, "%s must be greater than zero", field)
	}
	if milli > maxMilli {
		return 0, errs.New(errs.CodeValidation, "%s is out of range", field)
	}
	return milli, nil
}

// parseDecimal parses a non-negative decimal string into a whole part and a
// fraction scaled to 10^maxFrac (zero-padded/truncated at maxFrac digits).
// Both parts are non-negative; anything unusual is refused loudly.
func parseDecimal(field, value string, maxFrac int) (int64, int64, error) {
	if value == "" {
		return 0, 0, errs.New(errs.CodeValidation, "%s must not be empty", field)
	}
	if strings.HasPrefix(value, "-") || strings.HasPrefix(value, "+") {
		return 0, 0, errs.New(errs.CodeValidation, "%s must not carry a sign (got %q)", field, value)
	}
	whole, frac, hasFrac := strings.Cut(value, ".")
	if whole == "" {
		return 0, 0, errs.New(errs.CodeValidation, "%s has no integer part (got %q)", field, value)
	}
	for _, r := range whole {
		if r < '0' || r > '9' {
			return 0, 0, errs.New(errs.CodeValidation, "%s contains non-digit characters (got %q)", field, value)
		}
	}
	w := int64(0)
	for _, r := range whole {
		w = w*10 + int64(r-'0')
		if w > maxCents*1000 {
			return 0, 0, errs.New(errs.CodeValidation, "%s is out of range", field)
		}
	}
	f := int64(0)
	if hasFrac {
		if frac == "" {
			return 0, 0, errs.New(errs.CodeValidation, "%s has an empty fraction (got %q)", field, value)
		}
		if len(frac) > maxFrac {
			return 0, 0, errs.New(errs.CodeValidation, "%s supports at most %d decimal places (got %q)", field, maxFrac, value)
		}
		for _, r := range frac {
			if r < '0' || r > '9' {
				return 0, 0, errs.New(errs.CodeValidation, "%s contains non-digit characters (got %q)", field, value)
			}
		}
		for i := 0; i < maxFrac; i++ {
			f *= 10
			if i < len(frac) {
				f += int64(frac[i] - '0')
			}
		}
	}
	return w, f, nil
}

// LineTotalCents computes one line total using the documented rounding rule:
// round-half-up(quantity_milli × unit_price_cents / 1000), integer math only.
func LineTotalCents(quantityMilli, unitPriceCents int64) int64 {
	return (quantityMilli*unitPriceCents + 500) / 1000
}

// DiscountCents computes the discount for a subtotal using the same rule:
// amount discounts are capped at the subtotal; percent discounts round
// half-up (0..100). kind is none|amount|percent.
func DiscountCents(kind string, value, subtotalCents int64) (int64, error) {
	switch kind {
	case "none":
		return 0, nil
	case "amount":
		if value < 0 || subtotalCents < 0 {
			return 0, errs.New(errs.CodeValidation, "discount amount and subtotal must not be negative")
		}
		if value > subtotalCents {
			return 0, errs.New(errs.CodeValidation, "discount amount %s exceeds the subtotal %s", FormatCents(value), FormatCents(subtotalCents))
		}
		return value, nil
	case "percent":
		if value < 0 || value > 100 || subtotalCents < 0 {
			return 0, errs.New(errs.CodeValidation, "discount percent must be 0-100 and subtotal non-negative")
		}
		// Split whole/remainder so a large valid subtotal never multiplies overflow.
		return (subtotalCents/100)*value + ((subtotalCents%100)*value+50)/100, nil
	default:
		return 0, errs.New(errs.CodeValidation, "unknown discount kind %q (valid: none, amount, percent)", kind)
	}
}

// ParsePercent parses an integer percentage 0-100 (percent points, not
// fractional currency — distinct from ParseCents on purpose).
func ParsePercent(field, value string) (int64, error) {
	value = strings.TrimSpace(value)
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, errs.New(errs.CodeValidation, "%s must be an integer 0-100 (got %q)", field, value)
		}
	}
	if value == "" {
		return 0, errs.New(errs.CodeValidation, "%s must not be empty", field)
	}
	var n int64
	for _, r := range value {
		n = n*10 + int64(r-'0')
		if n > 100 {
			return 0, errs.New(errs.CodeValidation, "%s must be 0-100 (got %q)", field, value)
		}
	}
	return n, nil
}

// FormatCents renders cents as a plain decimal string ("12.34").
func FormatCents(cents int64) string {
	sign := ""
	if cents < 0 {
		sign, cents = "-", -cents
	}
	return fmt.Sprintf("%s%d.%02d", sign, cents/100, cents%100)
}

// FormatQuantityMilli renders thousandths as a plain decimal string.
func FormatQuantityMilli(milli int64) string {
	return fmt.Sprintf("%d.%03d", milli/1000, milli%1000)
}
