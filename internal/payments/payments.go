// Package payments creates hosted provider checkouts and verifies payment evidence.
// It never treats a browser redirect as evidence that money moved.
package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Method struct {
	ID          string            `json:"id"`
	Type        string            `json:"type"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	QRURL       string            `json:"qr_url"`
	CheckoutURL string            `json:"checkout_url"`
	Enabled     bool              `json:"enabled"`
	Config      map[string]string `json:"config"`
}

type CheckoutRequest struct {
	ID                                                      string
	AmountMinor                                             int64
	Currency, Name, Email, ReturnURL, CancelURL, WebhookURL string
}

type CheckoutResult struct{ URL, Reference string }

// Reference is the provider order/session ID, not a user-supplied ID. The ledger
// must also match method, donation ID, currency and amount before applying this.
type Confirmation struct {
	EventID, DonationID, Reference string
	AmountMinor                    int64
	Currency                       string
	Paid                           bool
	Refunded                       bool
}

type Service struct{ Client *http.Client }

func New() *Service { return &Service{Client: &http.Client{Timeout: 25 * time.Second}} }

// ErrIgnoredEvent denotes verified events that do not settle or refund money.
var ErrIgnoredEvent = errors.New("payment event does not change donation status")

const maxAmountMinor int64 = 1_000_000_000_000

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
var methodPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
var waffoIDPattern = regexp.MustCompile(`^[A-Z]{2,5}_[A-Za-z0-9]{22}$`)

func validReference(value string) bool { return referencePattern.MatchString(value) }
func validateCurrency(value string) bool {
	switch value {
	case "USD", "EUR", "GBP", "CNY", "TWD", "HKD", "JPY":
		return true
	}
	return false
}

func amountString(minor int64, currency string) string {
	if currency == "JPY" {
		return strconv.FormatInt(minor, 10)
	}
	return fmt.Sprintf("%d.%02d", minor/100, minor%100)
}

// parseAmount performs decimal arithmetic without rounding floating-point values.
func parseAmount(value, currency string) (int64, error) {
	if !validateCurrency(currency) || value == "" || len(value) > 32 {
		return 0, errors.New("invalid payment amount or currency")
	}
	parts := strings.Split(value, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, errors.New("invalid payment amount")
	}
	for _, part := range parts {
		for _, r := range part {
			if r < '0' || r > '9' {
				return 0, errors.New("invalid payment amount")
			}
		}
	}
	whole, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || whole > maxAmountMinor {
		return 0, errors.New("payment amount out of range")
	}
	if currency == "JPY" {
		if len(parts) == 2 && (parts[1] == "" || len(parts[1]) > 2 || strings.Trim(parts[1], "0") != "") {
			return 0, errors.New("JPY amount must be a whole number")
		}
		return whole, nil
	}
	if whole > maxAmountMinor/100 {
		return 0, errors.New("payment amount out of range")
	}
	fraction := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) == 0 || len(parts[1]) > 2 {
			return 0, errors.New("payment amount has invalid precision")
		}
		p := parts[1]
		if len(p) == 1 {
			p += "0"
		}
		fraction, _ = strconv.ParseInt(p, 10, 64)
	}
	minor := whole*100 + fraction
	if minor > maxAmountMinor {
		return 0, errors.New("payment amount out of range")
	}
	return minor, nil
}

func ValidateMethod(m Method) error {
	if !methodPattern.MatchString(m.ID) || strings.TrimSpace(m.Name) == "" || len(m.Name) > 100 {
		return errors.New("payment method needs a valid id and name")
	}
	allowed := map[string]bool{}
	switch m.Type {
	case "waffo":
		for _, key := range []string{"merchant_id", "private_key", "environment", "store_id", "product_id", "tax_category", "language"} {
			allowed[key] = true
		}
	case "stripe":
		allowed["secret_key"] = true
		allowed["webhook_secret"] = true
	case "paypal":
		for _, key := range []string{"client_id", "client_secret", "webhook_id", "environment"} {
			allowed[key] = true
		}
	case "custom":
	default:
		return errors.New("unsupported payment method")
	}
	for key, value := range m.Config {
		if !allowed[key] {
			return fmt.Errorf("unsupported %s config field %s", m.Type, key)
		}
		if len(value) > 32768 {
			return errors.New("payment config is too long")
		}
		if key != "private_key" && (value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n")) {
			return errors.New("payment config values must not contain surrounding whitespace or line breaks")
		}
	}
	if m.Type == "custom" {
		if m.CheckoutURL != "" && !validPublicURL(m.CheckoutURL, false) {
			return errors.New("custom payment link must use HTTPS")
		}
		if m.QRURL != "" && !strings.HasPrefix(m.QRURL, "/uploads/") {
			return errors.New("QR image must be an uploaded image")
		}
		if m.Enabled && m.QRURL == "" && m.CheckoutURL == "" {
			return errors.New("custom payment needs an uploaded QR image or HTTPS link")
		}
		return nil
	}
	// Draft methods can be saved before credentials/products are provisioned.
	if !m.Enabled {
		return nil
	}
	switch m.Type {
	case "waffo":
		if _, err := New().waffoClient(m); err != nil {
			return err
		}
		if !validWaffoID(m.Config["store_id"], "STO") || !validWaffoID(m.Config["product_id"], "PROD") {
			return errors.New("Waffo checkout needs a selected store and product")
		}
		if !validTaxCategory(m.Config["tax_category"]) {
			return errors.New("Waffo checkout needs the product's approved tax category")
		}
		if lang := m.Config["language"]; lang != "" && !validWaffoLanguage(lang) {
			return errors.New("unsupported Waffo checkout language")
		}
	case "stripe":
		key := m.Config["secret_key"]
		if !(strings.HasPrefix(key, "sk_test_") || strings.HasPrefix(key, "sk_live_") || strings.HasPrefix(key, "rk_test_") || strings.HasPrefix(key, "rk_live_")) || len(key) < 12 {
			return errors.New("Stripe needs a secret API key")
		}
		if !strings.HasPrefix(m.Config["webhook_secret"], "whsec_") || len(m.Config["webhook_secret"]) < 12 {
			return errors.New("Stripe needs a webhook signing secret")
		}
	case "paypal":
		if m.Config["client_id"] == "" || m.Config["client_secret"] == "" || m.Config["webhook_id"] == "" {
			return errors.New("PayPal needs client ID, client secret and webhook ID")
		}
		if env := m.Config["environment"]; env != "live" && env != "sandbox" {
			return errors.New("PayPal environment must be live or sandbox")
		}
	}
	return nil
}

// ValidateCheckout is a local preflight; it performs no network requests. The
// application should call it before persisting a new pending donation, so a
// rejected provider currency/amount cannot leave an unusable checkout row.
func ValidateCheckout(m Method, in CheckoutRequest) error {
	if !m.Enabled {
		return errors.New("payment method is disabled")
	}
	if err := ValidateMethod(m); err != nil {
		return err
	}
	if !validReference(in.ID) || in.AmountMinor <= 0 || in.AmountMinor > maxAmountMinor || !validateCurrency(in.Currency) {
		return errors.New("invalid donation amount, currency or reference")
	}
	if !validPublicURL(in.ReturnURL, true) || !validPublicURL(in.CancelURL, true) {
		return errors.New("invalid checkout return URL")
	}
	switch m.Type {
	case "waffo":
		if !validPublicURL(in.ReturnURL, false) {
			return errors.New("Waffo checkout requires an HTTPS public return URL")
		}
		return waffoCurrencyAmount(in.AmountMinor, in.Currency)
	case "paypal":
		if len(in.ID) > 127 {
			return errors.New("invalid donation reference for PayPal (maximum 127 characters)")
		}
		if in.Currency == "TWD" && in.AmountMinor%100 != 0 {
			return errors.New("PayPal accepts only whole TWD amounts")
		}
	}
	return nil
}

func (s *Service) Checkout(ctx context.Context, m Method, in CheckoutRequest) (CheckoutResult, error) {
	if err := ValidateCheckout(m, in); err != nil {
		return CheckoutResult{}, err
	}
	switch m.Type {
	case "waffo":
		return s.checkoutWaffo(ctx, m, in)
	case "stripe":
		return s.checkoutStripe(ctx, m, in)
	case "paypal":
		return s.checkoutPayPal(ctx, m, in)
	case "custom":
		return CheckoutResult{URL: m.CheckoutURL, Reference: in.ID}, nil
	}
	return CheckoutResult{}, errors.New("unsupported payment provider")
}

func (s *Service) VerifyWebhook(ctx context.Context, m Method, h http.Header, body []byte) (Confirmation, error) {
	// A disabled method can still receive evidence for its existing checkouts.
	validation := m
	validation.Enabled = true
	if m.Type == "waffo" {
		// The selected product can be archived/cleared after an order was paid.
		// Callback verification needs merchant scope and store, not a new product.
		if _, err := s.waffoClient(m); err != nil {
			return Confirmation{}, err
		}
		if !validWaffoID(m.Config["store_id"], "STO") {
			return Confirmation{}, errors.New("Waffo webhook needs the original store configuration")
		}
	} else if err := ValidateMethod(validation); err != nil {
		return Confirmation{}, err
	}
	if len(body) == 0 || len(body) > 1<<20 {
		return Confirmation{}, errors.New("invalid payment webhook size")
	}
	switch m.Type {
	case "waffo":
		return s.verifyWaffo(ctx, m, h, body)
	case "stripe":
		return s.verifyStripe(ctx, m, h, body)
	case "paypal":
		return s.verifyPayPal(ctx, m, h, body)
	}
	return Confirmation{}, errors.New("custom payments need administrator confirmation")
}

func (s *Service) CapturePayPal(ctx context.Context, m Method, orderID string) (Confirmation, error) {
	validation := m
	validation.Enabled = true
	if m.Type != "paypal" {
		return Confirmation{}, errors.New("method is not PayPal")
	}
	if err := ValidateMethod(validation); err != nil {
		return Confirmation{}, err
	}
	if !validReference(orderID) {
		return Confirmation{}, errors.New("invalid PayPal order ID")
	}
	return s.capturePayPal(ctx, m, orderID)
}

func validPublicURL(value string, localhost bool) bool {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return localhost && u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")
}

func providerCheckoutURL(value string, hosts ...string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	for _, host := range hosts {
		if u.Host == host {
			return true
		}
	}
	return false
}

func (s *Service) httpClient() *http.Client {
	c := http.Client{Timeout: 25 * time.Second}
	if s != nil && s.Client != nil {
		c = *s.Client
		if c.Timeout == 0 {
			c.Timeout = 25 * time.Second
		}
	}
	// Credentials must never follow a redirect to a different host.
	c.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func (s *Service) requestJSON(ctx context.Context, method, endpoint string, headers http.Header, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header = headers.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	req.Header.Set("Accept", "application/json")
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("payment provider request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil {
		return errors.New("cannot read payment provider response")
	}
	if len(data) > 2<<20 {
		return errors.New("payment provider response too large")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("payment provider returned HTTP %d", resp.StatusCode)
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(data, output); err != nil {
		return errors.New("payment provider returned invalid JSON")
	}
	return nil
}

func validWaffoID(value, prefix string) bool {
	return waffoIDPattern.MatchString(value) && strings.HasPrefix(value, prefix+"_")
}
func validTaxCategory(value string) bool {
	switch value {
	case "digital_goods", "saas", "software", "ebook", "online_course", "consulting", "professional_service":
		return true
	}
	return false
}
func validWaffoLanguage(value string) bool {
	for _, language := range strings.Split("en,pt-BR,es-MX,id-ID,vi-VN,ru-RU,en-KE,es-PE,es-CO,es-CL,zh-Hant-TW,zh-Hant-HK,th-TH,ja-JP,en-NG,ko-KR,en-HK,zh-Hans-HK,pl-PL,tr-TR,zh-Hans,ms-MY", ",") {
		if value == language {
			return true
		}
	}
	return false
}
