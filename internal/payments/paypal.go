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
	"strconv"
	"strings"
)

type paypalMoney struct {
	Currency string `json:"currency_code"`
	Value    string `json:"value"`
}

type paypalLink struct {
	Href string `json:"href"`
	Rel  string `json:"rel"`
}

type paypalCapture struct {
	ID                string       `json:"id"`
	Status            string       `json:"status"`
	CustomID          string       `json:"custom_id"`
	InvoiceID         string       `json:"invoice_id"`
	Amount            paypalMoney  `json:"amount"`
	Links             []paypalLink `json:"links"`
	SupplementaryData struct {
		RelatedIDs struct {
			OrderID   string `json:"order_id"`
			CaptureID string `json:"capture_id"`
		} `json:"related_ids"`
	} `json:"supplementary_data"`
}

type paypalUnit struct {
	CustomID  string      `json:"custom_id"`
	InvoiceID string      `json:"invoice_id"`
	Amount    paypalMoney `json:"amount"`
	Payments  struct {
		Captures []paypalCapture `json:"captures"`
	} `json:"payments"`
}

type paypalOrder struct {
	ID            string       `json:"id"`
	Status        string       `json:"status"`
	PurchaseUnits []paypalUnit `json:"purchase_units"`
	Links         []paypalLink `json:"links"`
}

func paypalBase(m Method) (string, error) {
	switch m.Config["environment"] {
	case "", "live":
		return "https://api-m.paypal.com", nil
	case "sandbox":
		return "https://api-m.sandbox.paypal.com", nil
	default:
		return "", errors.New("PayPal environment must be live or sandbox")
	}
}

func paypalIdentifier(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, ch := range id {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-') {
			return false
		}
	}
	return true
}

// paypalRequest preserves the supplied JSON bytes. The verification endpoint
// requires webhook_event exactly as received, including its whitespace.
func (s *Service) paypalRequest(req *http.Request, out any) error {
	res, err := s.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("PayPal request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("PayPal returned HTTP %d", res.StatusCode)
	}
	const maxResponse = 2 << 20
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponse+1))
	if err != nil {
		return fmt.Errorf("read PayPal response: %w", err)
	}
	if len(body) > maxResponse {
		return errors.New("PayPal response too large")
	}
	if err := json.Unmarshal(body, out); err != nil {
		return errors.New("invalid PayPal response")
	}
	return nil
}

