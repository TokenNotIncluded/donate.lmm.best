package app

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticHTMLVersionsResourceContents(t *testing.T) {
	assets := fstest.MapFS{
		"index.html":  {Data: []byte(`<link href="/favicon.svg"><link id="icon-sprite" href="/icons.svg"><link href='/style.css'><script src="/app.js"></script><link href="/coffee.css"><script src="/coffee.js"></script><a href="/">home</a>`)},
		"admin.html":  {Data: []byte(`<link href='/favicon.svg'><link id="icon-sprite" href="/icons.svg"><link href="/admin.css"><script src='/admin.js'></script><link href='/coffee.css'><script src='/coffee.js'></script><a href="/api/admin/export">export</a>`)},
		"favicon.svg": {Data: []byte(`<svg></svg>`)},
		"icons.svg":   {Data: []byte(`<svg><symbol id="dice"></symbol></svg>`)},
		"style.css":   {Data: []byte(`body { color: white; }`)},
		"coffee.css":  {Data: []byte(`.coffee-logo { white-space: pre; }`)},
		"coffee.js":   {Data: []byte(`console.log("coffee");`)},
		"app.js":      {Data: []byte(`console.log("public");`)},
		"admin.css":   {Data: []byte(`nav { display: flex; }`)},
		"admin.js":    {Data: []byte(`console.log("admin");`)},
	}
	a := &App{assets: assets}
	get := func(t *testing.T, target string) *httptest.ResponseRecorder {
		t.Helper()
		rr := httptest.NewRecorder()
		a.serveAssets(rr, httptest.NewRequest(http.MethodGet, target, nil))
		expectStatus(t, rr, http.StatusOK)
		return rr
	}
	versionedURL := func(path string) string {
		return "/" + path + fmt.Sprintf("?v=%x", sha256.Sum256(assets[path].Data))
	}
	for _, page := range []struct {
		target    string
		resources []string
		unchanged string
	}{
		{"/", []string{"favicon.svg", "icons.svg", "style.css", "app.js", "coffee.css", "coffee.js"}, `<a href="/">home</a>`},
		{"/index.html", []string{"favicon.svg", "icons.svg", "style.css", "app.js", "coffee.css", "coffee.js"}, `<a href="/">home</a>`},
		{"/admin", []string{"favicon.svg", "icons.svg", "admin.css", "admin.js", "coffee.css", "coffee.js"}, `<a href="/api/admin/export">export</a>`},
		{"/admin/", []string{"favicon.svg", "icons.svg", "admin.css", "admin.js", "coffee.css", "coffee.js"}, `<a href="/api/admin/export">export</a>`},
		{"/admin.html", []string{"favicon.svg", "icons.svg", "admin.css", "admin.js", "coffee.css", "coffee.js"}, `<a href="/api/admin/export">export</a>`},
	} {
		t.Run(page.target, func(t *testing.T) {
			rr := get(t, page.target)
			if got := rr.Header().Get("Cache-Control"); got != "no-cache" {
				t.Fatalf("HTML Cache-Control = %q, want no-cache", got)
			}
			body := rr.Body.String()
			for _, path := range page.resources {
				want := versionedURL(path)
				if !strings.Contains(body, `"`+want+`"`) && !strings.Contains(body, `'`+want+`'`) {
					t.Errorf("HTML does not reference resource content hash %q: %s", want, body)
				}
			}
			if !strings.Contains(body, page.unchanged) {
				t.Errorf("unrelated navigation changed: %s", body)
			}
		})
	}
	for _, path := range []string{"favicon.svg", "icons.svg", "style.css", "app.js", "admin.css", "admin.js", "coffee.css", "coffee.js"} {
		t.Run(path, func(t *testing.T) {
			rr := get(t, versionedURL(path))
			if got := rr.Body.String(); got != string(assets[path].Data) {
				t.Fatalf("versioned resource body = %q, want %q", got, assets[path].Data)
			}
			if got := rr.Header().Get("Cache-Control"); got != "public, max-age=3600" {
				t.Fatalf("resource Cache-Control = %q, want public, max-age=3600", got)
			}
		})
	}

	oldScriptURL, oldStyleURL := versionedURL("app.js"), versionedURL("style.css")
	assets["app.js"].Data = []byte(`console.log("new public release");`)
	body := get(t, "/").Body.String()
	if strings.Contains(body, oldScriptURL) || !strings.Contains(body, versionedURL("app.js")) {
		t.Fatalf("changed script still uses old version: %s", body)
	}
	if !strings.Contains(body, oldStyleURL) {
		t.Fatalf("unchanged stylesheet version changed: %s", body)
	}
}

func TestImportedModulesRevalidateAcrossUpdates(t *testing.T) {
	assets := fstest.MapFS{"crypto.js": {Data: []byte("export const payment = 'old';")}}
	a := &App{assets: assets}
	request := func(etag string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/crypto.js", nil)
		if etag != "" {
			r.Header.Set("If-None-Match", etag)
		}
		a.serveAssets(rr, r)
		return rr
	}
	first := request("")
	expectStatus(t, first, http.StatusOK)
	etag := first.Header().Get("ETag")
	if etag == "" || first.Header().Get("Cache-Control") != "no-cache" {
		t.Fatal("unversioned module must revalidate with a content ETag")
	}
	cached := request(etag)
	expectStatus(t, cached, http.StatusNotModified)
	if cached.Body.Len() != 0 {
		t.Fatal("unchanged module should reuse its cached body")
	}
	assets["crypto.js"].Data = []byte("export const payment = 'new';")
	updated := request(etag)
	expectStatus(t, updated, http.StatusOK)
	if updated.Header().Get("ETag") == etag || !strings.Contains(updated.Body.String(), "'new'") {
		t.Fatal("updated module must invalidate the previous payment logic")
	}
}
