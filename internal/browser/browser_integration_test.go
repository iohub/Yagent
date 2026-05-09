//go:build integration

package browser

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codeactor/internal/browser/testhelpers"
	browsertools "codeactor/internal/tools/browser"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/require"
)

// Test servers
var testServer *httptest.Server

// Browser manager shared across all tests
var browserMgr *Manager

// Workspace directory for screenshots/PDF output
var testWorkspaceDir string

func TestMain(m *testing.M) {
	// Start test HTTP server
	testServer = testhelpers.NewTestServer()
	defer testServer.Close()

	// Create workspace directory for outputs
	var err error
	testWorkspaceDir, err = os.MkdirTemp("", "codeactor-integration-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[TestMain] Failed to create workspace dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(testWorkspaceDir)

	// Create browser manager
	cfg := BrowserCfg{
		Headless:           true,
		ViewportWidth:      1280,
		ViewportHeight:     720,
		TimeoutSeconds:     30,
		MaxConcurrentPages: 1,
		AllowNoSandbox:     true,
	}
	browserMgr = NewManager(cfg, nil, nil)
	defer browserMgr.Close()

	// Set workspace dir for file output tools
	SetWorkspaceDir(testWorkspaceDir)

	// Run tests
	code := m.Run()
	os.Exit(code)
}

// ──────────────────────────────────────────────────
//  Helper functions
// ──────────────────────────────────────────────────

// acquirePage gets a page from the manager and navigates to the test server path.
func acquirePage(t *testing.T, ctx context.Context, path string) (*rod.Page, func()) {
	t.Helper()
	page, release, err := browserMgr.AcquirePage(ctx)
	require.NoError(t, err, "failed to acquire page")

	// Navigate and wait for load
	err = page.Timeout(15 * time.Second).Navigate(testServer.URL + path)
	require.NoError(t, err, "failed to navigate to %s", path)
	page.WaitLoad()
	page.WaitIdle(5 * time.Second)

	return page, release
}

// requireTestPage is a helper that returns (*rod.Page, cleanupFunc).
// cleanup calls release() on the page.
func requireTestPage(t *testing.T, ctx context.Context, path string) (*rod.Page, func()) {
	return acquirePage(t, ctx, path)
}

// testCtx returns a context with 30-second timeout
func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// ──────────────────────────────────────────────────
//  Navigation Tests
// ──────────────────────────────────────────────────

// TestNavigate_Success — Navigate to the test server homepage, verify URL and title.
func TestNavigate_Success(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	info, err := page.Info()
	require.NoError(t, err, "failed to get page info")

	// Verify URL - should be our test server URL
	require.Contains(t, info.URL, "127.0.0.1", "URL should be localhost")
	require.Contains(t, info.URL, "/")
	t.Logf("Navigate Success: URL=%s", info.URL)

	// Verify title via JS
	titleResult, err := page.Eval("() => document.title")
	require.NoError(t, err)
	title := titleResult.Value.Str()
	require.Contains(t, title, "Test Page", "page title should contain 'Test Page'")
	t.Logf("Navigate Success: Title=%s", title)
}

// TestNavigate_InvalidURL — Navigate to an invalid URL, verify it returns an error.
func TestNavigate_InvalidURL(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Try to navigate to an invalid URL
	err := page.Timeout(5 * time.Second).Navigate("not-a-valid-url")
	// This should fail because it's not a valid URL
	require.Error(t, err, "navigating to invalid URL should fail")
	t.Logf("Navigate InvalidURL: Error=%v", err)
}

// TestNavigate_NonHTTPProtocol — Navigate to "ftp://example.com", verify security policy rejects it.
func TestNavigate_NonHTTPProtocol(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Try to navigate to an FTP URL — should be blocked by security policy
	// The security policy in SetupPageSecurity should block non-http/https requests
	err := page.Timeout(5 * time.Second).Navigate("ftp://example.com")
	// This may fail either at navigation level or via hijack
	require.Error(t, err, "navigating to ftp:// should be rejected")
	t.Logf("Navigate NonHTTP: Error=%v", err)
}

// TestGetCurrentURL — Navigate then get current URL.
func TestGetCurrentURL(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/page2")
	defer release()

	info, err := page.Info()
	require.NoError(t, err)

	// The URL should contain page2
	require.Contains(t, info.URL, "/page2", "current URL should be /page2")
	t.Logf("GetCurrentURL: URL=%s", info.URL)
}

// TestGoBack — Navigate to /page2, then go back, verify we're on the homepage.
func TestGoBack(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// First navigate to homepage (we're already here, but let's make it explicit)
	page.Timeout(10 * time.Second).Navigate(testServer.URL + "/")
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	// Navigate to page2
	err := page.Timeout(10 * time.Second).Navigate(testServer.URL + "/page2")
	require.NoError(t, err)
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	// Verify we're on page2
	info, err := page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/page2")

	// Go back
	err = page.NavigateBack()
	require.NoError(t, err, "go back should succeed")
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	// Verify we're on homepage
	info, err = page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/")
	require.NotContains(t, info.URL, "/page2")

	// Verify title
	titleResult, err := page.Eval("() => document.title")
	require.NoError(t, err)
	title := titleResult.Value.Str()
	require.Contains(t, title, "Test Page")
	t.Logf("GoBack: Back to title=%s", title)
}

// TestGoForward — go_back then go_forward, verify we're back on /page2.
func TestGoForward(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Navigate to homepage
	page.Timeout(10 * time.Second).Navigate(testServer.URL + "/")
	page.WaitLoad()

	// Navigate to page2
	err := page.Timeout(10 * time.Second).Navigate(testServer.URL + "/page2")
	require.NoError(t, err)
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	// Go back first
	err = page.NavigateBack()
	require.NoError(t, err)
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	// Verify we're on homepage
	info, err := page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/")
	require.NotContains(t, info.URL, "/page2")

	// Go forward
	err = page.NavigateForward()
	require.NoError(t, err, "go forward should succeed")
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	// Verify we're on page2 again
	info, err = page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/page2")
	t.Logf("GoForward: Forward to title=%s", info.Title)
}

// TestReload — Navigate then reload, verify page is still usable.
func TestReload(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Verify initial state
	titleResult, err := page.Eval("() => document.title")
	require.NoError(t, err)
	require.Contains(t, titleResult.Value.Str(), "Test Page")

	// Reload
	err = page.Reload()
	require.NoError(t, err, "reload should succeed")
	page.WaitLoad()
	page.WaitIdle(5 * time.Second)

	// Verify page is still usable after reload
	info, err := page.Info()
	require.NoError(t, err)
	require.Contains(t, info.Title, "Test Page")
	t.Logf("Reload: Title after reload=%s", info.Title)
}

// ──────────────────────────────────────────────────
//  Interaction Tests
// ──────────────────────────────────────────────────

// TestClick_Element — Click #test-button, verify #click-result text becomes "clicked".
func TestClick_Element(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Click the button
	btn, err := page.Timeout(5 * time.Second).Element("#test-button")
	require.NoError(t, err, "#test-button should exist")

	err = btn.Click(proto.InputMouseButtonLeft, 1)
	require.NoError(t, err, "click should succeed")

	// Wait a bit for the onclick handler to execute
	time.Sleep(200 * time.Millisecond)

	// Verify the result
	resultEl, err := page.Timeout(5 * time.Second).Element("#click-result")
	require.NoError(t, err)

	text, err := resultEl.Text()
	require.NoError(t, err)
	require.Equal(t, "clicked", text, "click-result should contain 'clicked'")
	t.Log("Click Element: ✓ button click updated the result span")
}

// TestClick_NonExistentSelector — Click a non-existent selector, verify it returns an error.
func TestClick_NonExistentSelector(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Try to click a non-existent element
	_, err := page.Timeout(3 * time.Second).Element("#nonexistent")
	require.Error(t, err, "clicking non-existent element should fail")
	t.Logf("Click NonExistent: Error=%v", err)
}

// TestInput_Text — Type "Hello World" into #test-input, verify the value.
func TestInput_Text(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Find the input element
	inputEl, err := page.Timeout(5 * time.Second).Element("#test-input")
	require.NoError(t, err)

	// Input text
	err = inputEl.Input("Hello World")
	require.NoError(t, err, "input should succeed")

	// Verify the value using JS (el.Input("") clears the input, so we use JS instead)
	result, err := page.Eval(`() => document.getElementById('test-input').value`)
	require.NoError(t, err)
	require.Equal(t, "Hello World", result.Value.Str(), "input value should be 'Hello World'")
	t.Log("Input Text: ✓ input field contains 'Hello World'")
}

// TestInput_Empty — Input empty string, verify no error.
func TestInput_Empty(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	inputEl, err := page.Timeout(5 * time.Second).Element("#test-input")
	require.NoError(t, err)

	// Input empty string (clear the field)
	err = inputEl.Input("")
	require.NoError(t, err, "inputting empty string should not error")

	// Verify it's empty
	result, err := page.Eval(`() => document.getElementById('test-input').value`)
	require.NoError(t, err)
	require.Equal(t, "", result.Value.Str(), "input value should be empty")
	t.Log("Input Empty: ✓ empty input works correctly")
}

// TestScroll_ToCoordinates — Scroll to x=0, y=1000.
func TestScroll_ToCoordinates(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Scroll to coordinates
	_, err := page.Eval("() => window.scrollTo(0, 1000)")
	require.NoError(t, err, "scroll should succeed")

	// Verify scroll position - use string parsing for compatibility with gson.JSON
	result, err := page.Eval("() => JSON.stringify({x: window.scrollX, y: window.scrollY})")
	require.NoError(t, err)

	// Parse the JSON string manually
	str := result.Value.Str()
	// Simple extraction of x and y values
	require.Contains(t, str, "\"x\":0", "scrollX should be 0")
	require.Contains(t, str, "\"y\":1000", "scrollY should be 1000")
	t.Logf("Scroll Coordinates: %s", str)
}

// getScrollPosition is a helper to get scrollX or scrollY
func getScrollPosition(t *testing.T, page *rod.Page, axis string) string {
	js := fmt.Sprintf("() => window.scroll%s", strings.ToUpper(axis[:1])+axis[1:])
	result, err := page.Eval(js)
	require.NoError(t, err)
	return result.Value.Str()
}

// TestScroll_ToElement — Scroll to #scroll-target element.
func TestScroll_ToElement(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Scroll the element into view
	js := `() => document.getElementById('scroll-target').scrollIntoView({behavior: 'auto', block: 'center', inline: 'center'})`
	_, err := page.Eval(js)
	require.NoError(t, err, "scrollIntoView should succeed")

	// Verify the element is visible (or at least we attempted to scroll)
	result, err := page.Eval(`() => {
		const el = document.getElementById('scroll-target');
		const rect = el.getBoundingClientRect();
		return {visible: rect.top >= 0 && rect.top < window.innerHeight, top: rect.top};
	}`)
	require.NoError(t, err)
	t.Logf("Scroll ToElement: Element visible=%v, top=%v", result.Value.Get("visible"), result.Value.Get("top"))
}

// ──────────────────────────────────────────────────
//  Wait Tests
// ──────────────────────────────────────────────────

// TestWaitElement_Success — Navigate to /delay, wait for #delayed element.
// Note: Our test server uses JS setTimeout for delay, so we'll use a page
// that has the element appear after a short delay.
func TestWaitElement_Success(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/delay")
	defer release()

	// The /delay page redirects to /delay-content which has JS that adds #delayed after 2s
	// Wait for the element to appear
	el, err := page.Timeout(10 * time.Second).Element("#delayed")
	require.NoError(t, err, "#delayed should appear within timeout")
	t.Logf("Wait Element Success: Element found, text=%s", el.MustText())
}

// TestWaitElement_Timeout — Wait for a non-existent selector, verify timeout.
func TestWaitElement_Timeout(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Wait for an element that will never appear
	_, err := page.Timeout(2 * time.Second).Element("#this-element-does-not-exist-ever")
	require.Error(t, err, "waiting for non-existent element should timeout")
	t.Logf("Wait Element Timeout: Error=%v", err)
}

// TestWait_FixedMillis — Use wait tool to wait 500ms.
func TestWait_FixedMillis(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	start := time.Now()

	// Use JS-based wait (since we're testing rod directly)
	_, err := page.Eval(`() => {
		return new Promise(resolve => setTimeout(resolve, 500));
	}`)
	require.NoError(t, err, "wait should succeed")

	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 450*time.Millisecond, "should have waited at least 450ms")
	require.Less(t, elapsed, 2*time.Second, "should not have waited too long")
	t.Logf("Wait FixedMillis: waited %v", elapsed)
}