func (s *Service) paypalAccessToken(ctx context.Context, m Method) (string, string, error) {
	base, err := paypalBase(m)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(m.Config["client_id"]) == "" || strings.TrimSpace(m.Config["client_secret"]) == "" {
		return "", "", errors.New("PayPal client ID and secret are required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/oauth2/token", strings.NewReader("grant_type=client_credentials"))
	if err != nil {
		return "", "", err
	}
	req.SetBasicAuth(m.Config["client_id"], m.Config["client_secret"])
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var result struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if err := s.paypalRequest(req, &result); err != nil {
		return "", "", err
	}
	if result.AccessToken == "" || !strings.EqualFold(result.TokenType, "Bearer") || strings.ContainsAny(result.AccessToken, "\r\n") {
		return "", "", errors.New("invalid PayPal access token")
	}
	return base, result.AccessToken, nil
}

func paypalHeaders(token string) http.Header {
	return http.Header{
		"Authorization": {"Bearer " + token},
		"Content-Type":  {"application/json"},
		"Accept":        {"application/json"},
		"Prefer":        {"return=representation"},
	}
}

func (s *Service) checkoutPayPal(ctx context.Context, m Method, in CheckoutRequest) (CheckoutResult, error) {
	if !validReference(in.ID) || len(in.ID) > 127 || in.AmountMinor <= 0 || !validateCurrency(in.Currency) {
		return CheckoutResult{}, errors.New("invalid PayPal donation amount, currency, or reference")
	}
	value := amountString(in.AmountMinor, in.Currency)
	// The ledger uses ISO minor units (TWD cents), while PayPal only accepts
	// whole TWD. Refuse to round a donor's chosen amount.
	if in.Currency == "TWD" {
		if in.AmountMinor%100 != 0 {
			return CheckoutResult{}, errors.New("PayPal accepts only whole TWD amounts")
		}
		value = strconv.FormatInt(in.AmountMinor/100, 10)
	}
	base, token, err := s.paypalAccessToken(ctx, m)
	if err != nil {
		return CheckoutResult{}, err
	}
	body := map[string]any{
		"intent": "CAPTURE",
		"purchase_units": []any{map[string]any{
			"custom_id": in.ID, "invoice_id": in.ID,
			"amount": paypalMoney{Currency: in.Currency, Value: value},
		}},
		"payment_source": map[string]any{"paypal": map[string]any{"experience_context": map[string]string{
			"shipping_preference": "NO_SHIPPING", "user_action": "PAY_NOW",
			"payment_method_preference": "IMMEDIATE_PAYMENT_REQUIRED",
			"return_url":                in.ReturnURL, "cancel_url": in.CancelURL,
		}}},
	}
	headers := paypalHeaders(token)
	headers.Set("PayPal-Request-Id", "donate-create-"+in.ID)
	var order paypalOrder
	if err := s.requestJSON(ctx, http.MethodPost, base+"/v2/checkout/orders", headers, body, &order); err != nil {
		return CheckoutResult{}, err
	}
	if !paypalIdentifier(order.ID) {
		return CheckoutResult{}, errors.New("PayPal did not return an order ID")
	}
	for _, link := range order.Links {
		if link.Rel != "approve" && link.Rel != "payer-action" {
			continue
		}
		u, err := url.Parse(link.Href)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
			continue
		}
		want := "www.paypal.com"
		if m.Config["environment"] == "sandbox" {
			want = "www.sandbox.paypal.com"
		}
		if u.Hostname() == want {
			return CheckoutResult{URL: link.Href, Reference: order.ID}, nil
		}
	}
	return CheckoutResult{}, errors.New("PayPal did not return a valid approval URL")
}

func paypalDonationUnit(order paypalOrder, orderID string) (paypalUnit, int64, error) {
	if order.ID != orderID || !paypalIdentifier(order.ID) || len(order.PurchaseUnits) != 1 {
		return paypalUnit{}, 0, errors.New("PayPal order does not match a single donation")
	}
	unit := order.PurchaseUnits[0]
	if !validReference(unit.CustomID) || len(unit.InvoiceID) > 127 || unit.InvoiceID != unit.CustomID {
		return paypalUnit{}, 0, errors.New("PayPal donation reference is missing or inconsistent")
	}
	amount, err := parseAmount(unit.Amount.Value, unit.Amount.Currency)
	if err != nil || amount <= 0 || !validateCurrency(unit.Amount.Currency) {
		return paypalUnit{}, 0, errors.New("invalid PayPal order amount")
	}
	return unit, amount, nil
}

func paypalMatchCapture(unit paypalUnit, original int64, capture paypalCapture) error {
	amount, err := parseAmount(capture.Amount.Value, capture.Amount.Currency)
	if err != nil || amount != original || capture.Amount.Currency != unit.Amount.Currency {
		return errors.New("PayPal capture amount or currency does not match the donation")
	}
	if !paypalIdentifier(capture.ID) || capture.CustomID != "" && capture.CustomID != unit.CustomID || capture.InvoiceID != "" && capture.InvoiceID != unit.InvoiceID {
		return errors.New("PayPal capture reference does not match the donation")
	}
	return nil
}

