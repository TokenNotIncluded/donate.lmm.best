# Payment providers

Money is stored as integer minor units. USD/EUR/GBP/CNY/TWD/HKD use two decimals;
JPY uses whole yen. There is no floating-point rounding. Browser return URLs
only resume status polling: they cannot mark a donation as paid.

Configure methods in the hidden admin panel. The settings response contains
provider credentials only for the authenticated administrator. Credentials stay
server-side; do not put private keys in the public payment link. A disabled
method can be saved while it is being configured. Enabling it validates its
required fields. The service calls fixed official API hosts and refuses
redirects, including redirects that could leak API credentials.

## On-chain stablecoins

See [CRYPTO.md](CRYPTO.md) for configurable EVM/TRON/Solana networks, wallet
requests, local QR codes, transaction verification, tolerance, finality and
chain-event webhooks. Checkout receipts use SSE for this method.

## Waffo Pancake

Waffo is the first provider in the application. Its merchant-of-record checkout
adapter is implemented, but its [product policy](https://docs.waffo.ai/mor/prohibited-products)
prohibits charities, political/religious organizations and livestream tipping;
its [service terms](https://waffo.com/en/terms) also prohibit processing without
real goods or services. This project's voluntary open-source support purpose
requires explicit Waffo approval. Common ownership, solo-developer onboarding
or a successful API request do not establish that approval. Test integration
can be configured with the method disabled before production is approved;
selecting a software tax category does not turn a donation into a software sale.

Config fields:

| Field | Value |
| --- | --- |
| `merchant_id` | Merchant Short ID, `MER_` plus 22 base62 characters |
| `private_key` | RSA private key, at least 2048 bits; PKCS#1/PKCS#8 PEM, escaped-newline PEM, or raw PKCS#8 base64 |
| `environment` | `test` or `prod`, explicitly selected |
| `store_id` | Selected store Short ID, `STO_…` |
| `product_id` | Selected approved **one-time** product Short ID, `PROD_…` |
| `tax_category` | The approved product category: `digital_goods`, `saas`, `software`, `ebook`, `online_course`, `consulting`, or `professional_service` |
| `language` | Optional official cashier language, for example `en`, `zh-Hans`, `zh-Hant-TW`, `ja-JP` |

Credential-only disabled methods can list merchant stores and products before
selecting a checkout product. See [Waffo catalog administration](WAFFO-CATALOG.md)
for the product APIs and idempotency behavior.

The merchant key determines the API environment. Selecting a different
`environment` in this application does not turn a test key into a production
key. It selects the expected signed webhook environment. Production stores and
products need the provider's approval/publishing process.

Store approval is tied to its verified product website. Waffo documents a
per-store domain binding that remains after KYB approval; changing the approved
domain requires support to release the old binding and a review of the new one.
The official documentation gives no same-owner or sibling-subdomain exemption.
The Go SDK does not compare `successUrl` with the store website's host, and the
checkout API only explicitly documents an absolute HTTPS return URL. That is
not evidence that a new website or business purpose is covered by the existing
store approval. Complete deployment and test integration first; enable a new
domain's production checkout after Waffo confirms its approval scope or reviews
the change. See [domain verification](https://docs.waffo.ai/settings/domain-verification)
and [business review](https://docs.waffo.ai/settings/business-details).

Register an HTTP store webhook in Waffo for:

```text
https://your-domain/api/webhooks/waffo?method_id=YOUR_METHOD_ID
events: order.completed, refund.succeeded
```

Choose the matching test/prod webhook mode. The raw body and
`X-Waffo-Signature` are verified with the official SDK's environment-specific
public key. The SDK rejects signatures older than 45 minutes or over one minute
in the future; the durable ledger also deduplicates event IDs. Store ID, mode,
merchant external donation ID, actual charged amount, currency, channel payment
ID and successful status must match. A signed event's list price is never used
as evidence of the actual charge.

Checkout sets `orderMerchantExternalId` to the local donation ID and submits an
explicit dynamic price with `taxIncluded:true`. This keeps the amount the donor
selected as the total charge. The official Go SDK v0.16.0 omits
`priceSnapshot.taxIncluded`, so this one request uses the documented RSA-SHA256
REST signing format; catalog operations and webhook verification use the SDK.
Wire-body signing and tax-inclusive pricing are covered by tests. Checkout
session IDs and later order IDs differ: the ledger binds the signed order to
the pending donation, then uses that order ID for refund matching.

Current documented Waffo currencies are USD, EUR, GBP, HKD, JPY and CNY. TWD is
not offered through this adapter. The selected one-time product must have a
configured price for the checkout currency; a dynamic amount does not imply a
new supported product currency. Current checkout/product ranges are:

| Currency | Minimum | Maximum |
| --- | ---: | ---: |
| USD | 1 | 10,000 |
| EUR | 1 | 9,400 |
| GBP | 1 | 8,150 |
| HKD | 8 | 77,600 |
| JPY | 100 | 1,600,000 |
| CNY | 1 | 1,000 |

CNY is offered through WeChat. The public return URL must be HTTPS. Waffo has no
cancel redirect; abandoned sessions expire. After payment, the cashier's Done
button returns to this site, where payment status is polled.

References: [official Go SDK](https://github.com/waffo-com/waffo-pancake-sdk-go),
[checkout session API](https://docs.waffo.ai/api-reference/endpoints/orders/create-checkout-session),
[request signing](https://docs.waffo.ai/api-reference/authentication),
[full official documentation](https://docs.waffo.ai/llms-full.txt).

## Stripe

Config:

| Field | Value |
| --- | --- |
| `secret_key` | Secret/restricted API key (`sk_test_…`, `sk_live_…`, `rk_test_…`, `rk_live_…`) with Checkout Session create/read permissions |
| `webhook_secret` | Endpoint signing secret, `whsec_…` |

Checkout uses the hosted Stripe Checkout page, a server-set single line item,
the exact amount/currency, local donation ID and method ID metadata. Idempotency
keys bind checkout creation to the donation ID. Stripe's normal minimum charge,
merchant-country and payment-method restrictions apply; failed provider requests
leave the donation unconfirmed.

Register a **snapshot** event webhook:

```text
https://your-domain/api/webhooks/stripe?method_id=YOUR_METHOD_ID
events: checkout.session.completed, checkout.session.async_payment_succeeded, charge.refunded
```

The raw body is verified with HMAC-SHA256 and a five-minute timestamp tolerance.
Multiple `v1` signatures support key rotation. The key's live/test mode must
match the event. Completion only confirms a payment session whose
`payment_status` is `paid`; an unpaid completed checkout is still pending.

Full refund events are bound back to the original Checkout Session through an
authenticated provider lookup of their Payment Intent. Ambiguous session
results, mismatched amounts, currencies or donation metadata are rejected.
The integration pins Stripe's stable `2024-06-20` API version for its outbound
Checkout API calls. Use snapshot webhook payloads that expose these standard
session fields.

References: [Checkout Session API](https://docs.stripe.com/api/checkout/sessions/create),
[webhook signature verification](https://docs.stripe.com/webhooks/signature),
[webhook setup](https://docs.stripe.com/webhooks).

## PayPal

Config:

| Field | Value |
| --- | --- |
| `client_id` | REST application client ID |
| `client_secret` | REST application secret |
| `webhook_id` | ID of the webhook registered for that application |
| `environment` | `sandbox` or `live` |

The server obtains an OAuth access token, creates a single-unit CAPTURE order
with donation ID in both `custom_id` and `invoice_id`, then returns the official
PayPal approval URL. The return route captures the order server-side and reads
the authenticated order for full amount/currency/metadata; a return parameter
alone never confirms payment. Repeated returns read a previously completed
capture safely. Live and sandbox API hosts are fixed.

Register these application events:

```text
https://your-domain/api/webhooks/paypal?method_id=YOUR_METHOD_ID
events: PAYMENT.CAPTURE.COMPLETED, PAYMENT.CAPTURE.REFUNDED
```

Verification posts the signature headers, configured webhook ID and unchanged
raw event JSON to PayPal's official signature verification endpoint. Only an
explicit `SUCCESS` counts. The server then retrieves the original authenticated
order and matches the captured amount, currency, donation ID and capture ID.
Certificate URLs are limited to official PayPal hosts and are not fetched by
this application. PayPal's webhook simulator is not supported by its remote
postback verifier; test with events from a real sandbox application/order.

PayPal only accepts whole TWD amounts; the site still stores TWD cents, so the
amount must be a multiple of 100 minor units. Fractional values are rejected
without rounding. CNY acceptance depends on the merchant account country.

References: [Orders v2](https://developer.paypal.com/api/orders/v2/),
[webhook verification](https://developer.paypal.com/api/rest/webhooks/rest/),
[Payments v2](https://developer.paypal.com/docs/api/payments/v2/),
[currency support](https://developer.paypal.com/api/codes/currency).

## Custom QR and payment links

Custom methods use `qr_url` from an uploaded PNG/JPEG/WebP image and/or an HTTPS
`checkout_url`. They create a pending donation. An administrator must confirm
the amount after checking the real payment, or record an already received
donation manually. Opening a QR/link or clicking a return button cannot confirm
money. Custom methods have no provider credentials and no incoming automatic
payment webhook.

## Refund and verification boundary

This release models donation status as pending, paid or refunded. A verified
full refund removes the original donation from paid totals. Partial refunds do
not turn a whole donation into a full refund: they leave its status paid until
the provider reports a full refund. Waffo must report one full refund equal to
its original actual charge; multiple partial Waffo refunds are not accumulated
in this release. Stripe reports cumulative refunded amounts; PayPal exposes
the fully refunded capture status. If accounting partial refunds is required,
add a separate refund ledger before relying on totals as net receipts.

Tests mock outbound requests and exercise real RSA/HMAC verification, raw-body
integrity, replay windows, amount/currency/reference matching, idempotency and
refund checks. They never create real charges. Live acceptance still requires
the operator's approved provider accounts, HTTPS domain and real sandbox events.

The application's donation route calls `payments.ValidateCheckout` before
inserting a new pending donation. This non-network preflight rejects invalid
amount/currency choices, provider ranges, incomplete credentials and return URLs
without trapping payment-method configuration behind an unusable pending row.

## Outgoing donation and chain notifications

In Admin → Notifications, enable a receiver HTTPS URL and signing secret.
The durable outbox snapshots its destination and payload at enqueue time.
Receivers must deduplicate the JSON event `id` (or `X-Donate-Delivery`). A lost
acknowledgement can cause delivery again with the same identifiers and body.

`X-Donate-Signature-V2` is `sha256=` plus hex HMAC-SHA256(secret,
`X-Donate-Timestamp` + "." + exact raw body). Compare signatures in constant time
and reject delivery timestamps more than five minutes away. Each retry gets a
fresh timestamp; event creation time remains in the signed JSON body. Do not
reserialize JSON before signature verification. `X-Donate-Event` names the event.
The existing `X-Donate-Signature` body-only HMAC remains available for older
receivers. Never use outgoing notification bodies as chain-payment evidence.

A receiver returns HTTP 2xx after durably accepting the event. Other statuses
or network failures enter the existing bounded retry schedule; administrators
can inspect delivery failures and request a retry under Notifications. The
payment remains recorded even while notifications are unavailable.