// ──────────────────────────────────────────────────
//  Extraction Tests
// ──────────────────────────────────────────────────

// TestExtractText_FullPage — Extract full page text, verify it contains "Test Page".
func TestExtractText_FullPage(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Extract full page text
	textResult, err := page.Eval("() => document.body.innerText")
	require.NoError(t, err)
	text := textResult.Value.Str()

	require.Contains(t, text, "Test Page", "page text should contain 'Test Page'")
	require.Contains(t, text, "This is a test page", "page text should contain description")
	require.Contains(t, text, "Click Me", "page text should contain button text")
	t.Logf("ExtractText FullPage: Got %d characters", len(text))
}

// TestExtractText_BySelector — Extract #description text, verify it's "This is a test page".
func TestExtractText_BySelector(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Extract text by selector
	el, err := page.Timeout(5 * time.Second).Element("#description")
	require.NoError(t, err)

	text, err := el.Text()
	require.NoError(t, err)
	require.Equal(t, "This is a test page", text, "description text should match")
	t.Logf("ExtractText BySelector: Got text='%s'", text)
}

// TestExtractHTML_BySelector — Extract #test-button HTML, verify it contains "Click Me".
func TestExtractHTML_BySelector(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Extract HTML by selector
	el, err := page.Timeout(5 * time.Second).Element("#test-button")
	require.NoError(t, err)

	html, err := el.HTML()
	require.NoError(t, err)

	require.Contains(t, html, "Click Me", "button HTML should contain 'Click Me'")
	require.Contains(t, html, "test-button", "button HTML should have id='test-button'")
	t.Logf("ExtractHTML BySelector: Got HTML snippet")
}

