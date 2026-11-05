package pathmgr

import "strings"

// Windows registry value types (REG_SZ / REG_EXPAND_SZ). Declared locally so the
// PATH type-preservation decision below stays pure and unit-testable on the
// non-Windows CI hosts, where golang.org/x/sys/windows/registry is unavailable.
const (
	regTypeSZ       uint32 = 1
	regTypeExpandSZ uint32 = 2
)

// choosePathValueType decides which registry value type to use when writing the
// user PATH. It keeps REG_EXPAND_SZ when the existing value was already
// expandable or when the new value contains a %VAR% reference, and falls back to
// REG_SZ otherwise. Writing REG_SZ over a REG_EXPAND_SZ PATH silently stops
// %VAR% expansion for every entry — the O8 regression this guards against.
func choosePathValueType(origType uint32, existed bool, val string) uint32 {
	if (existed && origType == regTypeExpandSZ) || strings.Contains(val, "%") {
		return regTypeExpandSZ
	}
	return regTypeSZ
}
