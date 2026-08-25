// Package proxy centralizes the application's outbound HTTP proxy policy:
// reading the user's proxy settings, applying the optional GitHub mirror,
// building proxied transports, and the SSRF-guarded connectivity check.
package proxy

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"svc/internal/apperr"
	"svc/internal/config"
	"svc/internal/downloader"
	"svc/internal/logger"
)

// Service reads proxy/mirror policy from the user settings.
type Service struct {
	settings *config.SettingsManager
}

// New builds a Service on top of the settings manager.
func New(sm *config.SettingsManager) *Service {
	return &Service{settings: sm}
}

// Config returns the current proxy configuration as consumed by the
// downloader package.
func (s *Service) Config() downloader.ProxyConfig {
	st := s.settings.Get()
	return downloader.ProxyConfig{
		Enabled:  st.Proxy.Enabled,
		Mode:     st.Proxy.Mode,
		URL:      st.Proxy.URL,
		Protocol: st.Proxy.Protocol,
	}
}

// CheckProxy verifies that the configured proxy can reach targetURL.
// Exposed to the frontend via the App facade.
func (s *Service) CheckProxy(targetURL string) error {
	// H2: Validate URL to prevent SSRF — only http/https, reject loopback/private.
	if err := validateCheckURL(targetURL); err != nil {
		return err
	}
	client := downloader.BuildClient(s.Config())
	client.Timeout = 10 * time.Second
	// Re-validate every redirect hop: the initial URL may pass validation,
	// but a 3xx chain could otherwise land on an internal address unchecked.
	client.CheckRedirect = checkRedirectPolicy

	resp, err := client.Get(targetURL)
	if err != nil {
		if resp != nil {
			resp.Body.Close()
		}
		return fmt.Errorf("connection failed: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode >= 400 {
		return apperr.New(apperr.HttpStatus, map[string]string{"status": strconv.Itoa(resp.StatusCode)})
	}
	return nil
}

// checkRedirectPolicy is the http.Client.CheckRedirect hook used by
// CheckProxy. Every hop is re-validated so a redirect cannot escape the SSRF
// constraints enforced on the initial URL. The 10-hop cap replicates the
// net/http default policy, which is not applied when a custom CheckRedirect
// is set.
func checkRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	return validateCheckURL(req.URL.String())
}

// validateCheckURL ensures the target URL is safe for proxy checking:
// must be http/https, must not target loopback/private/link-local addresses.
func validateCheckURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return apperr.New(apperr.SchemeNotAllowed, map[string]string{"scheme": u.Scheme})
	}
	host := u.Hostname()
	// Fast path: common loopback/private hosts by string match.
	if host == "localhost" || host == "127.0.0.1" || host == "::1" ||
		strings.HasPrefix(host, "127.") || strings.HasPrefix(host, "10.") ||
		strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "172.16.") ||
		strings.HasPrefix(host, "172.17.") || strings.HasPrefix(host, "172.18.") ||
		strings.HasPrefix(host, "172.19.") || strings.HasPrefix(host, "172.20.") ||
		strings.HasPrefix(host, "172.21.") || strings.HasPrefix(host, "172.22.") ||
		strings.HasPrefix(host, "172.23.") || strings.HasPrefix(host, "172.24.") ||
		strings.HasPrefix(host, "172.25.") || strings.HasPrefix(host, "172.26.") ||
		strings.HasPrefix(host, "172.27.") || strings.HasPrefix(host, "172.28.") ||
		strings.HasPrefix(host, "172.29.") || strings.HasPrefix(host, "172.30.") ||
		strings.HasPrefix(host, "172.31.") {
		return apperr.New(apperr.PrivateIp, map[string]string{"host": host})
	}
	// Thorough check for IP literals: covers ranges the string blacklist
	// misses (0.0.0.0, 169.254.0.0/16, IPv6 fc00::/7 and fe80::/10,
	// IPv4-mapped IPv6 like ::ffff:127.0.0.1). Non-IP hostnames parse to nil
	// and are allowed here.
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return apperr.New(apperr.PrivateIp, map[string]string{"host": host})
		}
	}
	return nil
}

// ApplyGithubMirror prefixes url with the user-configured GitHub mirror when
// url targets github.com and is not already under the mirror.
func (s *Service) ApplyGithubMirror(url string) string {
	mirror := s.settings.Get().GithubMirror
	if mirror == "" {
		return url
	}
	mirror = strings.TrimRight(mirror, "/")
	// Guard against a mirror that already points at github.com (e.g. a user
	// misconfigured it as https://github.com/proxy): without the prefix check
	// we'd prepend mirror to an already-mirrored URL, producing garbage.
	if isGithubHost(url) && !strings.HasPrefix(url, mirror) {
		return mirror + "/" + url
	}
	return url
}

// isGithubHost reports whether url targets github.com or a github.com subdomain.
// Uses net/url parsing so a non-github URL that merely contains "github.com"
// as a substring (e.g. https://evil.example.com/path/github.com/foo or
// https://github.com.evil.example.com/) is NOT treated as a github URL.
func isGithubHost(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := u.Host
	// Strip any port suffix for the host comparison.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(host)
	return host == "github.com" || strings.HasSuffix(host, ".github.com")
}

// BuildTransport builds the transport for non-download HTTP calls (the
// update checker). It delegates to downloader.ApplyProxyConfig, the same
// single implementation used by downloader.BuildClient, so the two cannot
// drift. (Previously this had its own logic: bare "host:port" custom proxies
// failed url.Parse and were silently dropped, and system mode used
// http.ProxyFromEnvironment instead of the platform applySystemProxy.)
func (s *Service) BuildTransport() *http.Transport {
	transport := &http.Transport{}
	if err := downloader.ApplyProxyConfig(transport, s.Config()); err != nil {
		logger.Warn("Invalid proxy configuration %q: %v — proxy will not be applied", s.settings.Get().Proxy.URL, err)
	}
	return transport
}
