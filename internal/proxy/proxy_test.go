package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"svc/internal/config"
	"svc/internal/downloader"
)

// TestValidateCheckURL covers the SSRF validation of proxy-check target
// URLs: scheme restrictions, the string blacklist fast path, and the
// net.IP-based checks for IP literals that the blacklist alone misses
// (0.0.0.0, 169.254/16, IPv6 ULA/link-local, IPv4-mapped IPv6).
func TestValidateCheckURL(t *testing.T) {
	tests := []struct {
		url     string
		wantErr bool
	}{
		// allowed
		{"https://example.com", false},
		{"https://github.com", false},
		{"http://nodejs.org/dist/index.json", false},
		{"https://api.adoptium.net", false},
		{"http://github.com/robots.txt", false},
		{"http://example.com:8080/path?q=1", false},
		{"http://8.8.8.8/", false}, // public IPv4 literal
		{"http://[2001:4860:4860::8888]/", false},
		// rejected: scheme
		{"ftp://example.com", true},
		{"file:///etc/passwd", true},
		{"javascript:alert(1)", true},
		{"://missing.scheme", true},
		{"://invalid", true},
		// rejected: string blacklist fast path
		{"http://localhost/", true},
		{"http://localhost:8080", true},
		{"http://127.0.0.1", true},
		{"http://127.5.5.5/", true},
		{"http://10.0.0.1/", true},
		{"http://192.168.1.1", true},
		{"http://172.16.0.1/", true},
		{"http://172.31.255.254/", true},
		{"http://[::1]/", true},
		// rejected: IP literal checks beyond the string blacklist
		{"http://0.0.0.0/", true},            // unspecified
		{"http://169.254.169.254/", true},    // link-local (cloud metadata)
		{"http://[fe80::1]/", true},          // IPv6 link-local
		{"http://[fc00::1]/", true},          // IPv6 unique-local (private)
		{"http://[::ffff:127.0.0.1]/", true}, // IPv4-mapped loopback
		{"http://[::ffff:10.0.0.1]/", true},  // IPv4-mapped private
	}
	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			err := validateCheckURL(tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCheckURL(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

// TestCheckRedirectPolicy verifies every redirect hop is re-validated so a
// public starting URL cannot redirect into internal addresses, and that the
// 10-hop cap (normally the net/http default) is preserved.
func TestCheckRedirectPolicy(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		viaLen  int
		wantErr bool
	}{
		{"public hop allowed", "http://example.com/next", 1, false},
		{"https hop allowed", "https://example.com/next", 1, false},
		{"loopback hop blocked", "http://127.0.0.1/inner", 1, true},
		{"private hop blocked", "http://10.0.0.5/inner", 1, true},
		{"link-local hop blocked", "http://169.254.169.254/latest/meta-data", 1, true},
		{"non-http hop blocked", "ftp://example.com/file", 1, true},
		{"tenth hop blocked", "http://example.com/next", 10, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			via := make([]*http.Request, tt.viaLen)
			err := checkRedirectPolicy(req, via)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkRedirectPolicy(%q, via=%d) error = %v, wantErr %v", tt.url, tt.viaLen, err, tt.wantErr)
			}
		})
	}
}

// TestCheckProxy_RejectsInternalTargets verifies CheckProxy refuses internal
// or non-http(s) targets before any connection attempt.
func TestCheckProxy_RejectsInternalTargets(t *testing.T) {
	svc := New(config.NewSettingsManager(t.TempDir()))
	for _, target := range []string{"http://127.0.0.1/", "http://10.0.0.1/", "ftp://example.com"} {
		if err := svc.CheckProxy(target); err == nil {
			t.Errorf("CheckProxy(%q) expected error, got nil", target)
		}
	}
}

// TestCheckProxy_RedirectRevalidated exercises the full redirect flow of the
// CheckProxy client wiring: a public starting URL that redirects to a
// loopback address must be blocked by the CheckRedirect hook, while a
// redirect to another public URL must still succeed. All dials are routed to
// the local test server via a custom DialContext, so the "public" hostnames
// never touch the real network.
func TestCheckProxy_RedirectRevalidated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start-bad":
			http.Redirect(w, r, "http://127.0.0.1:9/internal", http.StatusFound)
		case "/start-good":
			http.Redirect(w, r, "http://public-ok.example/done", http.StatusFound)
		case "/done":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	client := downloader.BuildClient(downloader.ProxyConfig{Enabled: false})
	client.Timeout = 10 * time.Second
	client.CheckRedirect = checkRedirectPolicy
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}

	// Redirect into a loopback address must be blocked.
	resp, err := client.Get("http://public-start.example/start-bad")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected redirect to loopback address to be blocked")
	}
	if !strings.Contains(err.Error(), "[svc:private-ip]") {
		t.Errorf("error should carry the private-ip marker, got: %v", err)
	}

	// Redirect to another public URL must still work.
	resp, err = client.Get("http://public-start.example/start-good")
	if err != nil {
		t.Fatalf("expected public redirect to succeed, got: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Errorf("got status %d body %q, want 200 \"ok\"", resp.StatusCode, body)
	}
}

// TestBuildTransport verifies the update checker's transport shares the
// downloader's proxy logic: bare "host:port" custom proxies get a scheme
// prepended (previously url.Parse failed and the proxy was silently dropped),
// socks5 protocol wires a dialer, and disabled proxies apply nothing.
func TestBuildTransport(t *testing.T) {
	tests := []struct {
		name         string
		proxy        config.ProxySettings
		wantProxyURL string // expected transport.Proxy(req) URL; "" means no proxy func
		wantDialer   bool   // expect DialContext set (socks5)
	}{
		{
			name:  "disabled proxy applies nothing",
			proxy: config.ProxySettings{Enabled: false, Mode: "custom", URL: "127.0.0.1:7890"},
		},
		{
			name:         "custom bare host:port gets http scheme",
			proxy:        config.ProxySettings{Enabled: true, Mode: "custom", URL: "127.0.0.1:7890"},
			wantProxyURL: "http://127.0.0.1:7890",
		},
		{
			name:       "custom bare host:port with socks5 protocol sets dialer",
			proxy:      config.ProxySettings{Enabled: true, Mode: "custom", URL: "127.0.0.1:1080", Protocol: "socks5"},
			wantDialer: true,
		},
		{
			name:         "custom full URL kept as-is",
			proxy:        config.ProxySettings{Enabled: true, Mode: "custom", URL: "http://proxy.example:3128"},
			wantProxyURL: "http://proxy.example:3128",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sm := config.NewSettingsManager(t.TempDir())
			s := sm.Get()
			s.Proxy = tt.proxy
			if err := sm.Update(s); err != nil {
				t.Fatalf("sm.Update: %v", err)
			}
			svc := New(sm)
			transport := svc.BuildTransport()

			if tt.wantDialer {
				if transport.DialContext == nil {
					t.Error("expected DialContext to be set for socks5 proxy")
				}
				return
			}
			if tt.wantProxyURL == "" {
				if transport.Proxy != nil {
					req := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
					u, _ := transport.Proxy(req)
					if u != nil {
						t.Errorf("expected no proxy, got %v", u)
					}
				}
				return
			}
			if transport.Proxy == nil {
				t.Fatal("expected transport.Proxy to be set")
			}
			req := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
			u, err := transport.Proxy(req)
			if err != nil {
				t.Fatalf("transport.Proxy: %v", err)
			}
			if u == nil || u.String() != tt.wantProxyURL {
				t.Errorf("proxy URL = %v, want %s", u, tt.wantProxyURL)
			}
		})
	}
}
