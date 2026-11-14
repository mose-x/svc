package sdk

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPerlWindowsJSONParsing(t *testing.T) {
	jsonData := `[{"version":"5.42.2.1","date":"2026-04-07","edition":{"portable":{"url":"https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54221_64bit/strawberry-perl-5.42.2.1-64bit-portable.zip","sha256":"abc","size":304301401}}},{"version":"5.40.4.1","date":"2026-04-07","edition":{"portable":{"url":"https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54041_64bit/strawberry-perl-5.40.4.1-64bit-portable.zip","sha256":"def","size":300000000}}}]`

	var releases []strawberryRelease
	if err := json.Unmarshal([]byte(jsonData), &releases); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if len(releases) != 2 {
		t.Fatalf("expected 2 releases, got %d", len(releases))
	}

	if releases[0].Version != "5.42.2.1" {
		t.Errorf("version: expected '5.42.2.1', got '%s'", releases[0].Version)
	}

	if releases[0].Edition.Portable.URL == "" {
		t.Error("portable URL should not be empty")
	}

	if !strings.Contains(releases[0].Edition.Portable.URL, "github.com") {
		t.Errorf("URL should point to GitHub, got: %s", releases[0].Edition.Portable.URL)
	}

	if strings.Contains(releases[0].Edition.Portable.URL, "strawberryperl.com/download") {
		t.Errorf("URL should NOT use old strawberryperl.com/download/ path (404), got: %s", releases[0].Edition.Portable.URL)
	}
}

func TestPerlWindowsFileNameExtraction(t *testing.T) {
	releases := []strawberryRelease{
		{Version: "5.42.2.1", Edition: struct {
			Portable struct {
				URL    string `json:"url"`
				Sha256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"portable"`
		}{
			Portable: struct {
				URL    string `json:"url"`
				Sha256 string `json:"sha256"`
				Size   int64  `json:"size"`
			}{
				URL: "https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54221_64bit/strawberry-perl-5.42.2.1-64bit-portable.zip",
			},
		},
		},
	}

	for _, r := range releases {
		urlParts := strings.Split(r.Edition.Portable.URL, "/")
		fileName := urlParts[len(urlParts)-1]
		if fileName != "strawberry-perl-5.42.2.1-64bit-portable.zip" {
			t.Errorf("fileName: expected 'strawberry-perl-5.42.2.1-64bit-portable.zip', got '%s'", fileName)
		}
	}
}

// --- Portable-edition filtering (releases.json is not uniformly a zip) ---

func TestIsPerlPortableArchive(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54231_64bit/strawberry-perl-5.42.3.1-64bit-portable.zip", true},
		{"https://example.com/strawberry-perl-5.40.4.1-64bit-PDL.zip", true},
		{"https://example.com/strawberry-perl-5.42.3.1-64bit-portable.ZIP", true},
		// The real feed ships an MSI installer under edition.portable for
		// 5.40.5.1 — the extractor cannot open it.
		{"https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54051_64bit/strawberry-perl-5.40.5.1-64bit.msi", false},
		{"https://example.com/strawberry-perl-5.40.5.1-64bit.exe", false},
		{"https://example.com/perl.tar.gz", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			if got := isPerlPortableArchive(tt.url); got != tt.want {
				t.Errorf("isPerlPortableArchive(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestPerlWindowsVersionsSkipsNonPortableEditions(t *testing.T) {
	// Mirrors the shapes strawberryperl.com/releases.json actually serves:
	// a normal portable zip, an MSI mislabelled as portable, an edition with
	// no portable URL, and a PDL portable zip.
	jsonData := `[
		{"version":"5.40.5.1","date":"2026-09-03","edition":{"portable":{"url":"https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54051_64bit/strawberry-perl-5.40.5.1-64bit.msi","sha256":"aaa","size":1}}},
		{"version":"5.42.3.1","date":"2026-08-17","edition":{"portable":{"url":"https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54231_64bit/strawberry-perl-5.42.3.1-64bit-portable.zip","sha256":"bbb","size":2}}},
		{"version":"5.42.2.1","date":"2026-04-07","edition":{"portable":{"url":"","sha256":"","size":0}}},
		{"version":"5.40.4.1","date":"2026-04-07","edition":{"portable":{"url":"https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54041_64bit/strawberry-perl-5.40.4.1-64bit-PDL.zip","sha256":"ccc","size":3}}},
		{"version":"5.42.0.1","date":"2025-08-01","edition":{"portable":{"url":"https://github.com/StrawberryPerl/Perl-Dist-Strawberry/releases/download/SP_54201_64bit/strawberry-perl-5.42.0.1-64bit-portable.zip","sha256":"ddd","size":4}}}
	]`

	var releases []strawberryRelease
	if err := json.Unmarshal([]byte(jsonData), &releases); err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	versions := perlWindowsVersions(releases)

	wantOrder := []string{"5.42.3.1", "5.42.0.1", "5.40.4.1"}
	if len(versions) != len(wantOrder) {
		t.Fatalf("expected %d installable versions %v, got %d: %+v", len(wantOrder), wantOrder, len(versions), versions)
	}
	for i, want := range wantOrder {
		if versions[i].Version != want {
			t.Errorf("versions[%d] = %q, want %q (newest first)", i, versions[i].Version, want)
		}
	}
	for _, v := range versions {
		if v.Version == "5.40.5.1" {
			t.Error("the MSI-only 5.40.5.1 release must not be offered: it cannot be extracted")
		}
		if !strings.HasSuffix(v.DownloadURL, ".zip") {
			t.Errorf("version %s: DownloadURL is not a zip archive: %s", v.Version, v.DownloadURL)
		}
		wantFile := v.DownloadURL[strings.LastIndex(v.DownloadURL, "/")+1:]
		if v.FileName != wantFile {
			t.Errorf("version %s: FileName = %q, want %q", v.Version, v.FileName, wantFile)
		}
	}
	if versions[0].Major != 5 {
		t.Errorf("Major = %d, want 5", versions[0].Major)
	}
	if versions[0].ReleaseDate != "2026-08-17" {
		t.Errorf("ReleaseDate = %q, want 2026-08-17", versions[0].ReleaseDate)
	}
}

func TestPerlWindowsVersionsEmptyFeed(t *testing.T) {
	if got := perlWindowsVersions(nil); got != nil {
		t.Errorf("perlWindowsVersions(nil) = %+v, want nil", got)
	}
}
