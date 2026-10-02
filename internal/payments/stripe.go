package payments

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type stripeSession struct {
	ID                string            `json:"id"`
	URL               string            `json:"url"`
	ClientReferenceID string            `json:"client_reference_id"`
	PaymentStatus     string            `json:"payment_status"`
	Mode              string            `json:"mode"`
	Currency          string            `json:"currency"`
	AmountTotal       int64             `json:"amount_total"`
	Metadata          map[string]string `json:"metadata"`
}

func stripeHeaders(m Method) http.Header {
	h := make(http.Header)
	h.Set("Authorization", "Bearer "+m.Config["secret_key"])
	// A stable API version keeps checkout/session fields predictable.
	h.Set("Stripe-Version", "2024-06-20")
	return h
}

func (s *Service) checkoutStripe(ctx context.Context, m Method, in CheckoutRequest) (CheckoutResult, error) {
	expiresAt, err := time.Parse(time.RFC3339Nano, in.ExpiresAt)
	if err != nil || expiresAt.Unix() <= 0 {
		return CheckoutResult{}, errors.New("Stripe checkout needs a persisted expiry")
	}
	form := url.Values{
		"mode": {"payment"}, "success_url": {in.ReturnURL}, "cancel_url": {in.CancelURL},
		"expires_at":          {strconv.FormatInt(expiresAt.Unix(), 10)},
		"client_reference_id": {in.ID}, "metadata[donation_id]": {in.ID}, "metadata[method_id]": {m.ID},
		"payment_intent_data[metadata][donation_id]": {in.ID}, "payment_intent_data[metadata][method_id]": {m.ID},
		"line_items[0][quantity]": {"1"}, "line_items[0][price_data][currency]": {strings.ToLower(in.Currency)},
		"line_items[0][price_data][unit_amount]":        {strconv.FormatInt(in.AmountMinor, 10)},
		"line_items[0][price_data][product_data][name]": {"Donation / 留一点燃料"},
	}
	if in.Email != "" {
		form.Set("customer_email", in.Email)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.stripe.com/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return CheckoutResult{}, err
	}
	req.Header = stripeHeaders(m)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Idempotency-Key", "donation-"+in.ID)
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return CheckoutResult{}, fmt.Errorf("Stripe checkout request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return CheckoutResult{}, fmt.Errorf("Stripe checkout returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return CheckoutResult{}, errors.New("cannot read Stripe checkout response")
	}
	var session stripeSession
	if json.Unmarshal(body, &session) != nil || !validReference(session.ID) || !strings.HasPrefix(session.ID, "cs_") || !providerCheckoutURL(session.URL, "checkout.stripe.com") {
		return CheckoutResult{}, errors.New("Stripe returned invalid checkout details")
	}
	return CheckoutResult{URL: session.URL, Reference: session.ID}, nil
}