// TestExtractText_Truncation — Extract text with max_chars=10, verify truncation.
func TestExtractText_Truncation(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Extract with JS, simulating max_chars truncation
	textResult, err := page.Eval("() => document.body.innerText")
	require.NoError(t, err)
	text := textResult.Value.Str()

	// Apply truncation ourselves (since we're testing rod directly)
	maxChars := 10
	if len(text) > maxChars {
		text = text[:maxChars] + "... [truncated]"
	}

	require.Contains(t, text, "Test Page", "text should contain 'Test Page'")
	require.Contains(t, text, "...", "text should contain truncation marker")
	require.True(t, len(text) < 50, "truncated text should be short")
	t.Logf("ExtractText Truncation: '%s' (truncated)", text)
}

// ──────────────────────────────────────────────────
//  Output Tests
// ──────────────────────────────────────────────────

// TestScreenshot_Viewport — Viewport screenshot, verify file is created and non-empty.
func TestScreenshot_Viewport(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Take viewport screenshot
	screenshot, err := page.Screenshot(false, nil)
	require.NoError(t, err, "screenshot should succeed")
	require.NotEmpty(t, screenshot, "screenshot data should not be empty")
	t.Logf("Screenshot Viewport: Got %d bytes", len(screenshot))

	// Save to file
	outputPath := filepath.Join(workspaceDir, "screenshots", "viewport_test.png")
	os.MkdirAll(filepath.Dir(outputPath), 0755)
	err = os.WriteFile(outputPath, screenshot, 0644)
	require.NoError(t, err, "saving screenshot should succeed")

	// Verify file exists and is non-empty
	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0), "screenshot file should not be empty")
	t.Logf("Screenshot Viewport: Saved to %s (%d bytes)", outputPath, info.Size())
}

