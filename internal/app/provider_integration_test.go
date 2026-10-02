package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TokenNotIncluded/donate.lmm.best/internal/payments"
)

func stripeMethod(id string) payments.Method {
	return payments.Method{ID: id, Type: "stripe", Name: "Card", Enabled: true, Config: map[string]string{"secret_key": "sk_test_integration", "webhook_secret": "whsec_integration_secret"}}
}

func stripeEvent(id, kind string, object any) map[string]any {
	return map[string]any{"id": id, "type": kind, "livemode": false, "data": map[string]any{"object": object}}
}

func stripeWebhook(t *testing.T, a *App, methodID string, event any, secret string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/stripe?method_id="+url.QueryEscape(methodID), strings.NewReader(string(body)))
	req.Header.Set("Stripe-Signature", "t="+timestamp+",v1="+hex.EncodeToString(mac.Sum(nil)))
	rr := httptest.NewRecorder()
	a.Routes().ServeHTTP(rr, req)
	return rr
}

func stripePaidObject(d Donation) map[string]any {
	return map[string]any{"id": d.ProviderRef, "client_reference_id": d.ID, "mode": "payment", "payment_status": "paid", "currency": strings.ToLower(d.Currency), "amount_total": d.AmountMinor, "metadata": map[string]string{"donation_id": d.ID, "method_id": d.MethodID}}
}

