package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cspFor(t *testing.T, env string) string {
	t.Helper()
	t.Setenv("HANZO_STATIC_CSP", env)
	rec := httptest.NewRecorder()
	securityHeaders(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	return rec.Header().Get("Content-Security-Policy")
}

// A site selects the shared policy by name instead of restating it, so a new
// first-party origin is added in one place and every site inherits it.
func TestCSPByName(t *testing.T) {
	got := cspFor(t, "hanzo")
	if got != hanzoCSP {
		t.Fatalf("HANZO_STATIC_CSP=hanzo should resolve the shared policy, got %q", got)
	}
	for _, origin := range []string{"https://hanzo.app", "https://analytics.hanzo.ai", "https://insights.hanzo.ai"} {
		if !strings.Contains(got, origin) {
			t.Errorf("shared policy is missing first-party origin %s", origin)
		}
	}
}

// A bespoke policy passes through untouched, and the locked-down default still
// applies when nothing is set.
func TestCSPPassthroughAndDefault(t *testing.T) {
	bespoke := "default-src 'self'; script-src 'self' https://www.youtube.com"
	if got := cspFor(t, bespoke); got != bespoke {
		t.Errorf("bespoke policy should pass through, got %q", got)
	}
	if got := cspFor(t, ""); !strings.Contains(got, "default-src 'none'") {
		t.Errorf("unset should stay locked down, got %q", got)
	}
}