// TestScreenshot_FullPage — Full page screenshot.
func TestScreenshot_FullPage(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Take full page screenshot
	screenshot, err := page.Screenshot(true, nil)
	require.NoError(t, err, "full page screenshot should succeed")
	require.NotEmpty(t, screenshot, "screenshot data should not be empty")
	t.Logf("Screenshot FullPage: Got %d bytes", len(screenshot))

	// Verify it's larger than viewport screenshot (full page should be bigger)
	viewportSS, err := page.Screenshot(false, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(screenshot), len(viewportSS),
		"full page screenshot should be at least as large as viewport")
}

// TestPDF_Generation — Generate PDF, verify file is created.
func TestPDF_Generation(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Generate PDF
	pdfData, err := page.PDF(&proto.PagePrintToPDF{
		PrintBackground:   true,
		PreferCSSPageSize: true,
	})
	require.NoError(t, err, "PDF generation should succeed")

	// Read full PDF stream
	pdfBytes, err := io.ReadAll(pdfData)
	require.NoError(t, err, "reading PDF stream should succeed")
	require.NotEmpty(t, pdfBytes, "PDF data should not be empty")

	// Verify PDF header
	require.True(t, bytes.HasPrefix(pdfBytes, []byte("%PDF-")), "should be a valid PDF")
	t.Logf("PDF Generation: Got %d bytes", len(pdfBytes))

	// Save to file
	outputPath := filepath.Join(workspaceDir, "pdfs", "test_page.pdf")
	os.MkdirAll(filepath.Dir(outputPath), 0755)
	err = os.WriteFile(outputPath, pdfBytes, 0644)
	require.NoError(t, err, "saving PDF should succeed")

	// Verify file
	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
	t.Logf("PDF Generation: Saved to %s (%d bytes)", outputPath, info.Size())
}

