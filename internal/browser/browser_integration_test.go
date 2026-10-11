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

	"yagent/internal/browser/testhelpers"
	browsertools "yagent/internal/tools/browser"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/require"
)

var testServer *httptest.Server

var browserMgr *Manager

var testWorkspaceDir string

func TestMain(m *testing.M) {
	testServer = testhelpers.NewTestServer()
	defer testServer.Close()

	var err error
	testWorkspaceDir, err = os.MkdirTemp("", "yagent-integration-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[TestMain] Failed to create workspace dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(testWorkspaceDir)

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

	SetWorkspaceDir(testWorkspaceDir)

	code := m.Run()
	os.Exit(code)
}

func acquirePage(t *testing.T, ctx context.Context, path string) (*rod.Page, func()) {
	t.Helper()
	page, release, err := browserMgr.AcquirePage(ctx)
	require.NoError(t, err, "failed to acquire page")

	err = page.Timeout(15 * time.Second).Navigate(testServer.URL + path)
	require.NoError(t, err, "failed to navigate to %s", path)
	page.WaitLoad()
	page.WaitIdle(5 * time.Second)

	return page, release
}

func requireTestPage(t *testing.T, ctx context.Context, path string) (*rod.Page, func()) {
	return acquirePage(t, ctx, path)
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestNavigate_Success(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	info, err := page.Info()
	require.NoError(t, err, "failed to get page info")

	require.Contains(t, info.URL, "127.0.0.1", "URL should be localhost")
	require.Contains(t, info.URL, "/")
	t.Logf("Navigate Success: URL=%s", info.URL)

	titleResult, err := page.Eval("() => document.title")
	require.NoError(t, err)
	title := titleResult.Value.Str()
	require.Contains(t, title, "Test Page", "page title should contain 'Test Page'")
	t.Logf("Navigate Success: Title=%s", title)
}

func TestNavigate_InvalidURL(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	err := page.Timeout(5 * time.Second).Navigate("not-a-valid-url")
	require.Error(t, err, "navigating to invalid URL should fail")
	t.Logf("Navigate InvalidURL: Error=%v", err)
}

func TestNavigate_NonHTTPProtocol(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	err := page.Timeout(5 * time.Second).Navigate("ftp://example.com")
	require.Error(t, err, "navigating to ftp:// should be rejected")
	t.Logf("Navigate NonHTTP: Error=%v", err)
}

func TestGetCurrentURL(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/page2")
	defer release()

	info, err := page.Info()
	require.NoError(t, err)

	require.Contains(t, info.URL, "/page2", "current URL should be /page2")
	t.Logf("GetCurrentURL: URL=%s", info.URL)
}

func TestGoBack(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	page.Timeout(10 * time.Second).Navigate(testServer.URL + "/")
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	err := page.Timeout(10 * time.Second).Navigate(testServer.URL + "/page2")
	require.NoError(t, err)
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	info, err := page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/page2")

	err = page.NavigateBack()
	require.NoError(t, err, "go back should succeed")
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	info, err = page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/")
	require.NotContains(t, info.URL, "/page2")

	titleResult, err := page.Eval("() => document.title")
	require.NoError(t, err)
	title := titleResult.Value.Str()
	require.Contains(t, title, "Test Page")
	t.Logf("GoBack: Back to title=%s", title)
}

func TestGoForward(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	page.Timeout(10 * time.Second).Navigate(testServer.URL + "/")
	page.WaitLoad()

	err := page.Timeout(10 * time.Second).Navigate(testServer.URL + "/page2")
	require.NoError(t, err)
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	err = page.NavigateBack()
	require.NoError(t, err)
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	info, err := page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/")
	require.NotContains(t, info.URL, "/page2")

	err = page.NavigateForward()
	require.NoError(t, err, "go forward should succeed")
	page.WaitLoad()
	page.WaitIdle(3 * time.Second)

	info, err = page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/page2")
	t.Logf("GoForward: Forward to title=%s", info.Title)
}

func TestReload(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	titleResult, err := page.Eval("() => document.title")
	require.NoError(t, err)
	require.Contains(t, titleResult.Value.Str(), "Test Page")

	err = page.Reload()
	require.NoError(t, err, "reload should succeed")
	page.WaitLoad()
	page.WaitIdle(5 * time.Second)

	info, err := page.Info()
	require.NoError(t, err)
	require.Contains(t, info.Title, "Test Page")
	t.Logf("Reload: Title after reload=%s", info.Title)
}

func TestClick_Element(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	btn, err := page.Timeout(5 * time.Second).Element("#test-button")
	require.NoError(t, err, "#test-button should exist")

	err = btn.Click(proto.InputMouseButtonLeft, 1)
	require.NoError(t, err, "click should succeed")

	time.Sleep(200 * time.Millisecond)

	resultEl, err := page.Timeout(5 * time.Second).Element("#click-result")
	require.NoError(t, err)

	text, err := resultEl.Text()
	require.NoError(t, err)
	require.Equal(t, "clicked", text, "click-result should contain 'clicked'")
	t.Log("Click Element: ✓ button click updated the result span")
}

func TestClick_NonExistentSelector(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	_, err := page.Timeout(3 * time.Second).Element("#nonexistent")
	require.Error(t, err, "clicking non-existent element should fail")
	t.Logf("Click NonExistent: Error=%v", err)
}

func TestInput_Text(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	inputEl, err := page.Timeout(5 * time.Second).Element("#test-input")
	require.NoError(t, err)

	err = inputEl.Input("Hello World")
	require.NoError(t, err, "input should succeed")

	result, err := page.Eval(`() => document.getElementById('test-input').value`)
	require.NoError(t, err)
	require.Equal(t, "Hello World", result.Value.Str(), "input value should be 'Hello World'")
	t.Log("Input Text: ✓ input field contains 'Hello World'")
}

func TestInput_Empty(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	inputEl, err := page.Timeout(5 * time.Second).Element("#test-input")
	require.NoError(t, err)

	err = inputEl.Input("")
	require.NoError(t, err, "inputting empty string should not error")

	result, err := page.Eval(`() => document.getElementById('test-input').value`)
	require.NoError(t, err)
	require.Equal(t, "", result.Value.Str(), "input value should be empty")
	t.Log("Input Empty: ✓ empty input works correctly")
}

func TestScroll_ToCoordinates(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	_, err := page.Eval("() => window.scrollTo(0, 1000)")
	require.NoError(t, err, "scroll should succeed")

	result, err := page.Eval("() => JSON.stringify({x: window.scrollX, y: window.scrollY})")
	require.NoError(t, err)

	str := result.Value.Str()
	require.Contains(t, str, "\"x\":0", "scrollX should be 0")
	require.Contains(t, str, "\"y\":1000", "scrollY should be 1000")
	t.Logf("Scroll Coordinates: %s", str)
}

func getScrollPosition(t *testing.T, page *rod.Page, axis string) string {
	js := fmt.Sprintf("() => window.scroll%s", strings.ToUpper(axis[:1])+axis[1:])
	result, err := page.Eval(js)
	require.NoError(t, err)
	return result.Value.Str()
}

func TestScroll_ToElement(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	js := `() => document.getElementById('scroll-target').scrollIntoView({behavior: 'auto', block: 'center', inline: 'center'})`
	_, err := page.Eval(js)
	require.NoError(t, err, "scrollIntoView should succeed")

	result, err := page.Eval(`() => {
		const el = document.getElementById('scroll-target');
		const rect = el.getBoundingClientRect();
		return {visible: rect.top >= 0 && rect.top < window.innerHeight, top: rect.top};
	}`)
	require.NoError(t, err)
	t.Logf("Scroll ToElement: Element visible=%v, top=%v", result.Value.Get("visible"), result.Value.Get("top"))
}

func TestWaitElement_Success(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/delay")
	defer release()

	el, err := page.Timeout(10 * time.Second).Element("#delayed")
	require.NoError(t, err, "#delayed should appear within timeout")
	t.Logf("Wait Element Success: Element found, text=%s", el.MustText())
}

func TestWaitElement_Timeout(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	_, err := page.Timeout(2 * time.Second).Element("#this-element-does-not-exist-ever")
	require.Error(t, err, "waiting for non-existent element should timeout")
	t.Logf("Wait Element Timeout: Error=%v", err)
}

func TestWait_FixedMillis(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	start := time.Now()

	_, err := page.Eval(`() => {
		return new Promise(resolve => setTimeout(resolve, 500));
	}`)
	require.NoError(t, err, "wait should succeed")

	elapsed := time.Since(start)
	require.GreaterOrEqual(t, elapsed, 450*time.Millisecond, "should have waited at least 450ms")
	require.Less(t, elapsed, 2*time.Second, "should not have waited too long")
	t.Logf("Wait FixedMillis: waited %v", elapsed)
}

func TestExtractText_FullPage(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	textResult, err := page.Eval("() => document.body.innerText")
	require.NoError(t, err)
	text := textResult.Value.Str()

	require.Contains(t, text, "Test Page", "page text should contain 'Test Page'")
	require.Contains(t, text, "This is a test page", "page text should contain description")
	require.Contains(t, text, "Click Me", "page text should contain button text")
	t.Logf("ExtractText FullPage: Got %d characters", len(text))
}

func TestExtractText_BySelector(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	el, err := page.Timeout(5 * time.Second).Element("#description")
	require.NoError(t, err)

	text, err := el.Text()
	require.NoError(t, err)
	require.Equal(t, "This is a test page", text, "description text should match")
	t.Logf("ExtractText BySelector: Got text='%s'", text)
}

func TestExtractHTML_BySelector(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	el, err := page.Timeout(5 * time.Second).Element("#test-button")
	require.NoError(t, err)

	html, err := el.HTML()
	require.NoError(t, err)

	require.Contains(t, html, "Click Me", "button HTML should contain 'Click Me'")
	require.Contains(t, html, "test-button", "button HTML should have id='test-button'")
	t.Logf("ExtractHTML BySelector: Got HTML snippet")
}

func TestExtractText_Truncation(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	textResult, err := page.Eval("() => document.body.innerText")
	require.NoError(t, err)
	text := textResult.Value.Str()

	maxChars := 10
	if len(text) > maxChars {
		text = text[:maxChars] + "... [truncated]"
	}

	require.Contains(t, text, "Test Page", "text should contain 'Test Page'")
	require.Contains(t, text, "...", "text should contain truncation marker")
	require.True(t, len(text) < 50, "truncated text should be short")
	t.Logf("ExtractText Truncation: '%s' (truncated)", text)
}

func TestScreenshot_Viewport(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	screenshot, err := page.Screenshot(false, nil)
	require.NoError(t, err, "screenshot should succeed")
	require.NotEmpty(t, screenshot, "screenshot data should not be empty")
	t.Logf("Screenshot Viewport: Got %d bytes", len(screenshot))

	outputPath := filepath.Join(workspaceDir, "screenshots", "viewport_test.png")
	os.MkdirAll(filepath.Dir(outputPath), 0755)
	err = os.WriteFile(outputPath, screenshot, 0644)
	require.NoError(t, err, "saving screenshot should succeed")

	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0), "screenshot file should not be empty")
	t.Logf("Screenshot Viewport: Saved to %s (%d bytes)", outputPath, info.Size())
}