func verifyStripeSignature(header string, body []byte, secret string) error {
	var timestamp string
	var signatures []string
	for _, pair := range strings.Split(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			if timestamp != "" {
				return errors.New("duplicate Stripe signature timestamp")
			}
			timestamp = value
		case "v1":
			signatures = append(signatures, value)
		}
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || len(signatures) == 0 {
		return errors.New("missing or malformed Stripe signature")
	}
	// Five-minute tolerance is Stripe's recommended replay-protection default.
	age := time.Now().Unix() - seconds
	if age > 300 || age < -300 {
		return errors.New("Stripe webhook timestamp outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp + "."))
	_, _ = mac.Write(body)
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		decoded, err := hex.DecodeString(signature)
		if err == nil && hmac.Equal(expected, decoded) {
			return nil
		}
	}
	return errors.New("Stripe webhook signature verification failed")
}

func stripeSessionConfirmation(m Method, eventID string, session stripeSession) (Confirmation, error) {
	currency := strings.ToUpper(session.Currency)
	if session.Mode != "payment" || session.PaymentStatus != "paid" {
		return Confirmation{EventID: eventID}, nil
	}
	if !validReference(session.ID) || !strings.HasPrefix(session.ID, "cs_") || !validReference(session.ClientReferenceID) || session.Metadata["donation_id"] != session.ClientReferenceID || session.Metadata["method_id"] != m.ID || !validateCurrency(currency) || session.AmountTotal <= 0 || session.AmountTotal > maxAmountMinor {
		return Confirmation{}, errors.New("Stripe payment amount, currency or donation reference is invalid")
	}
	return Confirmation{EventID: eventID, DonationID: session.ClientReferenceID, Reference: session.ID, AmountMinor: session.AmountTotal, Currency: currency, Paid: true}, nil
}

func (s *Service) verifyStripe(ctx context.Context, m Method, h http.Header, body []byte) (Confirmation, error) {
	if err := verifyStripeSignature(h.Get("Stripe-Signature"), body, m.Config["webhook_secret"]); err != nil {
		return Confirmation{}, err
	}
	var event struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		LiveMode bool   `json:"livemode"`
		Data     struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &event) != nil || !validReference(event.ID) || !strings.HasPrefix(event.ID, "evt_") {
		return Confirmation{}, errors.New("invalid Stripe webhook event")
	}
	isLive := strings.Contains(m.Config["secret_key"], "_live_")
	if event.LiveMode != isLive {
		return Confirmation{}, errors.New("Stripe webhook environment does not match payment method")
	}
	switch event.Type {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded":
		var session stripeSession
		if json.Unmarshal(event.Data.Object, &session) != nil {
			return Confirmation{}, errors.New("invalid Stripe session event")
		}
		return stripeSessionConfirmation(m, event.ID, session)
	case "charge.refunded":
		var charge struct {
			ID             string            `json:"id"`
			Amount         int64             `json:"amount"`
			AmountRefunded int64             `json:"amount_refunded"`
			Currency       string            `json:"currency"`
			Refunded       bool              `json:"refunded"`
			PaymentIntent  json.RawMessage   `json:"payment_intent"`
			Metadata       map[string]string `json:"metadata"`
		}
		if json.Unmarshal(event.Data.Object, &charge) != nil || !validReference(charge.ID) || !strings.HasPrefix(charge.ID, "ch_") {
			return Confirmation{}, errors.New("invalid Stripe refund event")
		}
		if !charge.Refunded || charge.AmountRefunded != charge.Amount || charge.Amount <= 0 {
			return Confirmation{EventID: event.ID}, nil
		}
		var intent string
		if json.Unmarshal(charge.PaymentIntent, &intent) != nil {
			var expanded struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(charge.PaymentIntent, &expanded)
			intent = expanded.ID
		}
		if !validReference(intent) || !strings.HasPrefix(intent, "pi_") {
			return Confirmation{}, errors.New("Stripe refund lacks the original payment intent")
		}
		var sessions struct {
			Data    []stripeSession `json:"data"`
			HasMore bool            `json:"has_more"`
		}
		endpoint := "https://api.stripe.com/v1/checkout/sessions?payment_intent=" + url.QueryEscape(intent) + "&limit=2"
		if err := s.requestJSON(ctx, http.MethodGet, endpoint, stripeHeaders(m), nil, &sessions); err != nil {
			return Confirmation{}, err
		}
		if len(sessions.Data) != 1 || sessions.HasMore {
			return Confirmation{}, errors.New("Stripe refund does not identify one donation checkout")
		}
		confirmed, err := stripeSessionConfirmation(m, event.ID, sessions.Data[0])
		if err != nil {
			return Confirmation{}, err
		}
		if !confirmed.Paid || confirmed.AmountMinor != charge.Amount || confirmed.Currency != strings.ToUpper(charge.Currency) {
			return Confirmation{}, errors.New("Stripe refund does not match the original payment")
		}
		confirmed.Paid = false
		confirmed.Refunded = true
		return confirmed, nil
	default:
		return Confirmation{EventID: event.ID}, nil
	}
}
