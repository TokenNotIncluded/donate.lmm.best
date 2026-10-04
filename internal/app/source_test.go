package app

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSourceStarsAreRealCachedAndKeepDatedStaleValue(t *testing.T) {
	a := testApp(t)
	calls := 0
	failNext := false
	a.Chain.Client = &http.Client{Transport: cryptoTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.github.com" || r.URL.Path != "/repos/TokenNotIncluded/donate.lmm.best" || r.Header.Get("Authorization") != "" {
			t.Fatal("unsafe source request", r.URL)
		}
		status, body := 200, `{"full_name":"TokenNotIncluded/donate.lmm.best","stargazers_count":32}`
		if failNext {
			status = 403
			body = `{"message":"rate limited"}`
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	read := func() map[string]any {
		rr := requestJSON(t, a.Routes(), "GET", "/api/source", nil, nil)
		expectStatus(t, rr, 200)
		return decodeResponse[map[string]any](t, rr)
	}
	first := read()
	read()
	if first["stars"] != float64(32) || first["fetched_at"] == "" || calls != 1 {
		t.Fatal(first, calls)
	}
	failNext = true
	a.starCache.Next = time.Now().Add(-time.Second)
	stale := read()
	if stale["stars"] != float64(32) || stale["fetched_at"] != first["fetched_at"] || stale["stale"] != true || calls != 2 {
		t.Fatal(stale, calls)
	}
	a.starCache = starSnapshot{}
	missing := read()
	if missing["stars"] != nil {
		t.Fatal("invented zero stars", missing)
	}
}
