package payments

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"

	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

type waffoGuardTransport struct{ next http.RoundTripper }

func (t waffoGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		resp.Body.Close()
		return nil, fmt.Errorf("Waffo returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	resp.Body.Close()
	if err != nil || len(data) > 2<<20 {
		return nil, errors.New("Waffo response is unreadable or too large")
	}
	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}

func (s *Service) waffoClient(m Method) (*pancake.Client, error) {
	if m.Type != "waffo" {
		return nil, errors.New("payment method is not Waffo")
	}
	draft := m
	draft.Enabled = false
	if err := ValidateMethod(draft); err != nil {
		return nil, err
	}
	if env := m.Config["environment"]; env != "test" && env != "prod" {
		return nil, errors.New("Waffo environment must be test or prod")
	}
	if !validWaffoID(m.Config["merchant_id"], "MER") {
		return nil, errors.New("Waffo needs a merchant Short ID")
	}
	if _, err := parseWaffoPrivateKey(m.Config["private_key"]); err != nil {
		return nil, err
	}
	client := s.httpClient()
	transport := client.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	client.Transport = waffoGuardTransport{next: transport}
	c, err := pancake.New(pancake.Config{MerchantID: m.Config["merchant_id"], PrivateKey: m.Config["private_key"], Environment: pancake.Environment(m.Config["environment"]), HTTPClient: client})
	if err != nil {
		return nil, errors.New("Waffo merchant credentials are invalid")
	}
	return c, nil
}

// Go SDK v0.16.0 omits priceSnapshot.taxIncluded. Checkout uses the documented
// signed REST endpoint so the donor's chosen amount includes tax. Catalog and
// webhook cryptography use the official SDK; remove this extension when the SDK
// exposes the field. Signing format: METHOD\nPATH\nTIMESTAMP\nBASE64(SHA256(BODY)).
func (s *Service) checkoutWaffo(ctx context.Context, m Method, in CheckoutRequest) (CheckoutResult, error) {
	if !validPublicURL(in.ReturnURL, false) {
		return CheckoutResult{}, errors.New("Waffo checkout requires an HTTPS public return URL")
	}
	if err := waffoCurrencyAmount(in.AmountMinor, in.Currency); err != nil {
		return CheckoutResult{}, err
	}
	key, err := parseWaffoPrivateKey(m.Config["private_key"])
	if err != nil {
		return CheckoutResult{}, err
	}
	input := map[string]any{
		"productId": m.Config["product_id"], "currency": in.Currency,
		"priceSnapshot":           map[string]any{"amount": amountString(in.AmountMinor, in.Currency), "taxIncluded": true, "taxCategory": m.Config["tax_category"]},
		"orderMerchantExternalId": in.ID, "metadata": map[string]string{"donation_id": in.ID, "method_id": m.ID},
		"successUrl": in.ReturnURL, "darkMode": true, "expiresInSeconds": 2700, "withTrial": false,
	}
	if in.Email != "" {
		input["buyerEmail"] = in.Email
	}
	if language := m.Config["language"]; language != "" {
		input["language"] = language
	}
	body, err := json.Marshal(input)
	if err != nil {
		return CheckoutResult{}, err
	}
	const path = "/v1/actions/checkout/create-session"
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	bodyHash := sha256.Sum256(body)
	canonical := http.MethodPost + "\n" + path + "\n" + timestamp + "\n" + base64.StdEncoding.EncodeToString(bodyHash[:])
	canonicalHash := sha256.Sum256([]byte(canonical))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, canonicalHash[:])
	if err != nil {
		return CheckoutResult{}, errors.New("cannot sign Waffo checkout")
	}
	headers := make(http.Header)
	headers.Set("X-Merchant-Id", m.Config["merchant_id"])
	headers.Set("X-Timestamp", timestamp)
	headers.Set("X-Signature", base64.StdEncoding.EncodeToString(signature))
	headers.Set("X-Idempotency-Key", "donation-"+in.ID)
	// Marshal the same deterministic map for signing and sending.
	var response struct {
		Data   pancake.CheckoutSessionResult `json:"data"`
		Errors []json.RawMessage             `json:"errors"`
	}
	if err := s.requestJSON(ctx, http.MethodPost, pancake.DefaultBaseURL+path, headers, input, &response); err != nil {
		return CheckoutResult{}, err
	}
	if len(response.Errors) > 0 {
		return CheckoutResult{}, errors.New("Waffo rejected the checkout; verify merchant product configuration")
	}
	if !validReference(response.Data.SessionID) || !strings.HasPrefix(response.Data.SessionID, "cs_") || !providerCheckoutURL(response.Data.CheckoutURL, "pancake.waffo.ai", "checkout.waffo.ai") {
		return CheckoutResult{}, errors.New("Waffo returned invalid checkout details")
	}
	return CheckoutResult{URL: response.Data.CheckoutURL, Reference: response.Data.SessionID}, nil
}

