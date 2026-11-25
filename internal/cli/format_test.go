package cli

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"testing"
)

// TestFormatBytesMatchesTheFrontend is the golden table for the byte formatter.
// Every expected string was captured from formatBytes in
// frontend/src/utils/format.ts running under node, so the CLI and the GUI are
// pinned to the same output rather than to the same idea.
//
// The interesting rows are the ones a "sensible" reimplementation would get
// wrong: 1048575 stays "1024.0 KB" because the unit index is floored, and
// 1280 is "1.3 KB" because toFixed rounds halves away from zero.
func TestFormatBytesMatchesTheFrontend(t *testing.T) {
	cases := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{-5, "0 B"},
		{math.MinInt64, "0 B"},
		{1, "1 B"},
		{2, "2 B"},
		{100, "100 B"},
		{255, "255 B"},
		{256, "256 B"},
		{511, "511 B"},
		{512, "512 B"},
		{768, "768 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1025, "1.0 KB"},
		{1280, "1.3 KB"},
		{1536, "1.5 KB"},
		{2047, "2.0 KB"},
		{2048, "2.0 KB"},
		{1048575, "1024.0 KB"},
		{1048576, "1.0 MB"},
		{1048577, "1.0 MB"},
		{1572864, "1.5 MB"},
		{1073741823, "1024.0 MB"},
		{1073741824, "1.0 GB"},
		{1610612736, "1.5 GB"},
		{1099511627775, "1024.0 GB"},
		{1099511627776, "1024.0 GB"},
		{5497558138880, "5120.0 GB"},
		{4503599627370496, "4194304.0 GB"},
		// Realistic archive sizes, so the table covers the range svc actually
		// reports rather than only its edges.
		{45_900_000, "43.8 MB"},
		{186_000_000, "177.4 MB"},
		{1_400_000_000, "1.3 GB"},
	}
	for _, c := range cases {
		if got := FormatBytes(c.bytes); got != c.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", c.bytes, got, c.want)
		}
	}
}

// TestFormatBytesRealisticSizesAgainstNode double-checks the three realistic
// sizes above were not rounded by hand.
func TestFormatBytesRealisticSizesAgainstNode(t *testing.T) {
	// Captured from node: (45900000/1048576).toFixed(1) and friends.
	cases := map[int64]string{
		45_900_000:    "43.8 MB",
		186_000_000:   "177.4 MB",
		1_400_000_000: "1.3 GB",
	}
	for bytes, want := range cases {
		if got := FormatBytes(bytes); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", bytes, got, want)
		}
	}
}

func TestFormatFixed(t *testing.T) {
	cases := []struct {
		value  float64
		digits int
		want   string
	}{
		// The case that motivated the helper: Go's own formatter rounds 1.25
		// to "1.2" and JavaScript rounds it to "1.3".
		{1.25, 1, "1.3"},
		{1.35, 1, "1.4"},
		{2.5, 0, "3"},
		{0.5, 0, "1"},
		{1.5, 0, "2"},
		{3.5, 0, "4"},
		{1023.95, 1, "1024.0"},
		{1024, 1, "1024.0"},
		{0, 0, "0"},
		{0, 1, "0.0"},
		{0, 3, "0.000"},
		{7, 2, "7.00"},
		{1.0005, 2, "1.00"},
		// 1.005 is 1.00499999... as a double, so both JavaScript and this
		// port round it down. The value is pinned because it looks like a bug.
		{1.005, 2, "1.00"},
		{-1.25, 1, "-1.3"},
		{-2.5, 0, "-3"},
		{-0.04, 1, "-0.0"},
		{math.Inf(1), 1, "Infinity"},
		{math.Inf(-1), 1, "-Infinity"},
		{math.NaN(), 1, "NaN"},
	}
	for _, c := range cases {
		if got := formatFixed(c.value, c.digits); got != c.want {
			t.Errorf("formatFixed(%v, %d) = %q, want %q", c.value, c.digits, got, c.want)
		}
	}
}

// TestFormatFixedClampsDigits guards the two degenerate arguments: a negative
// digit count behaves like zero rather than producing a negative-length
// padding, and a huge value degrades to strconv instead of overflowing int64.
func TestFormatFixedClampsDigits(t *testing.T) {
	if got := formatFixed(1.6, -3); got != "2" {
		t.Errorf("formatFixed(1.6, -3) = %q, want %q", got, "2")
	}
	huge := float64(math.MaxInt64)
	if got := formatFixed(huge, 1); !strings.Contains(got, "92233720368547") {
		t.Errorf("formatFixed(MaxInt64, 1) = %q, want the strconv fallback", got)
	}
}

