package pathmgr

import "testing"

// TestChoosePathValueType covers the O8 type-preservation decision on every
// platform (the function is pure; it never touches the registry).
func TestChoosePathValueType(t *testing.T) {
	tests := []struct {
		name     string
		origType uint32
		existed  bool
		val      string
		want     uint32
	}{
		{
			name:     "preserve expand_sz when original was expand_sz",
			origType: regTypeExpandSZ,
			existed:  true,
			val:      `C:\Windows\System32;C:\bin`,
			want:     regTypeExpandSZ,
		},
		{
			name:     "upgrade to expand_sz when new value references a var",
			origType: regTypeSZ,
			existed:  true,
			val:      `%USERPROFILE%\.svc\shims;C:\bin`,
			want:     regTypeExpandSZ,
		},
		{
			name:     "expand_sz when value references a var and PATH did not exist",
			origType: 0,
			existed:  false,
			val:      `%LOCALAPPDATA%\Microsoft\WindowsApps`,
			want:     regTypeExpandSZ,
		},
		{
			name:     "keep sz when original was sz and value has no var",
			origType: regTypeSZ,
			existed:  true,
			val:      `C:\bin;D:\tools`,
			want:     regTypeSZ,
		},
		{
			name:     "sz when PATH did not exist and value has no var",
			origType: 0,
			existed:  false,
			val:      `C:\bin`,
			want:     regTypeSZ,
		},
		{
			name:     "sz when original type is expand_sz but value never existed and has no var",
			origType: regTypeExpandSZ,
			existed:  false,
			val:      `C:\bin`,
			want:     regTypeSZ,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := choosePathValueType(tt.origType, tt.existed, tt.val); got != tt.want {
				t.Errorf("choosePathValueType(origType=%d, existed=%v, val=%q) = %d, want %d",
					tt.origType, tt.existed, tt.val, got, tt.want)
			}
		})
	}
}