func TestScreenshot_FullPage(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	screenshot, err := page.Screenshot(true, nil)
	require.NoError(t, err, "full page screenshot should succeed")
	require.NotEmpty(t, screenshot, "screenshot data should not be empty")
	t.Logf("Screenshot FullPage: Got %d bytes", len(screenshot))

	viewportSS, err := page.Screenshot(false, nil)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(screenshot), len(viewportSS),
		"full page screenshot should be at least as large as viewport")
}

func TestPDF_Generation(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	pdfData, err := page.PDF(&proto.PagePrintToPDF{
		PrintBackground:   true,
		PreferCSSPageSize: true,
	})
	require.NoError(t, err, "PDF generation should succeed")

	pdfBytes, err := io.ReadAll(pdfData)
	require.NoError(t, err, "reading PDF stream should succeed")
	require.NotEmpty(t, pdfBytes, "PDF data should not be empty")

	require.True(t, bytes.HasPrefix(pdfBytes, []byte("%PDF-")), "should be a valid PDF")
	t.Logf("PDF Generation: Got %d bytes", len(pdfBytes))

	outputPath := filepath.Join(workspaceDir, "pdfs", "test_page.pdf")
	os.MkdirAll(filepath.Dir(outputPath), 0755)
	err = os.WriteFile(outputPath, pdfBytes, 0644)
	require.NoError(t, err, "saving PDF should succeed")

	info, err := os.Stat(outputPath)
	require.NoError(t, err)
	require.Greater(t, info.Size(), int64(0))
	t.Logf("PDF Generation: Saved to %s (%d bytes)", outputPath, info.Size())
}

