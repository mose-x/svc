package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"svc/internal/apperr"
	"svc/internal/cli/i18n"
)

// The embedded catalogs are what makes "the CLI words things exactly like the
// UI" true at runtime. internal/cli/i18n guards the catalog CONTENT against the
// frontend tree; these tests guard the EMBED -- that the pattern resolves, that
// the bytes in the binary are the bytes on disk, and that they build a working
// Catalog. Without them a misconfigured go:embed pattern would ship a CLI that
// prints raw "[svc:...]" markers, and nothing would fail until a user hit it.

func TestEmbeddedCatalogsMatchTheFrontendFiles(t *testing.T) {
	catalogs := cliCatalogs()
	if len(catalogs) != 2 {
		t.Fatalf("cliCatalogs() returned %d entries, want zh and en", len(catalogs))
	}
	for lang, embedded := range catalogs {
		if len(embedded) == 0 {
			t.Errorf("the embedded %s catalog is empty; the go:embed pattern did not resolve", lang)
			continue
		}
		onDisk, err := os.ReadFile(filepath.Join("frontend", "src", "i18n", lang+".json"))
		if err != nil {
			t.Fatalf("read the %s catalog from disk: %v", lang, err)
		}
		if !bytes.Equal(embedded, onDisk) {
			t.Errorf("the embedded %s catalog differs from frontend/src/i18n/%s.json", lang, lang)
		}
	}
}

func TestEmbeddedCatalogsBuildAWorkingCatalog(t *testing.T) {
	for _, lang := range []string{i18n.DefaultLang, i18n.FallbackLang} {
		c, err := i18n.New(cliCatalogs(), lang)
		if err != nil {
			t.Fatalf("i18n.New(lang=%q): %v", lang, err)
		}
		if c.Lang() != lang {
			t.Errorf("Lang() = %q, want %q", c.Lang(), lang)
		}
		raw := apperr.New(apperr.NoBackup, nil).Error()
		if got := c.RenderErrorText(raw); got == raw {
			t.Errorf("%s: the embedded catalog cannot translate %q", lang, raw)
		}
	}
}