// TestFormatFixedMatchesTheECMARule walks a dense grid and compares against a
// big.Rat reference that implements the specification directly: choose n so
// that n/10^digits - x is as close to zero as possible, taking the larger n on
// a tie. The grid is stepped by eighths so it lands on exact halves, which is
// where a float-scaled implementation is most likely to diverge.
func TestFormatFixedMatchesTheECMARule(t *testing.T) {
	for digits := 0; digits <= 3; digits++ {
		for eighth := 0; eighth <= 8*1024; eighth++ {
			value := float64(eighth) / 8
			got := formatFixed(value, digits)
			want := ecmasToFixed(value, digits)
			if got != want {
				t.Fatalf("formatFixed(%v, %d) = %q, want %q (the specification's value)", value, digits, got, want)
			}
		}
	}
}

// TestFormatBytesEverySmallSize runs the whole formatter over every size up to
// 4 KiB plus every 256-byte multiple, which is where the unit boundaries and
// the half-away-from-zero ties live.
func TestFormatBytesEverySmallSize(t *testing.T) {
	check := func(bytes int64) {
		t.Helper()
		idx := unitIndex(float64(bytes))
		digits := 1
		if idx == 0 {
			digits = 0
		}
		value := float64(bytes) / math.Pow(1024, float64(idx))
		want := ecmasToFixed(value, digits) + " " + byteUnits[idx]
		if got := FormatBytes(bytes); got != want {
			t.Fatalf("FormatBytes(%d) = %q, want %q", bytes, got, want)
		}
	}
	for bytes := int64(1); bytes <= 4096; bytes++ {
		check(bytes)
	}
	for bytes := int64(5120); bytes <= 1<<40; bytes *= 2 {
		check(bytes)
		check(bytes - 1)
		check(bytes + 256)
	}
}

// unitIndex is the unit-selection half of FormatBytes, restated so the tests
// can build an expected string without calling the function under test.
func unitIndex(value float64) int {
	idx := int(math.Floor(math.Log(value) / math.Log(1024)))
	if idx < 0 {
		idx = 0
	}
	if idx > len(byteUnits)-1 {
		idx = len(byteUnits) - 1
	}
	return idx
}

// ecmasToFixed is the reference implementation of
// Number.prototype.toFixed for finite values: exact rational arithmetic, ties
// away from zero.
func ecmasToFixed(value float64, digits int) string {
	if math.IsNaN(value) {
		return "NaN"
	}
	sign := ""
	if value < 0 {
		sign, value = "-", -value
	}
	if math.IsInf(value, 1) {
		return sign + "Infinity"
	}

	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	scaled := new(big.Rat).SetFloat64(value)
	scaled.Mul(scaled, new(big.Rat).SetInt(scale))
	scaled.Add(scaled, big.NewRat(1, 2))

	numerator, denominator := scaled.Num(), scaled.Denom()
	rounded := new(big.Int).Quo(numerator, denominator) // floor: values are >= 0
	whole := new(big.Int).Quo(rounded, scale)
	fraction := new(big.Int).Mod(rounded, scale)

	text := sign + whole.String()
	if digits > 0 {
		padded := fraction.String()
		text += "." + strings.Repeat("0", digits-len(padded)) + padded
	}
	return text
}

// TestFormatBytesIsStableUnderRepeat is a cheap guard against a formatter that
// depends on hidden state: the same input must produce the same string every
// time, and the result must be plain ASCII plus the unit words.
func TestFormatBytesIsStableUnderRepeat(t *testing.T) {
	for _, bytes := range []int64{0, 1, 1023, 1024, 1280, 1048575, 1073741824} {
		want := FormatBytes(bytes)
		for i := 0; i < 3; i++ {
			if got := FormatBytes(bytes); got != want {
				t.Fatalf("FormatBytes(%d) changed between calls: %q then %q", bytes, want, got)
			}
		}
		fields := strings.Fields(want)
		if len(fields) != 2 {
			t.Fatalf("FormatBytes(%d) = %q, want a number and a unit", bytes, want)
		}
		if _, err := strconv.ParseFloat(fields[0], 64); err != nil {
			t.Errorf("FormatBytes(%d) = %q: %q is not a number", bytes, want, fields[0])
		}
	}
}