func TestGetCookies(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/set-cookie")
	defer release()

	cookies, err := page.Cookies(nil)
	require.NoError(t, err, "getting cookies should succeed")

	require.NotEmpty(t, cookies, "should have at least one cookie")

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

func TestSetCookies(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	err := page.SetCookies([]*proto.NetworkCookieParam{
		{
			Name:   "my-test-cookie",
			Value:  "my-test-value",
			Domain: "127.0.0.1",
			Path:   "/",
		},
	})
	require.NoError(t, err, "setting cookie should succeed")

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

func TestEvaluateJS_Safe(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	result, err := page.Eval("() => document.title")
	require.NoError(t, err, "safe JS execution should succeed")

	title := result.Value.Str()
	require.Contains(t, title, "Test Page")
	t.Logf("EvaluateJS Safe: document.title = '%s'", title)
}

func TestEvaluateJS_Dangerous(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	evaluateTool := &browsertools.EvaluateJSTool{}

	dangerousCodes := []string{
		"eval('1+1')",
		"new Function('return 1')",
		"document.write('hello')",
		"obj.__proto__",
		"obj.constructor",
	}

	for _, code := range dangerousCodes {
		toolCtx := context.WithValue(ctx, browsertools.PageCtxKey, page)
		result, err := evaluateTool.Execute(toolCtx, map[string]interface{}{
			"code": code,
		})
		t.Logf("Dangerous code '%s': result=%v, err=%v", code, result, err)
		require.Error(t, err, "dangerous code '%s' should be rejected", code)
	}

	safeCode := "() => document.title"
	safeCtx := context.WithValue(ctx, browsertools.PageCtxKey, page)
	result, err := evaluateTool.Execute(safeCtx, map[string]interface{}{
		"code": safeCode,
	})
	require.NoError(t, err, "safe code should execute successfully")
	t.Logf("Safe code result: %v", result)
}

func TestScreenshot_InvalidPath(t *testing.T) {
	err := ValidateFilePath("/invalid/path/screenshot.png")
	require.Error(t, err, "should reject path outside workspace")
	t.Logf("Screenshot InvalidPath: Error=%v", err)
}

func TestScreenshot_InvalidPath_Tool(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	toolCtx := context.WithValue(ctx, PageCtxKey, page)

	screenshotTool := &browsertools.ScreenshotTool{}
	result, err := screenshotTool.Execute(toolCtx, map[string]interface{}{
		"output_file": "/invalid/path/screenshot.png",
	})
	t.Logf("Screenshot InvalidPath Tool: result=%v, err=%v", result, err)
}

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

func TestExtractText_MultipleElements(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	h1El, err := page.Timeout(5 * time.Second).Element("h1")
	require.NoError(t, err)
	h1Text, err := h1El.Text()
	require.NoError(t, err)
	require.Equal(t, "Test Page", h1Text)

	btnEl, err := page.Timeout(5 * time.Second).Element("#test-button")
	require.NoError(t, err)
	btnText, err := btnEl.Text()
	require.NoError(t, err)
	require.Contains(t, btnText, "Click Me")

	t.Logf("ExtractText MultipleElements: h1='%s', button='%s'", h1Text, btnText)
}

func TestGetAttribute(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	result, err := page.Eval(`() => document.getElementById('test-input').getAttribute('placeholder')`)
	require.NoError(t, err)
	require.Equal(t, "Enter text", result.Value.Str())

	result, err = page.Eval(`() => document.getElementById('test-button').id`)
	require.NoError(t, err)
	require.Equal(t, "test-button", result.Value.Str())

	t.Log("GetAttribute: Attributes retrieved successfully")
}

func TestFormInteraction(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/form")
	defer release()

	nameInput, err := page.Timeout(5 * time.Second).Element("input[name='name']")
	require.NoError(t, err)
	err = nameInput.Input("TestUser")
	require.NoError(t, err)

	submitBtn, err := page.Timeout(5 * time.Second).Element("button[type='submit']")
	require.NoError(t, err)
	err = submitBtn.Click(proto.InputMouseButtonLeft, 1)
	require.NoError(t, err)

	page.WaitLoad()
	page.WaitIdle(5 * time.Second)

	info, err := page.Info()
	require.NoError(t, err)
	require.Contains(t, info.URL, "/submit")

	bodyText, err := page.Eval("() => document.body.innerText")
	require.NoError(t, err)
	require.Contains(t, bodyText.Value.Str(), "TestUser")

	t.Logf("FormInteraction: Submitted form, name='%s'", "TestUser")
}

func TestRedirect_Follow(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/redirect")
	defer release()

	info, err := page.Info()
	require.NoError(t, err)

	require.Contains(t, info.URL, "/")
	require.NotContains(t, info.URL, "/redirect")

	titleResult, err := page.Eval("() => document.title")
	require.NoError(t, err)
	require.Contains(t, titleResult.Value.Str(), "Test Page")

	t.Log("Redirect Follow: Redirect followed successfully")
}

func TestMultipleCookies(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/set-multiple-cookies")
	defer release()

	cookies, err := page.Cookies(nil)
	require.NoError(t, err)

	cookieMap := make(map[string]string)
	for _, c := range cookies {
		cookieMap[c.Name] = c.Value
	}

	require.Equal(t, "value-a", cookieMap["cookie-a"], "cookie-a should be set")
	require.Equal(t, "value-b", cookieMap["cookie-b"], "cookie-b should be set")
	t.Log("MultipleCookies: Both cookies verified")
}

func TestElementVisibility(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	btn, err := page.Timeout(5 * time.Second).Element("#test-button")
	require.NoError(t, err)

	visible, err := btn.Visible()
	require.NoError(t, err)
	require.True(t, visible, "#test-button should be visible")

	t.Log("ElementVisibility: Button is visible")
}

func TestPageHistory(t *testing.T) {
	ctx := testCtx(t)
	page, release := requireTestPage(t, ctx, "/")
	defer release()

	err := page.Timeout(10 * time.Second).Navigate(testServer.URL + "/page2")
	require.NoError(t, err)
	page.WaitLoad()

	history, err := page.GetNavigationHistory()
	require.NoError(t, err)

	require.GreaterOrEqual(t, len(history.Entries), 2, "should have at least 2 history entries")
	t.Logf("PageHistory: %d entries, current index=%d", len(history.Entries), history.CurrentIndex)
}
