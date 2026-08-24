package sdk

import "testing"

// TestClassifyPathCopy pins the single source of truth used by every view
// (sidebar/detail status, PATH modal) and every import guard.
func TestClassifyPathCopy(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		sdkType SdkType
		p       string
		want    PathCopyClassification
	}{
		// Hidden: Windows Store python stubs (probing them opens the Store).
		{
			"windows store python binary hidden",
			"windows", Python, `C:\Users\mose\AppData\Local\Microsoft\WindowsApps\python.exe`,
			PathCopyClassification{Hidden: true},
		},
		{
			"windows store python dir hidden",
			"windows", Python, `C:\Users\mose\AppData\Local\Microsoft\WindowsApps`,
			PathCopyClassification{Hidden: true},
		},
		{
			"windows store dir not hidden for node",
			"windows", NodeJS, `C:\Users\mose\AppData\Local\Microsoft\WindowsApps\node.exe`,
			PathCopyClassification{SystemProtected: true},
		},
		// SystemProtected: generic protected dirs apply to every SDK type.
		{
			"darwin /usr/bin python3",
			"darwin", Python, "/usr/bin/python3",
			PathCopyClassification{SystemProtected: true},
		},
		{
			"linux /usr/bin python3",
			"linux", Python, "/usr/bin/python3",
			PathCopyClassification{SystemProtected: true},
		},
		{
			"darwin /usr/bin ruby",
			"darwin", Ruby, "/usr/bin/ruby",
			PathCopyClassification{SystemProtected: true},
		},
		{
			"darwin CLT perl",
			"darwin", Perl, "/Library/Developer/CommandLineTools/usr/bin/perl",
			PathCopyClassification{SystemProtected: true},
		},
		{
			"darwin system cryptex javac",
			"darwin", JDK, "/System/Cryptexes/App/usr/bin/javac",
			PathCopyClassification{SystemProtected: true},
		},
		{
			"windows system32",
			"windows", JDK, `C:\Windows\System32\java.exe`,
			PathCopyClassification{SystemProtected: true},
		},
		// Python-only system paths: framework/distro locations are protected
		// for python but not for other SDK types.
		{
			"darwin framework python protected",
			"darwin", Python, "/Library/Frameworks/Python.framework/Versions/3.13/bin/python3",
			PathCopyClassification{SystemProtected: true},
		},
		{
			"darwin framework node not protected",
			"darwin", NodeJS, "/Library/Frameworks/Python.framework/Versions/3.13/bin/node",
			PathCopyClassification{},
		},
		// ExternalManager: nvm / nvm-rust owned Node.js copies.
		{
			"nvm-rust node binary",
			"darwin", NodeJS, "/Users/mose/.nvm.rust/active/bin/node",
			PathCopyClassification{ExternalManager: "nvm-rust"},
		},
		{
			"nvm-rust shim dir",
			"darwin", NodeJS, "/Users/mose/.nvm.rust/shims",
			PathCopyClassification{ExternalManager: "nvm-rust"},
		},
		{
			"classic nvm node",
			"darwin", NodeJS, "/Users/mose/.nvm/versions/node/v20.11.1/bin/node",
			PathCopyClassification{ExternalManager: "nvm"},
		},
		{
			"nvm-windows node",
			"windows", NodeJS, `C:\Users\mose\AppData\Roaming\nvm\v20.11.1\node.exe`,
			PathCopyClassification{ExternalManager: "nvm"},
		},
		{
			"nvm dir ignored for non-node sdk",
			"darwin", Python, "/Users/mose/.nvm.rust/shims/python3",
			PathCopyClassification{},
		},
		// Standalone copies: nothing to flag.
		{
			"homebrew node",
			"darwin", NodeJS, "/opt/homebrew/bin/node",
			PathCopyClassification{},
		},
		{
			"homebrew python",
			"darwin", Python, "/opt/homebrew/bin/python3",
			PathCopyClassification{},
		},
		{
			"program files node",
			"windows", NodeJS, `C:\Program Files\nodejs\node.exe`,
			PathCopyClassification{},
		},
		// Edge cases.
		{"empty path", "darwin", NodeJS, "", PathCopyClassification{}},
		{"unknown goos", "plan9", Python, "/usr/bin/python3", PathCopyClassification{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyPathCopy(tt.goos, tt.sdkType, tt.p); got != tt.want {
				t.Errorf("classifyPathCopy(%q, %q, %q) = %+v; want %+v",
					tt.goos, tt.sdkType, tt.p, got, tt.want)
			}
		})
	}
}

