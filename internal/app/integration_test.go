package app

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/notify"
	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

func testApp(t *testing.T) *App {
	t.Helper()
	a, err := New(t.TempDir(), "http://localhost:8080", fstest.MapFS{
		"index.html": {Data: []byte("<html>donation page</html>")},
		"admin.html": {Data: []byte("<html>admin login</html>")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	return a
}

func testSettings(t *testing.T, a *App, methods ...payments.Method) Settings {
	t.Helper()
	s, err := a.settings()
	if err != nil {
		t.Fatal(err)
	}
	s.Methods = methods
	// Queue notifications without starting a worker or making network calls.
	s.Webhook = notify.WebhookConfig{URL: "https://example.com/donation-events", Secret: "notification-secret-never-public", Enabled: true}
	s.SMTP.Password = "smtp-password-never-public"
	if err = a.saveSettings(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func customMethod() payments.Method {
	return payments.Method{ID: "qr", Type: "custom", Name: "Scan to donate", Enabled: true, QRURL: "/uploads/example.png", Description: "Wait for the maintainer to verify payment"}
}

func requestJSON(t *testing.T, handler http.Handler, method, target string, value any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if value != nil {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, target, body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://localhost:8080")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func expectStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("HTTP status = %d, want %d; body = %s", rr.Code, want, rr.Body.String())
	}
}

func decodeResponse[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(rr.Body.Bytes(), &value); err != nil {
		t.Fatalf("invalid JSON response %q: %v", rr.Body.String(), err)
	}
	return value
}

func countRows(t *testing.T, a *App, table string) int {
	t.Helper()
	var count int
	if err := a.DB.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

type checkoutResponse struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	StatusToken string `json:"status_token"`
	CheckoutURL string `json:"checkout_url"`
}

func createCustom(t *testing.T, a *App, in donationInput, key string) checkoutResponse {
	t.Helper()
	in.MethodID = "qr"
	in.AcceptedTerms = true
	rr := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, map[string]string{"Idempotency-Key": key})
	expectStatus(t, rr, http.StatusOK)
	result := decodeResponse[checkoutResponse](t, rr)
	if result.ID == "" || result.StatusToken == "" || result.Status != "pending" {
		t.Fatalf("invalid checkout response: %#v", result)
	}
	return result
}

// Admin ledger handlers are exercised directly here; the route authentication
// boundary is tested separately, and auth's own tests verify signed WebAuthn.
func confirmCustom(t *testing.T, a *App, id string, value any) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/admin/donations/{id}/confirm", a.confirmDonation)
	return requestJSON(t, mux, http.MethodPost, "/api/admin/donations/"+id+"/confirm", value, nil)
}

type mockTransport func(*http.Request) (*http.Response, error)

func (f mockTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func providerResponse(status int, value any) *http.Response {
	raw, _ := json.Marshal(value)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw))}
}
