package pkgmgr

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
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

// Global npm package (tool) management. All commands run with the active
// Node.js scoped to the front of PATH, so they target that installation's
// global prefix.

// npmPackageNameRe matches npm's name grammar, incl. scoped packages.
var npmPackageNameRe = regexp.MustCompile(`^(@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*$`)

// validateNpmPackageName rejects anything outside npm's name grammar so
// install/uninstall arguments can never carry shell garbage or traversal.
func validateNpmPackageName(name string) error {
	if len(name) == 0 || len(name) > 214 || !npmPackageNameRe.MatchString(name) {
		return apperr.New(apperr.InvalidPackageName, map[string]string{"name": name})
	}
	return nil
}

// GetGlobalPackages lists globally installed npm packages of the active
// Node.js. Returns an empty list (no error) for non-nodejs SDK types or
// when no node version is active.
func (s *Service) GetGlobalPackages(sdkType string) ([]sdk.GlobalPackage, error) {
	if sdkType != string(sdk.NodeJS) || s.cfg.GetActiveVersion("nodejs") == "" {
		return []sdk.GlobalPackage{}, nil
	}
	// npm exits nonzero when the tree has extraneous/invalid packages while
	// still emitting valid JSON, so parse before judging the error.
	out, execErr := s.runScopedCommandCapture("npm", sdk.NodeJS, "ls", "-g", "--depth=0", "--json")
	pkgs, parseErr := parseGlobalPackages(out)
	if parseErr == nil {
		return pkgs, nil
	}
	if execErr != nil {
		return nil, execErr
	}
	return nil, parseErr
}

// parseGlobalPackages decodes `npm ls -g --depth=0 --json` output. Reading
// only the dependencies map excludes the root prefix entry; entries without
// a version are skipped; the result is sorted by name.
func parseGlobalPackages(jsonOut string) ([]sdk.GlobalPackage, error) {
	var doc struct {
		Dependencies map[string]struct {
			Version string `json:"version"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		return nil, fmt.Errorf("parse npm ls output: %w", err)
	}
	pkgs := make([]sdk.GlobalPackage, 0, len(doc.Dependencies))
	for name, dep := range doc.Dependencies {
		if dep.Version == "" {
			continue
		}
		pkgs = append(pkgs, sdk.GlobalPackage{Name: name, Version: dep.Version})
	}
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	return pkgs, nil
}

// InstallGlobalPackage installs name globally into the active Node.js.
func (s *Service) InstallGlobalPackage(name string) error {
	if err := s.requireActiveNode(name); err != nil {
		return err
	}
	return s.runScopedCommand("npm", sdk.NodeJS, "install", "-g", name)
}

// UninstallGlobalPackage removes name from the active Node.js global prefix.
// npm itself is protected: SVC shells out to it for every operation here.
func (s *Service) UninstallGlobalPackage(name string) error {
	if err := s.requireActiveNode(name); err != nil {
		return err
	}
	if name == "npm" {
		return apperr.New(apperr.ProtectedPackage, map[string]string{"name": name})
	}
	return s.runScopedCommand("npm", sdk.NodeJS, "uninstall", "-g", name)
}

// UpdateGlobalPackage updates name to its latest published version.
func (s *Service) UpdateGlobalPackage(name string) error {
	if err := s.requireActiveNode(name); err != nil {
		return err
	}
	return s.runScopedCommand("npm", sdk.NodeJS, "install", "-g", name+"@latest")
}

// requireActiveNode validates the package name and requires an active
// Node.js installation.
func (s *Service) requireActiveNode(name string) error {
	if err := validateNpmPackageName(name); err != nil {
		return err
	}
	if s.cfg.GetActiveVersion("nodejs") == "" {
		return apperr.New(apperr.NeedSdk, map[string]string{"name": name, "sdk": "Node.js"})
	}
	return nil
}