func parseWaffoPrivateKey(raw string) (*rsa.PrivateKey, error) {
	value := strings.ReplaceAll(strings.TrimSpace(raw), `\n`, "\n")
	isPKCS1 := strings.Contains(value, "-----BEGIN RSA PRIVATE KEY-----")
	for _, boundary := range []string{"-----BEGIN RSA PRIVATE KEY-----", "-----END RSA PRIVATE KEY-----", "-----BEGIN PRIVATE KEY-----", "-----END PRIVATE KEY-----"} {
		value = strings.ReplaceAll(value, boundary, "")
	}
	value = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, value)
	der, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, errors.New("Waffo needs an RSA private key in PEM or base64 format")
	}
	var key *rsa.PrivateKey
	if isPKCS1 {
		key, err = x509.ParsePKCS1PrivateKey(der)
	} else {
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(der)
		if err == nil {
			var ok bool
			key, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				err = errors.New("not RSA")
			}
		}
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return nil, errors.New("Waffo needs a valid RSA private key of at least 2048 bits")
	}
	return key, nil
}

func (s *Service) verifyWaffo(_ context.Context, m Method, h http.Header, body []byte) (Confirmation, error) {
	return verifyWaffoPayload(m, h, body, nil)
}

func verifyWaffoPayload(m Method, h http.Header, body []byte, options *pancake.VerifyWebhookOptions) (Confirmation, error) {
	opts := pancake.VerifyWebhookOptions{Environment: pancake.Environment(m.Config["environment"])}
	if options != nil {
		opts = *options
		opts.Environment = pancake.Environment(m.Config["environment"])
	}
	event, err := pancake.VerifyWebhookTyped[pancake.WebhookEventData](string(body), h.Get("X-Waffo-Signature"), &opts)
	if err != nil {
		return Confirmation{}, errors.New("Waffo webhook signature verification failed")
	}
	if event.Mode != opts.Environment || event.StoreID != m.Config["store_id"] || !validReference(event.EventID) {
		return Confirmation{}, errors.New("Waffo webhook environment, store or event ID does not match")
	}
	if event.EventType != "order.completed" && event.EventType != "refund.succeeded" {
		return Confirmation{EventID: event.EventID}, nil
	}
	data := event.Data
	if data.OrderMerchantExternalID == nil || !validReference(*data.OrderMerchantExternalID) || !validWaffoID(data.OrderID, "ORD") || !validateCurrency(data.Currency) {
		return Confirmation{}, errors.New("Waffo payment lacks a valid donation, order or currency")
	}
	if id := data.OrderMetadata["donation_id"]; id != "" && id != *data.OrderMerchantExternalID {
		return Confirmation{}, errors.New("Waffo donation metadata does not match")
	}
	if id := data.OrderMetadata["method_id"]; id != "" && id != m.ID {
		return Confirmation{}, errors.New("Waffo payment method metadata does not match")
	}
	if data.PaymentID == nil || !validWaffoID(*data.PaymentID, "PAY") {
		return Confirmation{}, errors.New("Waffo payment lacks a channel payment reference")
	}
	confirmed := Confirmation{EventID: event.EventID, DonationID: *data.OrderMerchantExternalID, Reference: data.OrderID, Currency: data.Currency}
	if event.EventType == "order.completed" {
		if data.OrderStatus == nil || *data.OrderStatus != "completed" || data.PaymentStatus == nil || *data.PaymentStatus != "succeeded" || data.ChargedAmount == nil {
			return Confirmation{}, errors.New("Waffo payment is not a confirmed channel charge")
		}
		amount, err := parseAmount(*data.ChargedAmount, data.Currency)
		if err != nil || amount <= 0 {
			return Confirmation{}, errors.New("Waffo actual charge amount is invalid")
		}
		confirmed.AmountMinor = amount
		confirmed.Paid = true
		return confirmed, nil
	}
	if data.RefundStatus == nil || *data.RefundStatus != "succeeded" || data.RefundedAmount == nil || data.OriginalChargedAmount == nil {
		return Confirmation{}, errors.New("Waffo refund lacks channel refund evidence")
	}
	amount, err := parseAmount(*data.RefundedAmount, data.Currency)
	if err != nil || amount <= 0 {
		return Confirmation{}, errors.New("Waffo actual refund amount is invalid")
	}
	original, err := parseAmount(*data.OriginalChargedAmount, data.Currency)
	if err != nil || original <= 0 || amount > original {
		return Confirmation{}, errors.New("Waffo refund exceeds the original charge")
	}
	if amount != original {
		return Confirmation{EventID: event.EventID}, nil
	}
	confirmed.AmountMinor = amount
	confirmed.Refunded = true
	return confirmed, nil
}
