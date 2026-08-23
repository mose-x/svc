package sdk

import "testing"

func TestIsWindowsStorePython(t *testing.T) {
	tests := []struct {
		name    string
		binPath string
		want    bool
	}{
		{
			name:    "user profile WindowsApps stub",
			binPath: `C:\Users\mose\AppData\Local\Microsoft\WindowsApps\python.exe`,
			want:    true,
		},
		{
			name:    "lowercase forward-slash variant",
			binPath: "c:/users/mose/appdata/local/microsoft/windowsapps/python3.exe",
			want:    true,
		},
		{
			name:    "mixed case",
			binPath: `C:\Users\MOSE\AppData\Local\Microsoft\WindowsApps\python.exe`,
			want:    true,
		},
		{
			name:    "real python-build-standalone install",
			binPath: `C:\Users\mose\.svc\python\3.13.2\python.exe`,
			want:    false,
		},
		{
			name:    "system32 is not the store stub",
			binPath: `C:\Windows\System32\python.exe`,
			want:    false,
		},
		{
			name:    "unix system python",
			binPath: "/usr/bin/python3",
			want:    false,
		},
		{
			name:    "empty",
			binPath: "",
			want:    false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsWindowsStorePython(tt.binPath); got != tt.want {
				t.Errorf("IsWindowsStorePython(%q) = %v; want %v", tt.binPath, got, tt.want)
			}
		})
	}
}