func TestStripeCheckoutWebhookLedgerRejectsMismatchesAndSettlesAndRefundsOnce(t *testing.T) {
	a := testApp(t)
	primary, other := stripeMethod("stripe-main"), stripeMethod("stripe-other")
	testSettings(t, a, primary, other)
	checkoutCalls, refundLookups := 0, 0
	var original Donation
	a.Payments.Client = &http.Client{Transport: mockTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "api.stripe.com" || req.Header.Get("Authorization") != "Bearer sk_test_integration" {
			t.Fatalf("request did not use the configured Stripe endpoint and credentials: %s", req.URL)
		}
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/v1/checkout/sessions":
			checkoutCalls++
			raw, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			form, err := url.ParseQuery(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			if form.Get("line_items[0][price_data][unit_amount]") != "1234" || form.Get("line_items[0][price_data][currency]") != "usd" || form.Get("metadata[method_id]") != primary.ID || form.Get("client_reference_id") == "" || req.Header.Get("Idempotency-Key") != "donation-"+form.Get("client_reference_id") {
				t.Fatalf("checkout changed the amount/currency/identity: %s", raw)
			}
			if !strings.Contains(form.Get("success_url"), "status_token=") || strings.Contains(form.Get("success_url"), "email=") {
				t.Fatalf("checkout return does not use a private capability: %s", form.Get("success_url"))
			}
			return providerResponse(200, map[string]any{"id": "cs_test_integration", "url": "https://checkout.stripe.com/c/pay/integration"}), nil
		case req.Method == http.MethodGet && req.URL.Path == "/v1/checkout/sessions" && req.URL.Query().Get("payment_intent") == "pi_test_integration":
			refundLookups++
			return providerResponse(200, map[string]any{"data": []any{stripePaidObject(original)}, "has_more": false}), nil
		default:
			t.Fatalf("unexpected Stripe request: %s %s", req.Method, req.URL)
			return nil, nil
		}
	})}
	in := donationInput{AmountMinor: 1234, Currency: "USD", MethodID: primary.ID, Email: "card-private@example.com", AcceptedTerms: true}
	key := "stripe-checkout-retry-0001"
	headers := map[string]string{"Idempotency-Key": key}
	first := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers)
	expectStatus(t, first, http.StatusOK)
	checkout := decodeResponse[checkoutResponse](t, first)
	second := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers)
	expectStatus(t, second, http.StatusOK)
	if checkoutCalls != 1 || decodeResponse[checkoutResponse](t, second).ID != checkout.ID {
		t.Fatal("checkout retry recreated a provider session")
	}
	var err error
	original, err = a.donation(checkout.ID)
	if err != nil || original.Status != "pending" || original.ProviderRef != "cs_test_integration" || countRows(t, a, "notification_jobs") != 0 {
		t.Fatalf("checkout was treated as a donation before payment: %#v, %v", original, err)
	}

	for _, test := range []struct {
		name     string
		methodID string
		secret   string
		mutate   func(map[string]any)
	}{
		{name: "signature", methodID: primary.ID, secret: "wrong-secret"},
		{name: "amount", methodID: primary.ID, secret: primary.Config["webhook_secret"], mutate: func(o map[string]any) { o["amount_total"] = 1235 }},
		{name: "currency", methodID: primary.ID, secret: primary.Config["webhook_secret"], mutate: func(o map[string]any) { o["currency"] = "eur" }},
		{name: "provider reference", methodID: primary.ID, secret: primary.Config["webhook_secret"], mutate: func(o map[string]any) { o["id"] = "cs_test_different" }},
		{name: "method ownership", methodID: other.ID, secret: other.Config["webhook_secret"], mutate: func(o map[string]any) {
			o["metadata"] = map[string]string{"donation_id": original.ID, "method_id": other.ID}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			object := stripePaidObject(original)
			if test.mutate != nil {
				test.mutate(object)
			}
			rr := stripeWebhook(t, a, test.methodID, stripeEvent("evt_mismatch", "checkout.session.completed", object), test.secret)
			expectStatus(t, rr, http.StatusBadRequest)
			d, err := a.donation(original.ID)
			if err != nil || d.Status != "pending" || countRows(t, a, "provider_events") != 0 || countRows(t, a, "notification_jobs") != 0 {
				t.Fatalf("mismatched callback mutated ledger/outbox: %#v, %v", d, err)
			}
		})
	}

	event := stripeEvent("evt_confirm_integration", "checkout.session.completed", stripePaidObject(original))
	for i := 0; i < 2; i++ {
		expectStatus(t, stripeWebhook(t, a, primary.ID, event, primary.Config["webhook_secret"]), http.StatusOK)
	}
	// Providers can emit another valid success event for the same payment.
	expectStatus(t, stripeWebhook(t, a, primary.ID, stripeEvent("evt_async_confirm", "checkout.session.async_payment_succeeded", stripePaidObject(original)), primary.Config["webhook_secret"]), http.StatusOK)
	d, err := a.donation(original.ID)
	if err != nil || d.Status != "confirmed" || d.PaidAt == "" || countRows(t, a, "provider_events") != 2 || countRows(t, a, "notification_jobs") != 1 {
		t.Fatalf("provider success did not settle exactly once: %#v, %v", d, err)
	}
	// Manual confirmation must never approve a payment-provider order.
	expectStatus(t, confirmCustom(t, a, original.ID, map[string]string{}), http.StatusConflict)

	refund := stripeEvent("evt_refund_integration", "charge.refunded", map[string]any{"id": "ch_test_integration", "amount": 1234, "amount_refunded": 1234, "currency": "usd", "refunded": true, "payment_intent": "pi_test_integration"})
	for i := 0; i < 2; i++ {
		expectStatus(t, stripeWebhook(t, a, primary.ID, refund, primary.Config["webhook_secret"]), http.StatusOK)
	}
	d, err = a.donation(original.ID)
	if err != nil || d.Status != "refunded" || countRows(t, a, "provider_events") != 3 || countRows(t, a, "notification_jobs") != 2 || refundLookups != 2 {
		t.Fatalf("refund did not update ledger/outbox exactly once: %#v, %v", d, err)
	}
	stats, err := a.stats("USD")
	if err != nil || stats.Count != 0 || stats.TotalMinor != 0 {
		t.Fatalf("refunded payment remained in donation totals: %#v, %v", stats, err)
	}
	expectStatus(t, stripeWebhook(t, a, primary.ID, stripeEvent("evt_late_success", "checkout.session.completed", stripePaidObject(original)), primary.Config["webhook_secret"]), http.StatusBadRequest)
	if countRows(t, a, "notification_jobs") != 2 {
		t.Fatal("late success revived a refunded payment")
	}
}