func (s *Service) capturePayPal(ctx context.Context, m Method, orderID string) (Confirmation, error) {
	if !paypalIdentifier(orderID) {
		return Confirmation{}, errors.New("invalid PayPal order ID")
	}
	base, token, err := s.paypalAccessToken(ctx, m)
	if err != nil {
		return Confirmation{}, err
	}
	headers := paypalHeaders(token)
	headers.Set("PayPal-Request-Id", "donate-capture-"+orderID)
	var order paypalOrder
	path := base + "/v2/checkout/orders/" + url.PathEscape(orderID)
	captureErr := s.requestJSON(ctx, http.MethodPost, path+"/capture", headers, map[string]any{}, &order)
	if captureErr == nil && order.ID != orderID {
		return Confirmation{}, errors.New("PayPal capture order does not match")
	}
	// Capture responses can omit purchase-unit metadata even when the capture
	// succeeded. Read the order to recover our amount and donation references.
	// This also safely handles a repeated return after ORDER_ALREADY_CAPTURED.
	if err := s.requestJSON(ctx, http.MethodGet, path, paypalHeaders(token), nil, &order); err != nil {
		if captureErr != nil {
			return Confirmation{}, captureErr
		}
		return Confirmation{}, err
	}
	unit, amount, err := paypalDonationUnit(order, orderID)
	if err != nil {
		return Confirmation{}, err
	}
	if order.Status != "COMPLETED" || len(unit.Payments.Captures) != 1 || unit.Payments.Captures[0].Status != "COMPLETED" {
		return Confirmation{}, errors.New("PayPal payment is not completed")
	}
	capture := unit.Payments.Captures[0]
	if err := paypalMatchCapture(unit, amount, capture); err != nil {
		return Confirmation{}, err
	}
	return Confirmation{EventID: "paypal-capture-" + capture.ID, DonationID: unit.CustomID, Reference: orderID, AmountMinor: amount, Currency: unit.Amount.Currency, Paid: true}, nil
}

func paypalCertificateURL(value string) bool {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !strings.HasPrefix(u.Path, "/v1/notifications/certs/") {
		return false
	}
	switch u.Hostname() {
	case "api.paypal.com", "api-m.paypal.com", "api.sandbox.paypal.com", "api-m.sandbox.paypal.com":
		return true
	}
	return false
}

// paypalRelatedID extracts identifiers from authenticated PayPal links. We
// never request their URL: all API calls are rebuilt using the fixed API host.
func paypalRelatedID(links []paypalLink, prefix string) string {
	for _, link := range links {
		if link.Rel != "up" {
			continue
		}
		u, err := url.Parse(link.Href)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || !strings.HasPrefix(u.Path, prefix) {
			continue
		}
		switch u.Hostname() {
		case "api.paypal.com", "api-m.paypal.com", "api.sandbox.paypal.com", "api-m.sandbox.paypal.com":
		default:
			continue
		}
		id := strings.TrimPrefix(u.Path, prefix)
		if paypalIdentifier(id) {
			return id
		}
	}
	return ""
}

