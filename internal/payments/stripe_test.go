package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func stripeTestMethod() Method {
	return Method{ID: "stripe-main", Type: "stripe", Name: "Stripe", Enabled: true, Config: map[string]string{"secret_key": "sk_test_example", "webhook_secret": "whsec_test_example"}}
}
func stripeTestSignature(body string, secret string, timestamp int64) string {
	t := strconv.FormatInt(timestamp, 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(t + "." + body))
	return "t=" + t + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestStripeCreatesBoundHostedCheckout(t *testing.T) {
	// The donation was created five minutes ago; retry must keep its original deadline.
	expiry := time.Now().Add(40 * time.Minute)
	firstBody := ""
	attempts := 0
	s := &Service{Client: &http.Client{Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.stripe.com/v1/checkout/sessions" || r.Method != "POST" {
			t.Fatal(r.URL, r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer sk_test_example" || r.Header.Get("Idempotency-Key") != "donation-d_123" {
			t.Fatal("missing request authority/idempotency")
		}
		body, _ := io.ReadAll(r.Body)
		attempts++
		if attempts == 1 {
			firstBody = string(body)
		} else if string(body) != firstBody {
			t.Fatal("Stripe retry changed the form under the same idempotency key")
		}
		form, err := url.ParseQuery(string(body))
		if err != nil {
			t.Fatal(err)
		}
		expiresAt, err := strconv.ParseInt(form.Get("expires_at"), 10, 64)
		if err != nil || expiresAt != expiry.Unix() {
			t.Errorf("checkout expiry changed from the persisted deadline: %q", form.Get("expires_at"))
		}
		for key, want := range map[string]string{"client_reference_id": "d_123", "metadata[donation_id]": "d_123", "metadata[method_id]": "stripe-main", "payment_intent_data[metadata][donation_id]": "d_123", "line_items[0][price_data][unit_amount]": "1234", "line_items[0][price_data][currency]": "usd", "mode": "payment"} {
			if form.Get(key) != want {
				t.Errorf("%s: %s", key, form.Get(key))
			}
		}
		return paymentResponse(200, `{"id":"cs_test_123","url":"https://checkout.stripe.com/c/pay/cs_test_123"}`), nil
	})}}
	in := CheckoutRequest{ID: "d_123", AmountMinor: 1234, Currency: "USD", ReturnURL: "https://example.com/?donation=d_123", CancelURL: "https://example.com/", ExpiresAt: expiry.Format(time.RFC3339Nano)}
	for range 2 {
		result, err := s.Checkout(context.Background(), stripeTestMethod(), in)
		if err != nil || result.Reference != "cs_test_123" {
			t.Fatal(result, err)
		}
	}
}

func TestStripeRejectsMissingOrInvalidPersistedExpiry(t *testing.T) {
	s := &Service{Client: &http.Client{Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("invalid expiry reached the payment provider")
		return nil, nil
	})}}
	for _, expiry := range []string{"", "not-a-date", "1960-01-01T00:00:00Z"} {
		_, err := s.Checkout(context.Background(), stripeTestMethod(), CheckoutRequest{ID: "d_123", AmountMinor: 100, Currency: "USD", ReturnURL: "https://example.com/", CancelURL: "https://example.com/", ExpiresAt: expiry})
		if err == nil {
			t.Fatalf("invalid persisted expiry accepted: %q", expiry)
		}
	}
}

