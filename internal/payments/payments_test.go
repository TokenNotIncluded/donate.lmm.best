package payments

import (
	"context"
	"errors"
	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
	"io"
	"net/http"
	"strings"
	"testing"
)

type paymentTestTransport func(*http.Request) (*http.Response, error)

func (f paymentTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func paymentResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func TestDecimalAmounts(t *testing.T) {
	for _, test := range []struct {
		value, currency string
		want            int64
	}{
		{"12.34", "USD", 1234}, {"12.3", "EUR", 1230}, {"0012", "HKD", 1200}, {"4500", "JPY", 4500}, {"4500.00", "JPY", 4500}, {"123", "TWD", 12300},
	} {
		got, err := parseAmount(test.value, test.currency)
		if err != nil || got != test.want {
			t.Errorf("%s %s: got %d, %v", test.value, test.currency, got, err)
		}
	}
	for _, test := range []struct{ value, currency string }{
		{"1.001", "USD"}, {"1.1", "JPY"}, {"-10", "USD"}, {"1e2", "USD"}, {"+1", "USD"}, {" 1", "USD"}, {"1.", "USD"}, {"1..0", "USD"}, {"10000000000.01", "USD"}, {"9223372036854775807", "USD"}, {"2", "BTC"},
	} {
		if _, err := parseAmount(test.value, test.currency); err == nil {
			t.Errorf("accepted %q %s", test.value, test.currency)
		}
	}
}

func TestMethodValidation(t *testing.T) {
	if err := ValidateMethod(Method{ID: "qr", Type: "custom", Name: "QR", Enabled: true, QRURL: "/uploads/abc.png"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateMethod(Method{ID: "draft", Type: "waffo", Name: "Waffo"}); err != nil {
		t.Fatal(err)
	}
	for _, m := range []Method{
		{ID: "qr", Type: "custom", Name: "QR", Enabled: true},
		{ID: "qr", Type: "custom", Name: "QR", Enabled: true, CheckoutURL: "javascript:alert(1)"},
		{ID: "qr", Type: "custom", Name: "QR", Enabled: true, CheckoutURL: "https://user:password@evil.example"},
		{ID: "bad/id", Type: "custom", Name: "QR", QRURL: "/uploads/q.png"},
		{ID: "s", Type: "stripe", Name: "Stripe", Enabled: true},
		{ID: "s", Type: "stripe", Name: "Stripe", Enabled: true, Config: map[string]string{"secret_key": "sk_test_example\n", "webhook_secret": "whsec_example"}},
		{ID: "w", Type: "waffo", Name: "Waffo", Config: map[string]string{"base_url": "http://127.0.0.1"}},
	} {
		if err := ValidateMethod(m); err == nil {
			t.Errorf("accepted invalid method %#v", m)
		}
	}
}

func TestCheckoutRejectsInvalidMoneyBeforeNetwork(t *testing.T) {
	s := &Service{Client: &http.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected provider request"); return nil, nil })}}
	m := Method{ID: "s", Type: "stripe", Name: "Stripe", Enabled: true, Config: map[string]string{"secret_key": "sk_test_example", "webhook_secret": "whsec_example"}}
	for _, request := range []CheckoutRequest{
		{ID: "d1", AmountMinor: 0, Currency: "USD", ReturnURL: "https://example.com", CancelURL: "https://example.com"},
		{ID: "../d1", AmountMinor: 100, Currency: "USD", ReturnURL: "https://example.com", CancelURL: "https://example.com"},
		{ID: "d1", AmountMinor: 100, Currency: "BTC", ReturnURL: "https://example.com", CancelURL: "https://example.com"},
		{ID: "d1", AmountMinor: 100, Currency: "USD", ReturnURL: "http://example.com", CancelURL: "https://example.com"},
	} {
		if _, err := s.Checkout(context.Background(), m, request); err == nil {
			t.Error("invalid donation accepted")
		}
	}
}

func TestProviderRequestsDoNotFollowRedirects(t *testing.T) {
	calls := 0
	s := &Service{Client: &http.Client{Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		res := paymentResponse(http.StatusFound, "")
		res.Header.Set("Location", "https://evil.example/collect")
		res.Request = r
		return res, nil
	})}}
	h := make(http.Header)
	h.Set("Authorization", "Bearer SECRET")
	if err := s.requestJSON(context.Background(), http.MethodGet, "https://api.stripe.com/v1/checkout/sessions", h, nil, &struct{}{}); err == nil {
		t.Fatal("redirect accepted")
	}
	if calls != 1 {
		t.Fatalf("credentials followed redirect: %d requests", calls)
	}
}

func TestSafeErrorDoesNotExposeProviderSecrets(t *testing.T) {
	secret := "-----BEGIN PRIVATE KEY----- private-token"
	provider := &pancake.Error{Status: 400, Errors: []pancake.Notice{{Message: secret}}}
	for _, err := range []error{provider, errors.New(secret)} {
		if message := SafeError(err); strings.Contains(message, "PRIVATE KEY") || strings.Contains(message, "private-token") || len(message) > 240 {
			t.Fatal("unsafe public payment error:", message)
		}
	}
	if SafeError(errors.New("Waffo USD amount must be between $1 and $10000")) != "Waffo USD amount must be between $1 and $10000" {
		t.Fatal("validation guidance was lost")
	}
}
