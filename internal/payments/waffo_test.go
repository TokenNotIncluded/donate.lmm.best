package payments

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

func waffoCheckoutTestMethod(t *testing.T) (Method, *rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	public := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
	m := Method{ID: "waffo-main", Type: "waffo", Name: "Waffo", Enabled: true, Config: map[string]string{"merchant_id": "MER_0123456789abcdefghijkl", "private_key": string(private), "environment": "test", "store_id": "STO_0123456789abcdefghijkl", "product_id": "PROD_0123456789abcdefghijkl", "tax_category": "digital_goods", "language": "zh-Hans"}}
	return m, key, public
}

func TestWaffoSignedCheckoutIncludesTaxInSelectedAmount(t *testing.T) {
	m, key, _ := waffoCheckoutTestMethod(t)
	s := &Service{Client: &http.Client{Transport: paymentTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://api.waffo.ai/v1/actions/checkout/create-session" || r.Method != "POST" {
			t.Fatal(r.URL)
		}
		body, _ := io.ReadAll(r.Body)
		bodyHash := sha256.Sum256(body)
		canonical := "POST\n" + r.URL.Path + "\n" + r.Header.Get("X-Timestamp") + "\n" + base64.StdEncoding.EncodeToString(bodyHash[:])
		hash := sha256.Sum256([]byte(canonical))
		signature, err := base64.StdEncoding.DecodeString(r.Header.Get("X-Signature"))
		if err != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], signature) != nil {
			t.Fatal("request was not signed over the exact wire body")
		}
		if r.Header.Get("X-Merchant-Id") != m.Config["merchant_id"] || r.Header.Get("X-Idempotency-Key") != "donation-donation-123" {
			t.Fatal("missing merchant authority/idempotency")
		}
		var in struct {
			ProductID string `json:"productId"`
			Currency  string `json:"currency"`
			Price     struct {
				Amount      string `json:"amount"`
				TaxIncluded bool   `json:"taxIncluded"`
			} `json:"priceSnapshot"`
			ExternalID string            `json:"orderMerchantExternalId"`
			Language   string            `json:"language"`
			Metadata   map[string]string `json:"metadata"`
		}
		if json.Unmarshal(body, &in) != nil || in.Price.Amount != "12.34" || !in.Price.TaxIncluded || in.Currency != "USD" || in.ProductID != m.Config["product_id"] || in.ExternalID != "donation-123" || in.Metadata["method_id"] != m.ID || in.Language != "zh-Hans" {
			t.Fatal(string(body))
		}
		return paymentResponse(200, `{"data":{"sessionId":"cs_checkout-123","checkoutUrl":"https://checkout.waffo.ai/my-store/checkout/cs_checkout-123"}}`), nil
	})}}
	result, err := s.Checkout(context.Background(), m, CheckoutRequest{ID: "donation-123", AmountMinor: 1234, Currency: "USD", ReturnURL: "https://example.com/?donation=donation-123", CancelURL: "https://example.com/"})
	if err != nil || result.Reference != "cs_checkout-123" {
		t.Fatal(result, err)
	}
}

func waffoWebhookTestSignature(t *testing.T, key *rsa.PrivateKey, body string, timestamp int64) string {
	t.Helper()
	value := strconv.FormatInt(timestamp, 10)
	hash := sha256.Sum256([]byte(value + "." + body))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	return "t=" + value + ",v1=" + base64.StdEncoding.EncodeToString(signature)
}