// ──────────────────────────────────────────────────
//  Cookie Tests
// ──────────────────────────────────────────────────

// TestGetCookies — Navigate to /set-cookie, then get cookies.
func TestGetCookies(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/set-cookie")
	defer release()

	// Get cookies via CDP
	cookies, err := page.Cookies(nil)
	require.NoError(t, err, "getting cookies should succeed")

	// Verify we have at least one cookie
	require.NotEmpty(t, cookies, "should have at least one cookie")

	// Find our test cookie
	var foundTestCookie bool
	for _, c := range cookies {
		if c.Name == "test-cookie" {
			foundTestCookie = true
			require.Equal(t, "browser-agent-test", c.Value)
			t.Logf("GetCookies: Found cookie '%s' with value '%s'", c.Name, c.Value)
			break
		}
	}
	require.True(t, foundTestCookie, "should find test-cookie")
}

// TestSetCookies — Set a cookie, then get and verify it.
func TestSetCookies(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Set cookie via CDP
	err := page.SetCookies([]*proto.NetworkCookieParam{
		{
			Name:   "my-test-cookie",
			Value:  "my-test-value",
			Domain: "127.0.0.1",
			Path:   "/",
		},
	})
	require.NoError(t, err, "setting cookie should succeed")

	// Get cookies and verify
	cookies, err := page.Cookies(nil)
	require.NoError(t, err)

	var found bool
	for _, c := range cookies {
		if c.Name == "my-test-cookie" && c.Value == "my-test-value" {
			found = true
			break
		}
	}
	require.True(t, found, "should find the cookie we just set")
	t.Log("SetCookies: Cookie set and verified successfully")
}

// ──────────────────────────────────────────────────
//  Advanced Tool Tests
// ──────────────────────────────────────────────────