func TestStripeRawSignatureAndPaymentEvidence(t *testing.T) {
	m := stripeTestMethod()
	s := New()
	body := `{"id":"evt_123","type":"checkout.session.completed","livemode":false,"data":{"object":{"id":"cs_test_123","client_reference_id":"d_123","payment_status":"paid","mode":"payment","currency":"usd","amount_total":1234,"metadata":{"donation_id":"d_123","method_id":"stripe-main"}}}}`
	signature := stripeTestSignature(body, m.Config["webhook_secret"], time.Now().Unix())
	h := make(http.Header)
	h.Set("Stripe-Signature", signature)
	result, err := s.VerifyWebhook(context.Background(), m, h, []byte(body))
	if err != nil || !result.Paid || result.AmountMinor != 1234 || result.Currency != "USD" || result.Reference != "cs_test_123" || result.DonationID != "d_123" {
		t.Fatal(result, err)
	}
	if _, err := s.VerifyWebhook(context.Background(), m, h, []byte(body+" ")); err == nil {
		t.Fatal("mutated raw body signature accepted")
	}
	h.Set("Stripe-Signature", stripeTestSignature(body, m.Config["webhook_secret"], time.Now().Unix()-301))
	if _, err := s.VerifyWebhook(context.Background(), m, h, []byte(body)); err == nil {
		t.Fatal("old signature accepted")
	}
	h.Set("Stripe-Signature", stripeTestSignature(body, m.Config["webhook_secret"], time.Now().Unix()+301))
	if _, err := s.VerifyWebhook(context.Background(), m, h, []byte(body)); err == nil {
		t.Fatal("future signature accepted")
	}
	for _, bad := range []string{strings.Replace(body, `"donation_id":"d_123"`, `"donation_id":"other"`, 1), strings.Replace(body, `"method_id":"stripe-main"`, `"method_id":"other"`, 1), strings.Replace(body, `"amount_total":1234`, `"amount_total":0`, 1), strings.Replace(body, `"livemode":false`, `"livemode":true`, 1)} {
		h.Set("Stripe-Signature", stripeTestSignature(bad, m.Config["webhook_secret"], time.Now().Unix()))
		if _, err := s.VerifyWebhook(context.Background(), m, h, []byte(bad)); err == nil {
			t.Errorf("invalid signed evidence accepted: %s", bad)
		}
	}
}

func TestStripeAsyncPaymentIsNotPaidBeforeSettlement(t *testing.T) {
	m := stripeTestMethod()
	body := `{"id":"evt_pending","type":"checkout.session.completed","livemode":false,"data":{"object":{"id":"cs_pending","payment_status":"unpaid","mode":"payment"}}}`
	h := make(http.Header)
	h.Set("Stripe-Signature", stripeTestSignature(body, m.Config["webhook_secret"], time.Now().Unix()))
	result, err := New().VerifyWebhook(context.Background(), m, h, []byte(body))
	if err != nil || result.Paid {
		t.Fatal(result, err)
	}
}

func TestStripeRefundResolvesOriginalSession(t *testing.T) {
	m := stripeTestMethod()
	s := &Service{Client: &http.Client{Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.stripe.com" || r.URL.Query().Get("payment_intent") != "pi_original" {
			t.Fatal(r.URL)
		}
		return paymentResponse(200, `{"data":[{"id":"cs_original","client_reference_id":"d_123","payment_status":"paid","mode":"payment","currency":"usd","amount_total":1234,"metadata":{"donation_id":"d_123","method_id":"stripe-main"}}],"has_more":false}`), nil
	})}}
	body := `{"id":"evt_refund","type":"charge.refunded","livemode":false,"data":{"object":{"id":"ch_original","amount":1234,"amount_refunded":1234,"currency":"usd","refunded":true,"payment_intent":"pi_original"}}}`
	h := make(http.Header)
	h.Set("Stripe-Signature", stripeTestSignature(body, m.Config["webhook_secret"], time.Now().Unix()))
	result, err := s.VerifyWebhook(context.Background(), m, h, []byte(body))
	if err != nil || !result.Refunded || result.Paid || result.Reference != "cs_original" || result.AmountMinor != 1234 {
		t.Fatal(result, err)
	}
	partial := strings.Replace(body, `"amount_refunded":1234`, `"amount_refunded":500`, 1)
	h.Set("Stripe-Signature", stripeTestSignature(partial, m.Config["webhook_secret"], time.Now().Unix()))
	result, err = s.VerifyWebhook(context.Background(), m, h, []byte(partial))
	if err != nil || result.Refunded {
		t.Fatal(result, err)
	}
}

func TestStripeSignatureSupportsRotation(t *testing.T) {
	body := "{\"example\":true}"
	valid := stripeTestSignature(body, "secret", time.Now().Unix())
	if err := verifyStripeSignature(fmt.Sprintf("%s,v1=%s", valid, strings.Repeat("0", 64)), []byte(body), "secret"); err != nil {
		t.Fatal(err)
	}
	if err := verifyStripeSignature(valid+",t="+strconv.FormatInt(time.Now().Unix(), 10), []byte(body), "secret"); err == nil {
		t.Fatal("ambiguous timestamp accepted")
	}
}
