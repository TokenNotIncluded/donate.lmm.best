package payments

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

type paypalTestTransport func(*http.Request) (*http.Response, error)

func (f paypalTestTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func paypalTestResponse(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

func paypalTestMethod() Method {
	return Method{ID: "paypal", Type: "paypal", Enabled: true, Config: map[string]string{
		"client_id": "client", "client_secret": "secret", "webhook_id": "webhook", "environment": "sandbox",
	}}
}

func paypalTestHeaders() http.Header {
	return http.Header{
		"Paypal-Auth-Algo": {"SHA256withRSA"}, "Paypal-Cert-Url": {"https://api-m.sandbox.paypal.com/v1/notifications/certs/CERT-123"},
		"Paypal-Transmission-Id": {"transmission"}, "Paypal-Transmission-Sig": {"signed-value"},
		"Paypal-Transmission-Time": {"2026-10-03T00:00:00Z"},
	}
}

const paypalTestOrder = `{"id":"ORDER123","status":"COMPLETED","purchase_units":[{"custom_id":"donation123","invoice_id":"donation123","amount":{"currency_code":"USD","value":"12.34"},"payments":{"captures":[{"id":"CAPTURE123","status":"COMPLETED","amount":{"currency_code":"USD","value":"12.34"}}]}}]}`

const paypalTestCompleted = `{
  "id": "WH-123", "event_type": "PAYMENT.CAPTURE.COMPLETED",
  "resource": {"id":"CAPTURE123","status":"COMPLETED","amount":{"currency_code":"USD","value":"12.34"},"supplementary_data":{"related_ids":{"order_id":"ORDER123"}}}
}`

func TestPayPalCheckoutCreatesBoundOrder(t *testing.T) {
	calls := 0
	s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host != "api-m.sandbox.paypal.com" {
			t.Fatalf("unexpected API host %q", req.URL.Host)
		}
		switch req.URL.Path {
		case "/v1/oauth2/token":
			id, secret, ok := req.BasicAuth()
			if !ok || id != "client" || secret != "secret" {
				t.Fatal("missing OAuth basic authentication")
			}
			body, _ := io.ReadAll(req.Body)
			if string(body) != "grant_type=client_credentials" || req.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
				t.Fatal("OAuth request must be form encoded")
			}
			return paypalTestResponse(`{"access_token":"test-token","token_type":"Bearer"}`), nil
		case "/v2/checkout/orders":
			if req.Method != http.MethodPost || req.Header.Get("Authorization") != "Bearer test-token" || req.Header.Get("PayPal-Request-Id") != "donate-create-donation123" {
				t.Fatal("missing order authentication or idempotency")
			}
			var input struct {
				Intent        string       `json:"intent"`
				Units         []paypalUnit `json:"purchase_units"`
				PaymentSource struct {
					Paypal struct {
						Experience map[string]string `json:"experience_context"`
					} `json:"paypal"`
				} `json:"payment_source"`
			}
			if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			if input.Intent != "CAPTURE" || len(input.Units) != 1 || input.Units[0].CustomID != "donation123" || input.Units[0].InvoiceID != "donation123" || input.Units[0].Amount.Value != "12.34" || input.Units[0].Amount.Currency != "USD" {
				t.Fatalf("incorrect bound purchase unit: %+v", input)
			}
			if input.PaymentSource.Paypal.Experience["return_url"] != "https://donate.example/api/paypal/return" || input.PaymentSource.Paypal.Experience["shipping_preference"] != "NO_SHIPPING" {
				t.Fatal("checkout experience missing return URL or shipping preference")
			}
			return paypalTestResponse(`{"id":"ORDER123","links":[{"rel":"payer-action","href":"https://www.sandbox.paypal.com/checkoutnow?token=ORDER123"}]}`), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}}
	got, err := s.checkoutPayPal(context.Background(), paypalTestMethod(), CheckoutRequest{ID: "donation123", AmountMinor: 1234, Currency: "USD", ReturnURL: "https://donate.example/api/paypal/return", CancelURL: "https://donate.example/"})
	if err != nil || got.Reference != "ORDER123" || got.URL != "https://www.sandbox.paypal.com/checkoutnow?token=ORDER123" || calls != 2 {
		t.Fatalf("checkout = %+v, %v; calls %d", got, err, calls)
	}
}

