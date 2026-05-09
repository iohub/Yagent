//go:build integration

// Package testhelpers provides test infrastructure for browser integration tests.
package testhelpers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
)

// NewTestServer creates and returns a started test HTTP server
// with various routes for browser integration testing.
func NewTestServer() *httptest.Server {
	mux := http.NewServeMux()

	// GET / - Base test page
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Test Page</title></head>
<body>
<h1>Test Page</h1>
<p id="description">This is a test page</p>
<a href="/page2" id="link-to-page2">Go to Page 2</a>
<button id="test-button" onclick="document.getElementById('click-result').textContent='clicked'">Click Me</button>
<span id="click-result"></span>
<input id="test-input" type="text" placeholder="Enter text">
<form id="test-form"><input name="username" type="text"></form>
<div id="delay-container"></div>
<div id="scroll-target" style="margin-top:3000px">Scroll Target</div>
`)
		// Add lots of filler content to make the page scrollable
		for i := 1; i <= 100; i++ {
			fmt.Fprintf(w, `<p>Filler paragraph %d — Lorem ipsum dolor sit amet, consectetur adipiscing elit.</p>`, i)
		}
		fmt.Fprint(w, `</body></html>`)
	})

	// GET /page2 - Second test page
	mux.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Page 2</title></head>
<body>
<h1>Page 2</h1>
<a href="/">Back</a>
<p>This is the second test page.</p>
</body></html>`)
	})

	// GET /set-cookie - Set a test cookie
	mux.HandleFunc("/set-cookie", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{
			Name:   "test-cookie",
			Value:  "browser-agent-test",
			Path:   "/",
			MaxAge: 3600,
		})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Cookie Set</title></head>
<body><h1>Cookie Set Successfully</h1><p>test-cookie has been set.</p></body></html>`)
	})

	// GET /delay - Delayed content
	mux.HandleFunc("/delay", func(w http.ResponseWriter, r *http.Request) {
		// Simulate server-side delay
		http.Redirect(w, r, "/delay-content", http.StatusFound)
	})

	// GET /delay-content - The actual delayed content
	mux.HandleFunc("/delay-content", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Delayed Content</title></head>
<body>
<h1>Loading...</h1>
<script>
// Simulate delayed content loading
setTimeout(function() {
	document.body.innerHTML = '<h1>Delayed Content Page</h1><div id="delayed">Delayed Content</div>';
}, 2000);
</script>
</body></html>`)
	})

	// GET /api/data - JSON API endpoint
	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"ok","message":"test data"}`)
	})

	// GET /form - Form page
	mux.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Form Page</title></head>
<body>
<h1>Form Page</h1>
<form method="POST" action="/submit"><input name="name"><button type="submit">Submit</button></form>
</body></html>`)
	})

	// POST /submit - Form submission handler
	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Form Submitted</title></head>
<body>
<h1>Form Submitted</h1>
<p>Name: %s</p>
</body></html>`, r.FormValue("name"))
	})

	// GET /redirect - 302 redirect to /
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})

	// GET /set-multiple-cookies - Set multiple cookies
	mux.HandleFunc("/set-multiple-cookies", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "cookie-a", Value: "value-a", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "cookie-b", Value: "value-b", Path: "/"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html><html><head><title>Multiple Cookies Set</title></head>
<body><h1>Multiple Cookies Set</h1><p>cookie-a and cookie-b have been set.</p></body></html>`)
	})

	return httptest.NewServer(mux)
}

// NewDelayedTestServer creates a test server with a route that actually
// delays the response on the server side to test client-side waiting.
func NewDelayedTestServer() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Test Page</title></head>
<body>
<h1>Test Page</h1>
<p id="description">This is a test page</p>
<button id="test-button" onclick="document.getElementById('click-result').textContent='clicked'">Click Me</button>
<span id="click-result"></span>
<input id="test-input" type="text" placeholder="Enter text">
<div id="delay-container"></div>
<div id="scroll-target" style="margin-top:3000px">Scroll Target</div>
`)
		for i := 1; i <= 100; i++ {
			fmt.Fprintf(w, `<p>Filler paragraph %d — Lorem ipsum dolor sit amet.</p>`, i)
		}
		fmt.Fprint(w, `</body></html>`)
	})

	mux.HandleFunc("/page2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html lang="en">
<head><meta charset="UTF-8"><title>Page 2</title></head>
<body>
<h1>Page 2</h1>
<a href="/">Back</a>
</body></html>`)
	})

	// GET /delayed-content — actually waits 2s before responding
	mux.HandleFunc("/delayed-content", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Loading...</title></head><body>
<h1>Loading...</h1>
<script>setTimeout(function(){document.body.innerHTML='<h1>Delayed Content</h1><div id="delayed">Delayed Content</div>'},2000);</script>
</body></html>`)
	})

	mux.HandleFunc("/set-cookie", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "test-cookie", Value: "browser-agent-test", Path: "/", MaxAge: 3600})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><h1>Cookie Set</h1></body></html>`)
	})

	mux.HandleFunc("/api/data", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok","message":"test data"}`)
	})

	mux.HandleFunc("/form", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<!DOCTYPE html>
<html><head><title>Form Page</title></head><body>
<h1>Form Page</h1>
<form method="POST" action="/submit"><input name="name"><button type="submit">Submit</button></form>
</body></html>`)
	})

	mux.HandleFunc("/submit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<html><body><h1>Submitted: %s</h1></body></html>`, r.FormValue("name"))
	})

	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/", http.StatusFound)
	})

	mux.HandleFunc("/set-multiple-cookies", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "cookie-a", Value: "value-a"})
		http.SetCookie(w, &http.Cookie{Name: "cookie-b", Value: "value-b"})
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<html><body><h1>Done</h1></body></html>`)
	})

	return httptest.NewServer(mux)
}

// ExtractPort extracts the port from the test server URL.
func ExtractPort(serverURL string) string {
	// URLs look like "http://127.0.0.1:12345"
	for i := len(serverURL) - 1; i >= 0; i-- {
		if serverURL[i] == ':' {
			return serverURL[i+1:]
		}
	}
	return ""
}

// IsLocalhost checks if a URL is a localhost URL.
func IsLocalhost(u string) bool {
	return strings.Contains(u, "127.0.0.1") || strings.Contains(u, "localhost")
}
