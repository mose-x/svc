package cli

import (
	"math"
	"strconv"
	"strings"

	"svc/internal/sdk"
)

// byteUnits is the unit table from frontend/src/utils/format.ts. It stops at
// GB on purpose: an SDK archive is never a terabyte, and a longer table would
// make the CLI and the GUI disagree about the same file size.
var byteUnits = [...]string{"B", "KB", "MB", "GB"}

// FormatBytes renders a byte count exactly as the GUI renders it. It is a port
// of formatBytes in frontend/src/utils/format.ts, quirks included:
//
//   - anything <= 0 is "0 B", so an unknown size never prints a negative number;
//   - the unit index is floor(log(bytes)/log(1024)) clamped to the table, which
//     means 1048575 prints as "1024.0 KB" rather than rounding up to 1 MB --
//     reproducing that is the point, since the same download must be described
//     identically in the window and in the terminal;
//   - the fraction is rounded half away from zero (see formatFixed).
//
// The float arithmetic is deliberately identical to the TypeScript: Go's
// math.Log and V8's Math.Log agree bit for bit on these inputs, so the unit
// boundaries land on the same side.
func FormatBytes(bytes int64) string {
	if bytes <= 0 {
		return "0 B"
	}
	value := float64(bytes)
	idx := int(math.Floor(math.Log(value) / math.Log(1024)))
	if idx < 0 {
		idx = 0
	}
	if idx > len(byteUnits)-1 {
		idx = len(byteUnits) - 1
	}
	digits := 1
	if idx == 0 {
		digits = 0
	}
	scaled := value / math.Pow(1024, float64(idx))
	return formatFixed(scaled, digits) + " " + byteUnits[idx]
}

// formatFixed formats value with exactly digits decimals, rounding halves away
// from zero -- JavaScript's Number.prototype.toFixed behaviour.
//
// strconv.FormatFloat cannot be used here: Go rounds halves to even, so the
// 1280 bytes that the GUI shows as "1.3 KB" would print as "1.2 KB" in the
// terminal. That single digit is the kind of difference this port exists to
// eliminate.
//
// Rounding by scaling (floor(|x|*10^d + 0.5)) is exact for the inputs
// FormatBytes produces. x is bytes/1024^idx, and dividing by a power of two is
// exact, so a tie can only occur when x*10^d is itself a dyadic half-integer --
// which is representable exactly, so the multiply introduces no error to be
// amplified. Values far outside that range fall back to strconv rather than
// overflowing the int64.
func formatFixed(value float64, digits int) string {
	if digits < 0 {
		digits = 0
	}
	if math.IsNaN(value) {
		return "NaN"
	}
	sign := ""
	if value < 0 || (value == 0 && math.Signbit(value)) {
		sign = "-"
		value = -value
	}
	if math.IsInf(value, 1) {
		return sign + "Infinity"
	}

	shift := math.Pow(10, float64(digits))
	scaled := value * shift
	if scaled > math.MaxInt64-1 {
		// Unreachable from FormatBytes; kept so a caller passing an absurd
		// value degrades instead of invoking undefined int64 conversion.
		return sign + strconv.FormatFloat(value, 'f', digits, 64)
	}
	rounded := int64(math.Floor(scaled + 0.5))

	if digits == 0 {
		return sign + strconv.FormatInt(rounded, 10)
	}
	divisor := int64(shift)
	whole := rounded / divisor
	fraction := strconv.FormatInt(rounded%divisor, 10)
	return sign + strconv.FormatInt(whole, 10) + "." +
		strings.Repeat("0", digits-len(fraction)) + fraction
}

// ExternalManagerLabel renders a detected external version manager the way the
// GUI renders it. The table lives in the sdk package, next to the detectors that
// produce the machine names it labels, because a service refusal carries the
// label too: the CLI and the GUI have to be able to say the same sentence about
// the same nvm installation, and a copy on each side is how they would drift.
func ExternalManagerLabel(manager string) string {
	return sdk.ExternalManagerLabel(manager)
}