func TestPayPalCaptureRequiresExactCompletedDonation(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		wantErr        bool
	}{
		{"completed", paypalTestOrder, false},
		{"pending", strings.ReplaceAll(paypalTestOrder, `"status":"COMPLETED"`, `"status":"PENDING"`), true},
		{"wrong order", strings.ReplaceAll(paypalTestOrder, "ORDER123", "OTHER123"), true},
		{"conflicting donation", strings.Replace(paypalTestOrder, `"invoice_id":"donation123"`, `"invoice_id":"other"`, 1), true},
		{"capture amount differs", strings.Replace(paypalTestOrder, `"value":"12.34"`, `"value":"12.35"`, 1), true},
		{"missing capture", `{"id":"ORDER123","status":"COMPLETED","purchase_units":[{"custom_id":"donation123","invoice_id":"donation123","amount":{"currency_code":"USD","value":"12.34"}}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/v1/oauth2/token" {
					return paypalTestResponse(`{"access_token":"test-token","token_type":"Bearer"}`), nil
				}
				if req.URL.Path == "/v2/checkout/orders/ORDER123/capture" {
					if req.Method != http.MethodPost || req.Header.Get("PayPal-Request-Id") != "donate-capture-ORDER123" {
						t.Fatalf("incorrect capture request %s %s", req.Method, req.URL)
					}
				} else if req.URL.Path != "/v2/checkout/orders/ORDER123" || req.Method != http.MethodGet {
					t.Fatalf("incorrect order request %s %s", req.Method, req.URL)
				}
				return paypalTestResponse(tc.response), nil
			})}}
			got, err := s.capturePayPal(context.Background(), paypalTestMethod(), "ORDER123")
			if (err != nil) != tc.wantErr {
				t.Fatalf("capture = %+v, %v; want error %v", got, err, tc.wantErr)
			}
			if !tc.wantErr && (!got.Paid || got.Refunded || got.AmountMinor != 1234 || got.Currency != "USD" || got.DonationID != "donation123" || got.Reference != "ORDER123" || got.EventID != "paypal-capture-CAPTURE123") {
				t.Fatalf("incorrect confirmation %+v", got)
			}
		})
	}
}

func TestPayPalCaptureRetryReadsExistingOrder(t *testing.T) {
	s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/oauth2/token":
			return paypalTestResponse(`{"access_token":"test-token","token_type":"Bearer"}`), nil
		case "/v2/checkout/orders/ORDER123/capture":
			res := paypalTestResponse(`{"name":"UNPROCESSABLE_ENTITY"}`)
			res.StatusCode = http.StatusUnprocessableEntity
			return res, nil
		case "/v2/checkout/orders/ORDER123":
			if req.Method != http.MethodGet {
				t.Fatal("retry must read order")
			}
			return paypalTestResponse(paypalTestOrder), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}}
	got, err := s.capturePayPal(context.Background(), paypalTestMethod(), "ORDER123")
	if err != nil || !got.Paid || got.Reference != "ORDER123" {
		t.Fatalf("retry = %+v, %v", got, err)
	}
}

func TestPayPalCaptureRecoversOmittedPurchaseUnitMetadata(t *testing.T) {
	s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1/oauth2/token":
			return paypalTestResponse(`{"access_token":"test-token","token_type":"Bearer"}`), nil
		case "/v2/checkout/orders/ORDER123/capture":
			return paypalTestResponse(`{"id":"ORDER123","status":"COMPLETED","purchase_units":[{"reference_id":"default","payments":{"captures":[{"id":"CAPTURE123","status":"COMPLETED","amount":{"currency_code":"USD","value":"12.34"}}]}}]}`), nil
		case "/v2/checkout/orders/ORDER123":
			return paypalTestResponse(paypalTestOrder), nil
		default:
			t.Fatalf("unexpected request %s", req.URL)
			return nil, nil
		}
	})}}
	got, err := s.capturePayPal(context.Background(), paypalTestMethod(), "ORDER123")
	if err != nil || !got.Paid || got.DonationID != "donation123" || got.AmountMinor != 1234 {
		t.Fatalf("capture with omitted unit fields = %+v, %v", got, err)
	}
}

func TestPayPalWebhookRemoteVerificationPreservesRawEvent(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(map[bool]string{false: "invalid signature", true: "verified"}[success], func(t *testing.T) {
			calls := 0
			s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				switch req.URL.Path {
				case "/v1/oauth2/token":
					return paypalTestResponse(`{"access_token":"test-token","token_type":"Bearer"}`), nil
				case "/v1/notifications/verify-webhook-signature":
					data, _ := io.ReadAll(req.Body)
					if !strings.Contains(string(data), `"webhook_event":`+paypalTestCompleted) {
						t.Fatal("verification changed the original webhook formatting")
					}
					if req.Header.Get("Authorization") != "Bearer test-token" {
						t.Fatal("missing verification authorization")
					}
					var envelope map[string]json.RawMessage
					if err := json.Unmarshal(data, &envelope); err != nil {
						t.Fatal(err)
					}
					if string(envelope["webhook_id"]) != `"webhook"` || string(envelope["transmission_sig"]) != `"signed-value"` {
						t.Fatal("incorrect signature envelope")
					}
					if success {
						return paypalTestResponse(`{"verification_status":"SUCCESS"}`), nil
					}
					return paypalTestResponse(`{"verification_status":"FAILURE"}`), nil
				case "/v2/checkout/orders/ORDER123":
					if !success {
						t.Fatal("unverified webhook must not fetch or confirm payment")
					}
					return paypalTestResponse(paypalTestOrder), nil
				default:
					t.Fatalf("unexpected request %s", req.URL)
					return nil, nil
				}
			})}}
			got, err := s.verifyPayPal(context.Background(), paypalTestMethod(), paypalTestHeaders(), []byte(paypalTestCompleted))
			if success {
				if err != nil || !got.Paid || got.EventID != "WH-123" || got.DonationID != "donation123" || got.AmountMinor != 1234 || got.Reference != "ORDER123" || calls != 3 {
					t.Fatalf("verified = %+v, %v; calls %d", got, err, calls)
				}
			} else if err == nil || got.Paid || calls != 2 {
				t.Fatalf("unverified = %+v, %v; calls %d", got, err, calls)
			}
		})
	}
}

func TestPayPalRefundRequiresOriginalCaptureEntirelyRefunded(t *testing.T) {
	const refund = `{"id":"WH-REFUND","event_type":"PAYMENT.CAPTURE.REFUNDED","resource":{"id":"REFUND123","status":"COMPLETED","amount":{"currency_code":"USD","value":"2.34"},"links":[{"rel":"up","href":"https://api-m.sandbox.paypal.com/v2/payments/captures/CAPTURE123"}]}}`
	for _, fullyRefunded := range []bool{false, true} {
		t.Run(map[bool]string{false: "partial", true: "full"}[fullyRefunded], func(t *testing.T) {
			s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/v1/oauth2/token":
					return paypalTestResponse(`{"access_token":"test-token","token_type":"Bearer"}`), nil
				case "/v1/notifications/verify-webhook-signature":
					return paypalTestResponse(`{"verification_status":"SUCCESS"}`), nil
				case "/v2/payments/captures/CAPTURE123":
					status := "PARTIALLY_REFUNDED"
					if fullyRefunded {
						status = "REFUNDED"
					}
					return paypalTestResponse(`{"id":"CAPTURE123","status":"` + status + `","amount":{"currency_code":"USD","value":"12.34"},"links":[{"rel":"up","href":"https://api-m.sandbox.paypal.com/v2/checkout/orders/ORDER123"}]}`), nil
				case "/v2/checkout/orders/ORDER123":
					if !fullyRefunded {
						t.Fatal("partial refund should not change full donation ledger")
					}
					return paypalTestResponse(strings.Replace(paypalTestOrder, `"id":"CAPTURE123","status":"COMPLETED"`, `"id":"CAPTURE123","status":"REFUNDED"`, 1)), nil
				default:
					t.Fatalf("unexpected request %s", req.URL)
					return nil, nil
				}
			})}}
			got, err := s.verifyPayPal(context.Background(), paypalTestMethod(), paypalTestHeaders(), []byte(refund))
			if err != nil {
				t.Fatal(err)
			}
			if fullyRefunded {
				if !got.Refunded || got.Paid || got.AmountMinor != 1234 || got.Reference != "ORDER123" || got.DonationID != "donation123" {
					t.Fatalf("full refund = %+v", got)
				}
			} else if got.Paid || got.Refunded {
				t.Fatalf("partial refund = %+v", got)
			}
		})
	}
}

func TestPayPalRejectsInvalidInputsWithoutRequests(t *testing.T) {
	s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
		t.Fatalf("invalid input made a request %s", req.URL)
		return nil, nil
	})}}
	if _, err := s.capturePayPal(context.Background(), paypalTestMethod(), "../orders"); err == nil {
		t.Fatal("accepted path traversal order ID")
	}
	if _, err := s.checkoutPayPal(context.Background(), paypalTestMethod(), CheckoutRequest{ID: "donation123", AmountMinor: 1234, Currency: "TWD"}); err == nil {
		t.Fatal("accepted fractional TWD amount")
	}
	if _, err := s.checkoutPayPal(context.Background(), paypalTestMethod(), CheckoutRequest{ID: strings.Repeat("a", 128), AmountMinor: 1234, Currency: "USD"}); err == nil {
		t.Fatal("accepted donation reference longer than PayPal invoice limit")
	}
	h := paypalTestHeaders()
	h.Set("PayPal-Cert-Url", "https://attacker.example/cert")
	if _, err := s.verifyPayPal(context.Background(), paypalTestMethod(), h, []byte(paypalTestCompleted)); err == nil {
		t.Fatal("accepted non-PayPal certificate URL")
	}
	h = paypalTestHeaders()
	h.Del("PayPal-Transmission-Sig")
	if _, err := s.verifyPayPal(context.Background(), paypalTestMethod(), h, []byte(paypalTestCompleted)); err == nil {
		t.Fatal("accepted missing signature")
	}
}

func TestPayPalTWDCurrencyUsesWholeProviderAmounts(t *testing.T) {
	s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/v1/oauth2/token" {
			return paypalTestResponse(`{"access_token":"test-token","token_type":"Bearer"}`), nil
		}
		var input struct {
			Units []paypalUnit `json:"purchase_units"`
		}
		if err := json.NewDecoder(req.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if len(input.Units) != 1 || input.Units[0].Amount.Value != "12" || input.Units[0].Amount.Currency != "TWD" {
			t.Fatalf("incorrect TWD amount: %+v", input)
		}
		return paypalTestResponse(`{"id":"ORDER123","links":[{"rel":"approve","href":"https://www.sandbox.paypal.com/checkoutnow?token=ORDER123"}]}`), nil
	})}}
	if _, err := s.checkoutPayPal(context.Background(), paypalTestMethod(), CheckoutRequest{ID: "donation123", AmountMinor: 1200, Currency: "TWD"}); err != nil {
		t.Fatal(err)
	}
	order := paypalOrder{ID: "ORDER123", PurchaseUnits: []paypalUnit{{CustomID: "donation123", InvoiceID: "donation123", Amount: paypalMoney{Currency: "TWD", Value: "12"}}}}
	_, amount, err := paypalDonationUnit(order, "ORDER123")
	if err != nil || amount != 1200 {
		t.Fatalf("provider TWD amount parsed as %d, %v", amount, err)
	}
}

func TestPayPalWebhookRejectsInconsistentCaptureEvidence(t *testing.T) {
	for _, tc := range []struct{ name, event, order string }{
		{"amount", strings.Replace(paypalTestCompleted, `"value":"12.34"`, `"value":"12.35"`, 1), paypalTestOrder},
		{"currency", strings.Replace(paypalTestCompleted, `"currency_code":"USD"`, `"currency_code":"EUR"`, 1), paypalTestOrder},
		{"capture ID", paypalTestCompleted, strings.ReplaceAll(paypalTestOrder, "CAPTURE123", "OTHER123")},
		{"donation ID", strings.Replace(paypalTestCompleted, `"id":"CAPTURE123"`, `"id":"CAPTURE123","custom_id":"otherdonation"`, 1), paypalTestOrder},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{Client: &http.Client{Transport: paypalTestTransport(func(req *http.Request) (*http.Response, error) {
				switch req.URL.Path {
				case "/v1/oauth2/token":
					return paypalTestResponse(`{"access_token":"test-token","token_type":"Bearer"}`), nil
				case "/v1/notifications/verify-webhook-signature":
					return paypalTestResponse(`{"verification_status":"SUCCESS"}`), nil
				case "/v2/checkout/orders/ORDER123":
					return paypalTestResponse(tc.order), nil
				default:
					t.Fatalf("unexpected request %s", req.URL)
					return nil, nil
				}
			})}}
			got, err := s.verifyPayPal(context.Background(), paypalTestMethod(), paypalTestHeaders(), []byte(tc.event))
			if err == nil || got.Paid {
				t.Fatalf("inconsistent evidence accepted: %+v, %v", got, err)
			}
		})
	}
}