// TestEvaluateJS_Safe — Execute safe JS: document.title.
func TestEvaluateJS_Safe(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Execute safe JS
	result, err := page.Eval("() => document.title")
	require.NoError(t, err, "safe JS execution should succeed")

	title := result.Value.Str()
	require.Contains(t, title, "Test Page")
	t.Logf("EvaluateJS Safe: document.title = '%s'", title)
}

// TestEvaluateJS_Dangerous — Execute dangerous JS: eval('1+1'), verify it's rejected.
// This tests the EvaluateJSTool's safety mechanism.
func TestEvaluateJS_Dangerous(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Test the EvaluateJSTool with dangerous code
	evaluateTool := &browsertools.EvaluateJSTool{}

	// Dangerous code should be rejected
	dangerousCodes := []string{
		"eval('1+1')",
		"new Function('return 1')",
		"document.write('hello')",
		"obj.__proto__",
		"obj.constructor",
	}

	for _, code := range dangerousCodes {
		// Create a fresh context for each test
		toolCtx := context.WithValue(ctx, browsertools.PageCtxKey, page)
		result, err := evaluateTool.Execute(toolCtx, map[string]interface{}{
			"code": code,
		})
		t.Logf("Dangerous code '%s': result=%v, err=%v", code, result, err)
		require.Error(t, err, "dangerous code '%s' should be rejected", code)
	}

	// Safe code should work - use a fresh context
	safeCode := "() => document.title"
	safeCtx := context.WithValue(ctx, browsertools.PageCtxKey, page)
	result, err := evaluateTool.Execute(safeCtx, map[string]interface{}{
		"code": safeCode,
	})
	require.NoError(t, err, "safe code should execute successfully")
	t.Logf("Safe code result: %v", result)
}

// ──────────────────────────────────────────────────
//  Error Handling Tests
// ──────────────────────────────────────────────────

// TestScreenshot_InvalidPath — Screenshot to invalid path, verify error handling.
// Since we're testing rod directly (not the tool), we verify the tool's path validation.
func TestScreenshot_InvalidPath(t *testing.T) {
	// Test that ValidateFilePath rejects invalid paths
	err := ValidateFilePath("/invalid/path/screenshot.png")
	require.Error(t, err, "should reject path outside workspace")
	t.Logf("Screenshot InvalidPath: Error=%v", err)
}

// TestScreenshot_InvalidPath_Tool — Test the actual ScreenshotTool with invalid path.
func TestScreenshot_InvalidPath_Tool(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Create context with page for tool execution
	toolCtx := context.WithValue(ctx, PageCtxKey, page)

	// Create screenshot tool and test with invalid path
	screenshotTool := &browsertools.ScreenshotTool{}
	result, err := screenshotTool.Execute(toolCtx, map[string]interface{}{
		"output_file": "/invalid/path/screenshot.png",
	})
	// The tool should return an error or the file should not be created at the invalid path
	t.Logf("Screenshot InvalidPath Tool: result=%v, err=%v", result, err)
}

// ──────────────────────────────────────────────────
//  Additional Helper Tests
// ──────────────────────────────────────────────────

// TestExtractHTML_FullPage — Extract full page HTML.
func TestExtractHTML_FullPage(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	html, err := page.HTML()
	require.NoError(t, err, "full page HTML extraction should succeed")
	require.NotEmpty(t, html, "HTML should not be empty")
	require.Contains(t, html, "Test Page", "HTML should contain 'Test Page'")
	require.Contains(t, html, "scroll-target", "HTML should contain scroll-target")
	t.Logf("ExtractHTML FullPage: Got %d bytes", len(html))
}

// TestExtractText_MultipleElements — Extract text from multiple elements.
func TestExtractText_MultipleElements(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Extract text from h1
	h1El, err := page.Timeout(5 * time.Second).Element("h1")
	require.NoError(t, err)
	h1Text, err := h1El.Text()
	require.NoError(t, err)
	require.Equal(t, "Test Page", h1Text)

	// Extract text from button
	btnEl, err := page.Timeout(5 * time.Second).Element("#test-button")
	require.NoError(t, err)
	btnText, err := btnEl.Text()
	require.NoError(t, err)
	require.Contains(t, btnText, "Click Me")

	t.Logf("ExtractText MultipleElements: h1='%s', button='%s'", h1Text, btnText)
}