func TestWaffoOfficialSDKSignatureAndActualCharge(t *testing.T) {
	m, key, public := waffoCheckoutTestMethod(t)
	body := fmt.Sprintf(`{"id":"PAY_0123456789abcdefghijkl","eventId":"PAY_0123456789abcdefghijkl","eventType":"order.completed","storeId":%q,"mode":"test","data":{"orderId":"ORD_0123456789abcdefghijkl","orderMerchantExternalId":"donation-123","orderStatus":"completed","currency":"USD","chargedAmount":"12.34","amount":"999.99","listPrice":{"total":"999.99"},"paymentId":"PAY_0123456789abcdefghijkl","paymentStatus":"succeeded","orderMetadata":{"donation_id":"donation-123","method_id":"waffo-main"}}}`, m.Config["store_id"])
	opts := &pancake.VerifyWebhookOptions{PublicKey: public}
	h := make(http.Header)
	h.Set("X-Waffo-Signature", waffoWebhookTestSignature(t, key, body, time.Now().UnixMilli()))
	result, err := verifyWaffoPayload(m, h, []byte(body), opts)
	if err != nil || !result.Paid || result.AmountMinor != 1234 || result.Reference != "ORD_0123456789abcdefghijkl" || result.DonationID != "donation-123" {
		t.Fatal(result, err)
	}
	if _, err := verifyWaffoPayload(m, h, []byte(body+" "), opts); err == nil {
		t.Fatal("tampered raw body accepted")
	}
	h.Set("X-Waffo-Signature", waffoWebhookTestSignature(t, key, body, time.Now().UnixMilli()-46*60*1000))
	if _, err := verifyWaffoPayload(m, h, []byte(body), opts); err == nil {
		t.Fatal("old webhook accepted")
	}
	for _, bad := range []string{
		strings.Replace(body, `"chargedAmount":"12.34",`, "", 1),
		strings.Replace(body, `"mode":"test"`, `"mode":"prod"`, 1),
		strings.Replace(body, m.Config["store_id"], "STO_abcdefghijkl0123456789", 1),
		strings.Replace(body, `"paymentStatus":"succeeded"`, `"paymentStatus":"pending"`, 1),
		strings.Replace(body, `"donation_id":"donation-123"`, `"donation_id":"other"`, 1),
	} {
		h.Set("X-Waffo-Signature", waffoWebhookTestSignature(t, key, bad, time.Now().UnixMilli()))
		if _, err := verifyWaffoPayload(m, h, []byte(bad), opts); err == nil {
			t.Errorf("invalid signed webhook accepted: %s", bad)
		}
	}
	// Runtime verification cannot accept a test override key supplied by HTTP.
	h.Set("X-Waffo-Signature", waffoWebhookTestSignature(t, key, body, time.Now().UnixMilli()))
	if _, err := New().VerifyWebhook(context.Background(), m, h, []byte(body)); err == nil {
		t.Fatal("arbitrary test key accepted by runtime verifier")
	}
}

func TestWaffoRefundDoesNotUseOriginalPriceAsRefundAmount(t *testing.T) {
	m, key, public := waffoCheckoutTestMethod(t)
	opts := &pancake.VerifyWebhookOptions{PublicKey: public}
	body := fmt.Sprintf(`{"eventId":"REF_0123456789abcdefghijkl","eventType":"refund.succeeded","storeId":%q,"mode":"test","data":{"orderId":"ORD_0123456789abcdefghijkl","orderMerchantExternalId":"donation-123","currency":"USD","refundedAmount":"5.00","originalChargedAmount":"12.34","total":"12.34","paymentId":"PAY_0123456789abcdefghijkl","refundStatus":"succeeded"}}`, m.Config["store_id"])
	h := make(http.Header)
	h.Set("X-Waffo-Signature", waffoWebhookTestSignature(t, key, body, time.Now().UnixMilli()))
	result, err := verifyWaffoPayload(m, h, []byte(body), opts)
	if err != nil || result.Refunded {
		t.Fatal(result, err)
	}
	full := strings.Replace(body, `"refundedAmount":"5.00"`, `"refundedAmount":"12.34"`, 1)
	h.Set("X-Waffo-Signature", waffoWebhookTestSignature(t, key, full, time.Now().UnixMilli()))
	result, err = verifyWaffoPayload(m, h, []byte(full), opts)
	if err != nil || !result.Refunded || result.AmountMinor != 1234 || result.Paid {
		t.Fatal(result, err)
	}
}

func TestWaffoRejectsInvalidProviderBoundaries(t *testing.T) {
	m, _, _ := waffoCheckoutTestMethod(t)
	s := &Service{Client: &http.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) { t.Fatal("unexpected Waffo request"); return nil, nil })}}
	for _, request := range []CheckoutRequest{
		{ID: "d1", AmountMinor: 500, Currency: "TWD", ReturnURL: "https://example.com", CancelURL: "https://example.com"},
		{ID: "d1", AmountMinor: 99, Currency: "USD", ReturnURL: "https://example.com", CancelURL: "https://example.com"},
		{ID: "d1", AmountMinor: 1_000_001, Currency: "USD", ReturnURL: "https://example.com", CancelURL: "https://example.com"},
		{ID: "d1", AmountMinor: 500, Currency: "USD", ReturnURL: "http://localhost:8080", CancelURL: "http://localhost:8080"},
	} {
		if _, err := s.Checkout(context.Background(), m, request); err == nil {
			t.Error("invalid Waffo checkout accepted")
		}
	}
}

func TestWaffoHTTPGuardRejectsFailureAndHugeResponse(t *testing.T) {
	for _, test := range []struct {
		status int
		body   string
	}{{401, `{"data":{"stores":[]}}`}, {200, strings.Repeat("x", (2<<20)+1)}} {
		m, _, _ := waffoCheckoutTestMethod(t)
		s := &Service{Client: &http.Client{Transport: paymentTestTransport(func(*http.Request) (*http.Response, error) { return paymentResponse(test.status, test.body), nil })}}
		if _, err := s.WaffoStores(context.Background(), m); err == nil {
			t.Fatal("invalid provider response accepted")
		}
	}
}
