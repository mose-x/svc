package pkgmgr

import (
	"fmt"
	"net/url"
	"strings"

	"svc/internal/apperr"
	"svc/internal/sdk"
)

// Registry choice is persisted by npm itself in the user-level ~/.npmrc
// (`npm config set`). SVC deliberately keeps no copy: the panel always reads
// the live value, and SettingsPage's whole-object SaveSettings echo can
// never clobber it. Note ~/.npmrc is shared by every Node version SVC
// manages — the switch is user-global, not per-version.

const (
	npmRegistryOfficial = "https://registry.npmjs.org"
	npmRegistryChina    = "https://registry.npmmirror.com"
)

// GetNpmRegistry returns the active Node.js installation's configured npm
// registry, whitespace-trimmed and without a trailing slash (npm's default
// prints https://registry.npmjs.org/ with one).
func (s *Service) GetNpmRegistry() (string, error) {
	if s.cfg.GetActiveVersion("nodejs") == "" {
		return "", apperr.New(apperr.NeedSdk, map[string]string{"name": "npm", "sdk": "Node.js"})
	}
	out, err := s.runScopedCommandCapture("npm", sdk.NodeJS, "config", "get", "registry")
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(out, "/"), nil
}

// SetNpmRegistry validates url and applies it via `npm config set registry`,
// which persists to the user-level ~/.npmrc.
func (s *Service) SetNpmRegistry(registryURL string) error {
	if s.cfg.GetActiveVersion("nodejs") == "" {
		return apperr.New(apperr.NeedSdk, map[string]string{"name": "npm", "sdk": "Node.js"})
	}
	u, err := validateRegistryURL(registryURL)
	if err != nil {
		return err
	}
	return s.runScopedCommand("npm", sdk.NodeJS, "config", "set", "registry", u)
}

// validateRegistryURL trims and validates a registry URL, returning the
// cleaned value. Non-http(s) schemes yield the translated scheme-not-allowed
// marker; everything else is a plain diagnostic error.
func validateRegistryURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("invalid registry URL: empty")
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid registry URL: %q", trimmed)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", apperr.New(apperr.SchemeNotAllowed, map[string]string{"scheme": u.Scheme})
	}
	return trimmed, nil
}