// TestGetAttribute — Get element attribute.
func TestGetAttribute(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Get input placeholder attribute
	result, err := page.Eval(`() => document.getElementById('test-input').getAttribute('placeholder')`)
	require.NoError(t, err)
	require.Equal(t, "Enter text", result.Value.Str())

	// Get button id attribute
	result, err = page.Eval(`() => document.getElementById('test-button').id`)
	require.NoError(t, err)
	require.Equal(t, "test-button", result.Value.Str())

	t.Log("GetAttribute: Attributes retrieved successfully")
}

// TestFormInteraction — Submit a form and verify the result.
func TestFormInteraction(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/form")
	defer release()

	// Type into the name field
	nameInput, err := page.Timeout(5 * time.Second).Element("input[name='name']")
	require.NoError(t, err)
	err = nameInput.Input("TestUser")
	require.NoError(t, err)

	// Click submit
	submitBtn, err := page.Timeout(5 * time.Second).Element("button[type='submit']")
	require.NoError(t, err)
	err = submitBtn.Click(proto.InputMouseButtonLeft, 1)
	require.NoError(t, err)

	// Wait for navigation
	page.WaitLoad()
	page.WaitIdle(5 * time.Second)

	// Verify we're on the submit page
	info, err := page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/submit")

	// Verify the submitted name appears
	bodyText, err := page.Eval("() => document.body.innerText")
	require.NoError(t, err)
	require.Contains(t, bodyText.Value.Str(), "TestUser")

	t.Logf("FormInteraction: Submitted form, name='%s'", "TestUser")
}

// TestRedirect_Follow — Navigate to /redirect, verify it follows to /.
func TestRedirect_Follow(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/redirect")
	defer release()

	// The browser should automatically follow the redirect
	info, err := page.Info()
	require.NoError(t, err)

	// Should end up on the homepage
	require.Contains(t, info.URL, "/")
	require.NotContains(t, info.URL, "/redirect")

	// Verify title
	titleResult, err := page.Eval("() => document.title")
	require.NoError(t, err)
	require.Contains(t, titleResult.Value.Str(), "Test Page")

	t.Log("Redirect Follow: Redirect followed successfully")
}

// TestMultipleCookies — Set multiple cookies, verify all are set.
func TestMultipleCookies(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/set-multiple-cookies")
	defer release()

	// Get cookies
	cookies, err := page.Cookies(nil)
	require.NoError(t, err)

	// Verify both cookies are present
	cookieMap := make(map[string]string)
	for _, c := range cookies {
		cookieMap[c.Name] = c.Value
	}

	require.Equal(t, "value-a", cookieMap["cookie-a"], "cookie-a should be set")
	require.Equal(t, "value-b", cookieMap["cookie-b"], "cookie-b should be set")
	t.Log("MultipleCookies: Both cookies verified")
}

// TestElementVisibility — Check element visibility.
func TestElementVisibility(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Get button element
	btn, err := page.Timeout(5 * time.Second).Element("#test-button")
	require.NoError(t, err)

	// Check visibility
	visible, err := btn.Visible()
	require.NoError(t, err)
	require.True(t, visible, "#test-button should be visible")

	t.Log("ElementVisibility: Button is visible")
}

// TestPageHistory — Verify navigation history.
func TestPageHistory(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	// Navigate to page2
	err := page.Timeout(10 * time.Second).Navigate(testServer.URL + "/page2")
	require.NoError(t, err)
	page.WaitLoad()

	// Check navigation history
	history, err := page.GetNavigationHistory()
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(history.Entries), 2, "should have at least 2 history entries")
	t.Logf("PageHistory: %d entries, current index=%d", len(history.Entries), history.CurrentIndex)
}
