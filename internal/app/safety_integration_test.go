package app

import (
	"bytes"
	"encoding/csv"
	"image"
	"image/jpeg"
	"image/png"
	"io/fs"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	donate "github.com/TokenNotIncluded/donate.lmm.best"
)

func TestAdminRoutesDenyAnonymousAndPasswordBootstrapSessions(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	routes := a.Routes()
	for _, target := range []string{"/api/admin/settings", "/api/admin/donations", "/api/admin/export", "/api/admin/notifications", "/api/admin/waffo/stores?method_id=waffo"} {
		rr := requestJSON(t, routes, http.MethodGet, target, nil, nil)
		expectStatus(t, rr, http.StatusUnauthorized)
		if strings.Contains(rr.Body.String(), "secret-never-public") {
			t.Fatal("unauthenticated admin route exposed configuration")
		}
	}
	password, err := a.Auth.Password()
	if err != nil {
		t.Fatal(err)
	}
	rr := requestJSON(t, routes, http.MethodPost, "/api/auth/password", map[string]string{"password": password}, nil)
	expectStatus(t, rr, http.StatusOK)
	status := decodeResponse[map[string]any](t, rr)
	if status["authenticated"] != false || status["needs_passkey"] != true || status["csrf_token"] == "" {
		t.Fatalf("password granted full admin access: %v", status)
	}
	var session *http.Cookie
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name == "donate_session" {
			session = cookie
		}
	}
	if session == nil || !session.HttpOnly || session.SameSite != http.SameSiteStrictMode {
		t.Fatal("password response did not set a private strict session cookie")
	}
	for _, method := range []string{http.MethodGet, http.MethodPut} {
		req := httptest.NewRequest(method, "/api/admin/settings", strings.NewReader("{}"))
		req.AddCookie(session)
		req.Header.Set("Origin", a.PublicURL)
		req.Header.Set("X-CSRF-Token", status["csrf_token"].(string))
		rr = httptest.NewRecorder()
		routes.ServeHTTP(rr, req)
		expectStatus(t, rr, http.StatusForbidden)
	}
	if _, err := a.Auth.Reset(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/settings", nil)
	req.AddCookie(session)
	rr = httptest.NewRecorder()
	routes.ServeHTTP(rr, req)
	expectStatus(t, rr, http.StatusUnauthorized)
}

func TestDonationRejectsForeignOriginMalformedJSONAndInvalidAmounts(t *testing.T) {
	a := testApp(t)
	testSettings(t, a, customMethod())
	in := donationInput{AmountMinor: 100, Currency: "USD", MethodID: "qr", AcceptedTerms: true}
	for _, headers := range []map[string]string{{"Origin": "https://evil.example"}, {"Sec-Fetch-Site": "cross-site"}} {
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers), http.StatusForbidden)
	}
	for _, raw := range []string{`{"amount_minor":100} {}`, `{"amount_minor":100`, `null`, `{"amount_minor":1.25,"accepted_terms":true}`} {
		req := httptest.NewRequest(http.MethodPost, "/api/donations", strings.NewReader(raw))
		rr := httptest.NewRecorder()
		a.Routes().ServeHTTP(rr, req)
		expectStatus(t, rr, http.StatusBadRequest)
	}
	for _, amount := range []int64{0, -1, 100000001} {
		in.AmountMinor = amount
		expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, nil), http.StatusBadRequest)
	}
	in.AmountMinor, in.Currency = 1000001, "JPY"
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, nil), http.StatusBadRequest)
	if countRows(t, a, "donations") != 0 {
		t.Fatal("rejected donation request wrote a record")
	}
}

func TestCSVExportNeutralizesUserControlledSpreadsheetFormulas(t *testing.T) {
	a := testApp(t)
	testSettings(t, a)
	in := donationInput{AmountMinor: 500, Currency: "JPY", MethodName: "@SUM(A1:A2)", Name: "=HYPERLINK(\"https://evil.example\",\"click\")", Email: "safe@example.com", Message: "\t=1+1", Reference: "+cmd"}
	rr := requestJSON(t, http.HandlerFunc(a.manualDonation), http.MethodPost, "/api/admin/donations", in, nil)
	expectStatus(t, rr, http.StatusCreated)
	rr = requestJSON(t, http.HandlerFunc(a.exportDonations), http.MethodGet, "/api/admin/export", nil, nil)
	expectStatus(t, rr, http.StatusOK)
	rows, err := csv.NewReader(strings.NewReader(rr.Body.String())).ReadAll()
	if err != nil || len(rows) != 2 || len(rows[1]) != 13 {
		t.Fatalf("invalid CSV: rows=%v err=%v", rows, err)
	}
	for _, column := range []int{4, 5, 7, 12} {
		if !strings.HasPrefix(rows[1][column], "'") {
			t.Fatalf("formula was not escaped in CSV column %q: %q", rows[0][column], rows[1][column])
		}
	}
	if rows[1][2] != "500" || rows[1][3] != "JPY" || rows[1][6] != in.Email || !strings.Contains(rr.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal("CSV modified safe data or did not download as an attachment")
	}
}

