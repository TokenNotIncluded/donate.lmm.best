package payments

import (
	"errors"
	"net/url"
	"regexp"
	"strings"

	pancake "github.com/waffo-com/waffo-pancake-sdk-go"
)

var safeHTTPError = regexp.MustCompile(`^(payment provider|Stripe checkout|Waffo|PayPal) (returned|checkout returned) HTTP [0-9]{3}$`)

// SafeError is a bounded user-facing explanation. Provider response bodies,
// SDK error messages and transport request URLs can contain sensitive details;
// this function never relays them. Local validation messages are allowlisted.
func SafeError(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if strings.HasPrefix(message, "Waffo product saved; first publish failed") {
		return "Waffo product was saved, but first publish failed. Check store approval and product eligibility, then retry with the same Idempotency-Key."
	}
	var sdkError *pancake.Error
	if errors.As(err, &sdkError) {
		switch sdkError.Status {
		case 401, 403:
			return "Waffo rejected access. Check the merchant credentials, environment and store approval."
		case 429:
			return "Waffo rate limit reached. Retry shortly with the same Idempotency-Key."
		default:
			return "Waffo rejected the request. Check store and product selection, currency, product category and merchant approval."
		}
	}
	var transportError *url.Error
	if errors.As(err, &transportError) {
		return "Payment provider could not be reached. Check the server's network connection and retry."
	}
	if safeHTTPError.MatchString(message) {
		return message + ". Check provider credentials and environment; retry temporary failures."
	}
	if len(message) <= 240 && !strings.ContainsAny(message, "\r\n") {
		for _, prefix := range []string{
			"payment method is ", "payment method needs ", "payment config ", "unsupported payment ", "invalid donation ", "invalid checkout ",
			"custom payment ", "QR image must ", "Stripe needs ", "PayPal needs ", "PayPal environment must ", "PayPal accepts only ",
			"Waffo needs ", "Waffo environment must ", "Waffo merchant credentials are ", "Waffo checkout ",
			"Waffo USD amount must ", "Waffo EUR amount must ", "Waffo GBP amount must ", "Waffo HKD amount must ", "Waffo JPY amount must ", "Waffo CNY amount must ",
			"invalid Waffo currency or amount ", "unsupported Waffo checkout language", "select a valid Waffo ", "select the Waffo product's approved ",
			"Waffo product name must ", "Waffo product description is ", "Waffo product status must ", "a valid Idempotency-Key is required ",
			"Waffo catalog exceeds ", "Waffo did not return ", "Waffo returned invalid ", "Waffo returned an invalid ", "Waffo returned an unexpected ", "Waffo rejected the checkout; ",
		} {
			if strings.HasPrefix(message, prefix) {
				return message
			}
		}
	}
	return "Payment provider rejected the request. Check credentials, environment, currency and product configuration."
}