func (s *Service) verifyPayPal(ctx context.Context, m Method, h http.Header, body []byte) (Confirmation, error) {
	if !json.Valid(body) || len(body) > 2<<20 || strings.TrimSpace(m.Config["webhook_id"]) == "" {
		return Confirmation{}, errors.New("invalid PayPal webhook or missing webhook ID")
	}
	fields := map[string]string{
		"auth_algo": h.Get("PayPal-Auth-Algo"), "cert_url": h.Get("PayPal-Cert-Url"),
		"transmission_id": h.Get("PayPal-Transmission-Id"), "transmission_sig": h.Get("PayPal-Transmission-Sig"),
		"transmission_time": h.Get("PayPal-Transmission-Time"), "webhook_id": m.Config["webhook_id"],
	}
	for _, value := range fields {
		if strings.TrimSpace(value) == "" || len(value) > 8192 {
			return Confirmation{}, errors.New("missing or invalid PayPal signature headers")
		}
	}
	if !paypalCertificateURL(fields["cert_url"]) {
		return Confirmation{}, errors.New("invalid PayPal certificate URL")
	}
	base, token, err := s.paypalAccessToken(ctx, m)
	if err != nil {
		return Confirmation{}, err
	}
	envelope, err := json.Marshal(fields)
	if err != nil {
		return Confirmation{}, err
	}
	envelope = append(envelope[:len(envelope)-1], []byte(",\"webhook_event\":")...)
	envelope = append(envelope, body...)
	envelope = append(envelope, '}')
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/notifications/verify-webhook-signature", bytes.NewReader(envelope))
	if err != nil {
		return Confirmation{}, err
	}
	req.Header = paypalHeaders(token)
	var verified struct {
		Status string `json:"verification_status"`
	}
	if err := s.paypalRequest(req, &verified); err != nil {
		return Confirmation{}, err
	}
	if verified.Status != "SUCCESS" {
		return Confirmation{}, errors.New("PayPal webhook signature verification failed")
	}
	var event struct {
		ID        string        `json:"id"`
		EventType string        `json:"event_type"`
		Resource  paypalCapture `json:"resource"`
	}
	if err := json.Unmarshal(body, &event); err != nil || !validReference(event.ID) {
		return Confirmation{}, errors.New("invalid PayPal webhook event")
	}
	if event.EventType != "PAYMENT.CAPTURE.COMPLETED" && event.EventType != "PAYMENT.CAPTURE.REFUNDED" {
		return Confirmation{EventID: event.ID}, nil
	}
	resource := event.Resource
	if !paypalIdentifier(resource.ID) || resource.Status != "COMPLETED" {
		return Confirmation{}, errors.New("PayPal webhook payment is not completed")
	}
	orderID := resource.SupplementaryData.RelatedIDs.OrderID
	if event.EventType == "PAYMENT.CAPTURE.REFUNDED" {
		captureID := resource.SupplementaryData.RelatedIDs.CaptureID
		if captureID == "" {
			captureID = paypalRelatedID(resource.Links, "/v2/payments/captures/")
		}
		if !paypalIdentifier(captureID) {
			return Confirmation{}, errors.New("PayPal refund is missing the original capture ID")
		}
		var original paypalCapture
		if err := s.requestJSON(ctx, http.MethodGet, base+"/v2/payments/captures/"+url.PathEscape(captureID), paypalHeaders(token), nil, &original); err != nil {
			return Confirmation{}, err
		}
		if original.ID != captureID {
			return Confirmation{}, errors.New("PayPal refund capture does not match")
		}
		// Partial refunds remain in the paid ledger. A full refund is emitted
		// only when PayPal reports the original capture entirely REFUNDED.
		if original.Status != "REFUNDED" {
			return Confirmation{EventID: event.ID}, nil
		}
		if orderID != "" && original.SupplementaryData.RelatedIDs.OrderID != "" && orderID != original.SupplementaryData.RelatedIDs.OrderID {
			return Confirmation{}, errors.New("PayPal refund order does not match")
		}
		if orderID == "" {
			orderID = original.SupplementaryData.RelatedIDs.OrderID
		}
		if orderID == "" {
			orderID = paypalRelatedID(original.Links, "/v2/checkout/orders/")
		}
		resource = original
	}
	if orderID == "" {
		orderID = paypalRelatedID(resource.Links, "/v2/checkout/orders/")
	}
	if !paypalIdentifier(orderID) {
		return Confirmation{}, errors.New("PayPal webhook is missing the original order ID")
	}
	var order paypalOrder
	if err := s.requestJSON(ctx, http.MethodGet, base+"/v2/checkout/orders/"+url.PathEscape(orderID), paypalHeaders(token), nil, &order); err != nil {
		return Confirmation{}, err
	}
	unit, amount, err := paypalDonationUnit(order, orderID)
	if err != nil {
		return Confirmation{}, err
	}
	if err := paypalMatchCapture(unit, amount, resource); err != nil {
		return Confirmation{}, err
	}
	if len(unit.Payments.Captures) != 1 || unit.Payments.Captures[0].ID != resource.ID {
		return Confirmation{}, errors.New("PayPal webhook capture does not match the order")
	}
	refunded := event.EventType == "PAYMENT.CAPTURE.REFUNDED"
	if !refunded && unit.Payments.Captures[0].Status != "COMPLETED" {
		return Confirmation{EventID: event.ID}, nil
	}
	return Confirmation{EventID: event.ID, DonationID: unit.CustomID, Reference: orderID, AmountMinor: amount, Currency: unit.Amount.Currency, Paid: !refunded, Refunded: refunded}, nil
}