func uploadRequest(t *testing.T, a *App, name string, content []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	multipartBody := multipart.NewWriter(&body)
	part, err := multipartBody.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err = multipartBody.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/upload", &body)
	req.Header.Set("Content-Type", multipartBody.FormDataContentType())
	rr := httptest.NewRecorder()
	a.upload(rr, req)
	return rr
}

func TestUploadsValidateImageContentDimensionsAndSize(t *testing.T) {
	a := testApp(t)
	var validPNG, validJPEG, widePNG bytes.Buffer
	if err := png.Encode(&validPNG, image.NewRGBA(image.Rect(0, 0, 32, 32))); err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(&validJPEG, image.NewRGBA(image.Rect(0, 0, 32, 32)), nil); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&widePNG, image.NewRGBA(image.Rect(0, 0, 8193, 16))); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"html-with-png-name", []byte("<html><script>alert(1)</script></html>")},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)},
		{"malformed-png", []byte("\x89PNG\r\n\x1a\ninvalid image")},
		{"truncated-png", validPNG.Bytes()[:33]},
		{"too-wide", widePNG.Bytes()},
		{"over-two-megabytes", append(append([]byte{}, validPNG.Bytes()...), make([]byte, 2*1024*1024)...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			rr := uploadRequest(t, a, "qr.png", test.raw)
			expectStatus(t, rr, http.StatusBadRequest)
		})
	}
	entries, err := os.ReadDir(filepath.Join(a.DataDir, "uploads"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("rejected images were retained: %v, %v", entries, err)
	}
	for _, imageBytes := range [][]byte{validPNG.Bytes(), validJPEG.Bytes()} {
		rr := uploadRequest(t, a, "../../filename-is-not-trusted.svg", imageBytes)
		expectStatus(t, rr, http.StatusCreated)
		result := decodeResponse[map[string]string](t, rr)
		if !strings.HasPrefix(result["url"], "/uploads/") || strings.Contains(result["url"], "filename") {
			t.Fatalf("upload used the supplied unsafe filename: %v", result)
		}
		served := requestJSON(t, a.Routes(), http.MethodGet, result["url"], nil, nil)
		expectStatus(t, served, http.StatusOK)
		if !bytes.Equal(served.Body.Bytes(), imageBytes) || served.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Fatal("uploaded image did not round-trip safely")
		}
		info, err := os.Stat(filepath.Join(a.DataDir, strings.TrimPrefix(result["url"], "/")))
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("uploaded image permissions were not private: %v %v", info, err)
		}
	}
}

func TestEmbeddedFrontendDoesNotExposeSourceDocumentsOrData(t *testing.T) {
	assets, err := fs.Sub(donate.Assets, "web")
	if err != nil {
		t.Fatal(err)
	}
	a := testApp(t)
	a.assets = assets
	for _, path := range []string{"/docs/CONTRACT.md", "/README.md", "/go.mod", "/internal/app/app.go", "/data/donate.sqlite", "/bootstrap-password", "/admin/settings", "/uploads/bootstrap-password"} {
		rr := requestJSON(t, a.Routes(), http.MethodGet, path, nil, nil)
		expectStatus(t, rr, http.StatusNotFound)
	}
	for _, path := range []string{"/", "/admin", "/admin/", "/app.js", "/style.css"} {
		rr := requestJSON(t, a.Routes(), http.MethodGet, path, nil, nil)
		expectStatus(t, rr, http.StatusOK)
		if rr.Header().Get("Content-Security-Policy") == "" || rr.Header().Get("X-Frame-Options") != "DENY" || rr.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatalf("frontend response omitted browser protections for %s", path)
		}
	}
}
