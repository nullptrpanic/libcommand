package main

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestHandlerServesIndex(t *testing.T) {
	response := serveTestRequest(newHandler(testAssets()), "/")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if body := response.Body.String(); body != "<!doctype html><title>playground</title>" {
		t.Fatalf("body = %q", body)
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", contentType)
	}
}

func TestHandlerSetsBrowserSecurityHeaders(t *testing.T) {
	response := serveTestRequest(newHandler(testAssets()), "/")

	for name, want := range map[string]string{
		"Content-Security-Policy": "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'",
		"Referrer-Policy":         "no-referrer",
		"X-Content-Type-Options":  "nosniff",
	} {
		if got := response.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func TestHandlerAllowsJavaScriptCompilationOnlyInSimulationWorker(t *testing.T) {
	page := serveTestRequest(newHandler(testAssets()), "/")
	worker := serveTestRequest(newHandler(testAssets()), "/worker.js")

	if got := page.Header().Get("Content-Security-Policy"); strings.Contains(got, " 'unsafe-eval'") {
		t.Fatalf("page Content-Security-Policy = %q, want no unsafe-eval", got)
	}
	want := "default-src 'self'; script-src 'self' 'wasm-unsafe-eval' 'unsafe-eval'; worker-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'"
	if got := worker.Header().Get("Content-Security-Policy"); got != want {
		t.Fatalf("worker Content-Security-Policy = %q, want %q", got, want)
	}
}

func TestHandlerServesWebAssemblyMediaType(t *testing.T) {
	response := serveTestRequest(newHandler(testAssets()), "/libcommand.wasm")

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/wasm" {
		t.Fatalf("Content-Type = %q, want application/wasm", contentType)
	}
	if body := response.Body.String(); body != "wasm" {
		t.Fatalf("body = %q, want %q", body, "wasm")
	}
}

func TestHandlerReturnsNotFoundForMissingAsset(t *testing.T) {
	response := serveTestRequest(newHandler(testAssets()), "/missing.js")

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
}

func TestAssetsStubExplainsRequiredBuildTarget(t *testing.T) {
	assets, err := playgroundAssets()
	if assets != nil || err == nil || !strings.Contains(err.Error(), "make playground") {
		t.Fatalf("playgroundAssets() = %#v, %v", assets, err)
	}
}

func testAssets() fs.FS {
	return fstest.MapFS{
		"index.html":      {Data: []byte("<!doctype html><title>playground</title>")},
		"libcommand.wasm": {Data: []byte("wasm")},
		"worker.js":       {Data: []byte("self.onmessage = () => {}")},
	}
}

func serveTestRequest(handler http.Handler, path string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
