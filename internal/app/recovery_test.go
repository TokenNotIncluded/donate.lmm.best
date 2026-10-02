package app

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

func TestProviderPreflightDoesNotPersistUnusableDonations(t *testing.T) {
	for _, kind := range []string{"waffo-TWD", "waffo-too-small", "paypal-fractional-TWD"} {
		t.Run(kind, func(t *testing.T) {
			a := testApp(t)
			a.PublicURL = "https://donate.example.com"
			m := appWaffoMethod(t)
			m.Enabled = true
			m.Config["product_id"] = appProductID
			m.Config["tax_category"] = "digital_goods"
			currency, amount := "TWD", int64(1500)
			if kind == "waffo-too-small" {
				currency, amount = "USD", 50
			}
			if kind == "paypal-fractional-TWD" {
				m = payments.Method{ID: "paypal", Type: "paypal", Name: "PayPal", Enabled: true, Config: map[string]string{"client_id": "client", "client_secret": "secret", "webhook_id": "webhook", "environment": "sandbox"}}
				amount = 1501
			}
			testSettings(t, a, m)
			a.Payments.Client = &http.Client{Transport: mockTransport(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid checkout reached provider")
				return nil, nil
			})}
			response := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", donationInput{AmountMinor: amount, Currency: currency, MethodID: m.ID, AcceptedTerms: true}, map[string]string{"Origin": a.PublicURL})
			expectStatus(t, response, http.StatusBadRequest)
			if countRows(t, a, "donations") != 0 {
				t.Fatal("invalid provider amount/currency persisted and could lock admin configuration")
			}
		})
	}
}

func TestUncertainCheckoutDoesNotRestartAfterProviderKeyRetention(t *testing.T) {
	a := testApp(t)
	m := stripeMethod("card")
	testSettings(t, a, m)
	calls := 0
	a.Payments.Client = &http.Client{Transport: mockTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return providerResponse(503, map[string]string{"error": "outage"}), nil
	})}
	in := donationInput{AmountMinor: 1500, Currency: "USD", MethodID: m.ID, AcceptedTerms: true}
	headers := map[string]string{"Idempotency-Key": "uncertain-checkout-retry-0001"}
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers), http.StatusBadGateway)
	if _, err := a.DB.Exec("UPDATE donations SET created_at=?", time.Now().Add(-6*time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers), http.StatusConflict)
	if calls != 1 || countRows(t, a, "donations") != 1 {
		t.Fatal("expired provider retry created a second operation")
	}
}

func TestTrustedProxyRejectsSpoofedClientChains(t *testing.T) {
	a := testApp(t)
	tests := []struct{ peer, chain, want string }{
		{"198.51.100.4:8000", "203.0.113.50", "198.51.100.4"},
		{"127.0.0.1:8000", "203.0.113.50, 198.51.100.4", "198.51.100.4"},
		{"127.0.0.1:8000", "203.0.113.50, 127.0.0.1", "203.0.113.50"},
		{"127.0.0.1:8000", "invalid-header", "127.0.0.1"},
	}
	for _, test := range tests {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = test.peer
		r.Header.Set("X-Forwarded-For", test.chain)
		if got := a.clientIP(r); got != test.want {
			t.Fatalf("peer %q chain %q => %q, want %q", test.peer, test.chain, got, test.want)
		}
	}
}
