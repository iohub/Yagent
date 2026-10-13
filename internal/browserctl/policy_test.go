package browserctl

import (
	"strings"
	"testing"
)

func TestCheckURL_RejectsNonHTTPSchemes(t *testing.T) {
	p := NewPolicy(nil, DefaultConfig().DeniedDomains)

	for _, raw := range []string{
		"file:///etc/passwd",
		"chrome://settings",
		"devtools://devtools/bundled/inspector.html",
		"ftp://example.com/x",
		"javascript:alert(1)",
		"",
	} {
		if err := p.CheckURL(raw); err == nil {
			t.Errorf("CheckURL(%q) should be rejected", raw)
		}
	}
}

func TestCheckURL_DeniedAndAllowedLists(t *testing.T) {
	p := NewPolicy([]string{"*.example.com", "docs.test"}, nil)

	if err := p.CheckURL("https://api.example.com/v1"); err != nil {
		t.Errorf("wildcard allow should pass: %v", err)
	}
	if err := p.CheckURL("https://example.com/"); err != nil {
		t.Errorf("apex of wildcard allow should pass: %v", err)
	}
	if err := p.CheckURL("https://evil.test/"); err == nil {
		t.Error("host outside allowlist must be rejected")
	}

	deny := NewPolicy(nil, []string{"*.local", "10.0.0.0/8", "127.0.0.1"})
	for _, raw := range []string{
		"http://router.local/admin",
		"http://10.1.2.3/",
		"http://127.0.0.1:8080/",
	} {
		if err := deny.CheckURL(raw); err == nil {
			t.Errorf("CheckURL(%q) should hit the deny list", raw)
		}
	}
	if err := deny.CheckURL("https://public.test/"); err != nil {
		t.Errorf("public host should pass: %v", err)
	}
}

func TestMatchHost_CIDRAndWildcard(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"*.example.com", "a.example.com", true},
		{"*.example.com", "example.com", true},
		{"*.example.com", "notexample.com", false},
		{"192.168.0.0/16", "192.168.5.5", true},
		{"192.168.0.0/16", "192.169.5.5", false},
		{"example.com", "example.com", true},
		{"169.254.0.0/16", "169.254.169.254", true},
	}
	for _, c := range cases {
		if got := matchHost(c.pattern, c.host); got != c.want {
			t.Errorf("matchHost(%q,%q) = %v, want %v", c.pattern, c.host, got, c.want)
		}
	}
}

func TestBlockedURLPatterns_AlwaysBlocksFileScheme(t *testing.T) {
	p := NewPolicy(nil, []string{"*.local", "10.0.0.0/8"})

	patterns := p.BlockedURLPatterns()
	joined := strings.Join(patterns, ",")
	if !strings.Contains(joined, "file://*") {
		t.Errorf("file scheme must always be blocked, got %v", patterns)
	}
	if strings.Contains(joined, "10.0.0.0/8") {
		t.Errorf("CIDR cannot be expressed as a URL glob and must be skipped, got %v", patterns)
	}
	if !strings.Contains(joined, "*://*.local/*") {
		t.Errorf("deny host should be translated to a URL glob, got %v", patterns)
	}

	payload, err := p.BlockedURLsPayload()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if !strings.HasPrefix(payload, `{"urls":`) {
		t.Errorf("payload shape unexpected: %s", payload)
	}
}

func TestRiskyJS(t *testing.T) {
	risky := []string{
		`document.cookie`,
		`fetch('/api/me')`,
		`localStorage.getItem('token')`,
		`document.querySelector('form').submit()`,
	}
	for _, js := range risky {
		if ok, _ := RiskyJS(js); !ok {
			t.Errorf("RiskyJS(%q) should require authorization", js)
		}
	}

	benign := []string{
		`location.reload()`,
		`history.back()`,
		`document.querySelector('[data-browser-control-ref="e3"]').dispatchEvent(new MouseEvent("mouseover",{bubbles:true}))`,
	}
	for _, js := range benign {
		if ok, reason := RiskyJS(js); ok {
			t.Errorf("RiskyJS(%q) should be allowed (%s)", js, reason)
		}
	}
}