// TestClassifyPathCopyWrapper ensures the exported wrapper delegates to the
// pure core with the host GOOS.
func TestClassifyPathCopyWrapper(t *testing.T) {
	if got := ClassifyPathCopy(NodeJS, ""); got != (PathCopyClassification{}) {
		t.Errorf("ClassifyPathCopy empty path = %+v; want zero classification", got)
	}
}

func TestIsProtectedSystemDir(t *testing.T) {
	tests := []struct {
		name string
		goos string
		dir  string
		want bool
	}{
		// macOS
		{"darwin /usr/bin", "darwin", "/usr/bin", true},
		{"darwin /usr/sbin", "darwin", "/usr/sbin", true},
		{"darwin /bin", "darwin", "/bin", true},
		{"darwin /sbin", "darwin", "/sbin", true},
		{"darwin system cryptex", "darwin", "/System/Cryptexes/App/usr/bin", true},
		{"darwin CLT usr bin", "darwin", "/Library/Developer/CommandLineTools/usr/bin", true},
		{"darwin homebrew stays importable", "darwin", "/usr/local/bin", false},
		{"darwin opt homebrew", "darwin", "/opt/homebrew/bin", false},
		{"darwin prefix boundary", "darwin", "/usr/binx", false},
		// Linux
		{"linux /usr/bin", "linux", "/usr/bin", true},
		{"linux /bin", "linux", "/bin", true},
		{"linux /usr/lib", "linux", "/usr/lib", true},
		{"linux /usr/local/bin stays importable", "linux", "/usr/local/bin", false},
		{"linux snap", "linux", "/snap/bin", false},
		// Windows
		{"windows system32", "windows", `C:\Windows\System32`, true},
		{"windows store stubs", "windows", `C:\Users\mose\AppData\Local\Microsoft\WindowsApps`, true},
		{"windows program files node", "windows", `C:\Program Files\nodejs`, false},
		// Edge cases
		{"empty dir", "darwin", "", false},
		{"unknown goos", "plan9", "/usr/bin", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsProtectedSystemDir(tt.goos, tt.dir); got != tt.want {
				t.Errorf("IsProtectedSystemDir(%q, %q) = %v; want %v", tt.goos, tt.dir, got, tt.want)
			}
		})
	}
}

func TestIsSystemSdkPath(t *testing.T) {
	tests := []struct {
		name    string
		goos    string
		sdkType SdkType
		p       string
		want    bool
	}{
		{"protected dir any sdk", "darwin", Ruby, "/usr/bin/ruby", true},
		{"python framework path", "darwin", Python, "/Library/Frameworks/Python.framework/Versions/3.13/bin/python3", true},
		{"framework path non-python", "darwin", NodeJS, "/Library/Frameworks/Python.framework/Versions/3.13/bin/node", false},
		{"standalone", "darwin", Python, "/opt/homebrew/bin/python3", false},
		{"empty", "darwin", Python, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsSystemSdkPath(tt.goos, tt.sdkType, tt.p); got != tt.want {
				t.Errorf("IsSystemSdkPath(%q, %q, %q) = %v; want %v", tt.goos, tt.sdkType, tt.p, got, tt.want)
			}
		})
	}
}

func TestIsSystemPythonPath(t *testing.T) {
	tests := []struct {
		name string
		goos string
		p    string
		want bool
	}{
		{"darwin usr bin", "darwin", "/usr/bin/python3", true},
		{"darwin xcode python", "darwin", "/System/Volumes/Data/usr/bin/python3", true},
		{"linux usr bin", "linux", "/usr/bin/python3", true},
		{"linux usr lib", "linux", "/usr/lib/python3/dist-packages", true},
		{"windows system32", "windows", `C:\Windows\System32\python.exe`, true},
		{"windows store stub", "windows", `C:\Users\mose\AppData\Local\Microsoft\WindowsApps\python.exe`, true},
		{"darwin homebrew", "darwin", "/opt/homebrew/bin/python3", false},
		{"linux pyenv", "linux", "/home/mose/.pyenv/shims/python3", false},
		{"windows python org", "windows", `C:\Python313\python.exe`, false},
		{"empty", "darwin", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isSystemPythonPath(tt.goos, tt.p); got != tt.want {
				t.Errorf("isSystemPythonPath(%q, %q) = %v; want %v", tt.goos, tt.p, got, tt.want)
			}
		})
	}
}