func TestFailedProviderCheckoutRetryKeepsOneDonationAndStableProviderKey(t *testing.T) {
	a := testApp(t)
	m := stripeMethod("stripe-main")
	testSettings(t, a, m)
	var providerKeys []string
	a.Payments.Client = &http.Client{Transport: mockTransport(func(req *http.Request) (*http.Response, error) {
		providerKeys = append(providerKeys, req.Header.Get("Idempotency-Key"))
		if len(providerKeys) == 1 {
			return providerResponse(503, map[string]string{"error": "temporary outage"}), nil
		}
		return providerResponse(200, map[string]string{"id": "cs_retry_integration", "url": "https://checkout.stripe.com/c/pay/retry"}), nil
	})}
	in := donationInput{AmountMinor: 1500, Currency: "USD", MethodID: m.ID, AcceptedTerms: true}
	headers := map[string]string{"Idempotency-Key": "recover-checkout-request-0001"}
	first := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers)
	expectStatus(t, first, http.StatusBadGateway)
	failed := decodeResponse[checkoutResponse](t, first)
	second := requestJSON(t, a.Routes(), http.MethodPost, "/api/donations", in, headers)
	expectStatus(t, second, http.StatusOK)
	recovered := decodeResponse[checkoutResponse](t, second)
	if failed.ID == "" || failed.StatusToken == "" || recovered.ID != failed.ID || recovered.StatusToken != failed.StatusToken || len(providerKeys) != 2 || providerKeys[0] != providerKeys[1] || countRows(t, a, "donations") != 1 || countRows(t, a, "notification_jobs") != 0 {
		t.Fatalf("uncertain checkout retry duplicated identity or payment: failed=%#v recovered=%#v keys=%v", failed, recovered, providerKeys)
	}
}

func TestSettingsKeepHistoricalPaymentAccountsAvailableForCallbacks(t *testing.T) {
	waffo := appWaffoMethod(t)
	waffo.Config["product_id"] = appProductID
	waffo.Config["tax_category"] = "digital_goods"
	paypal := payments.Method{ID: "paypal", Type: "paypal", Name: "PayPal", Config: map[string]string{"client_id": "client-original", "client_secret": "client-secret-original", "environment": "sandbox", "webhook_id": "webhook-original"}}
	for _, test := range []struct {
		method payments.Method
		fields []string
	}{
		{stripeMethod("stripe-main"), []string{"secret_key"}},
		{waffo, []string{"merchant_id", "environment", "store_id", "private_key"}},
		{paypal, []string{"client_id", "client_secret", "environment"}},
	} {
		t.Run(test.method.Type, func(t *testing.T) {
			a := testApp(t)
			s := testSettings(t, a, test.method)
			s.Webhook.Enabled = false
			if err := a.saveSettings(s); err != nil {
				t.Fatal(err)
			}
			d := Donation{ID: "historical-payment", StatusToken: "historical-private-status", AmountMinor: 1234, Currency: "USD", MethodID: test.method.ID, MethodType: test.method.Type, MethodName: test.method.Name, Status: "confirmed", Source: "checkout", ProviderRef: "original-provider-order", CreatedAt: time.Now().UTC().Format(time.RFC3339), PaidAt: time.Now().UTC().Format(time.RFC3339)}
			tx, err := a.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = insertDonation(tx, d); err != nil {
				_ = tx.Rollback()
				t.Fatal(err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			for _, field := range test.fields {
				changed, err := a.settings()
				if err != nil {
					t.Fatal(err)
				}
				// A disabled method still receives settlement/refund callbacks.
				changed.Methods[0].Enabled = false
				changed.Methods[0].Config[field] = "different-account-value"
				rr := requestJSON(t, http.HandlerFunc(a.putSettings), http.MethodPut, "/api/admin/settings", changed, nil)
				expectStatus(t, rr, http.StatusConflict)
			}
			removed, _ := a.settings()
			removed.Methods = nil
			expectStatus(t, requestJSON(t, http.HandlerFunc(a.putSettings), http.MethodPut, "/api/admin/settings", removed, nil), http.StatusConflict)
			unchanged, _ := a.settings()
			unchanged.Methods[0].Enabled = false
			unchanged.Methods[0].Name = "No longer offered"
			expectStatus(t, requestJSON(t, http.HandlerFunc(a.putSettings), http.MethodPut, "/api/admin/settings", unchanged, nil), http.StatusOK)
			if countRows(t, a, "donations") != 1 {
				t.Fatal("payment method settings changed the historical ledger")
			}
		})
	}
}
