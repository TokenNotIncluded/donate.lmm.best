# Configurable on-chain payments

The server verifies public chain evidence and never holds a wallet signing key.
Enable `crypto` as a payment method in Admin → Payment methods. Configure shared
EVM, TRON and Solana receiving addresses there, then enable individual networks
and tokens. Addresses, RPC/WSS endpoints, contracts, precision, receiving-address
overrides and confirmation policy are stored in SQLite settings. Source presets
are editable seeds, not a runtime allowlist, and contain no merchant addresses.
The network JSON editor also accepts additional networks and tokens.

Stablecoins are priced at one token per USD. There is no fiat conversion or live
price oracle. A project using another currency retains its currency restriction;
crypto is available on the general USD form and USD projects. Tokens with similar
names are distinguished by exact contract/mint, and the selector labels USDT0
and Binance-Peg USDT explicitly. Configure a legacy token as a separate asset if
both versions need to be accepted; their contracts are not interchangeable.

## Verification and accounting

Each payment snapshots its address, token, precision, tolerance and network
policy. Later configuration edits or disabling a network do not change existing
receipts. All calculations use integer atomic units. The default underpayment
tolerance is 1%, configurable from 0 to 10% in 0.01% steps. The minimum is rounded
up in atomic units: a $10 order accepts 9.90 tokens at 1%, but 9.899999 does not.
Overpayments are accepted and shown separately; they are not refunded
automatically. Final partial transfers accumulate, and the wallet pre-fills the
remaining amount. The ledger and public statistics record actual receipts
rounded to USD cents, while the receipt retains original expectations and full
on-chain precision.

EVM verification checks chain ID, successful receipt, exact token Transfer logs,
net transfers to the recipient, canonical block hash, configured confirmations
and optionally the `finalized` head. TRON checks genesis identity and the
irreversible SolidityNode receipt, execution status and exact token/net transfer
logs. Solana checks genesis identity, successful transaction and finalized
signature status, and sums token-account balance changes owned by the recipient.
Node checks also verify token contracts and decimals before an operator enables
a network. Public RPC destinations reject internal/reserved IPs and redirects;
errors never expose provider URLs, credentials or response bodies.

A transaction is claimed once per actual chain identity, even across renamed
networks, token selections, different receiving addresses or concurrent orders.
A batched transaction is attributed to one order. Transactions older than order
creation (with a 30-second clock allowance) are rejected. The transaction ID is
not proof of donor identity; manual transfers from a shared receiving address
cannot provide a cryptographic identity assertion for exchange senders.

The default initiation window is 30 minutes and the confirmation deadline is
another 24 hours. Already submitted transactions continue confirming after
initiation expiry. Acceptance of transfers submitted after initiation expiry is
configurable. After the confirmation deadline the UI retains amounts, transaction
results and a contact-maintainer reason, and disables further payment. An
expired/failed order cannot locally cancel a transfer that has already broadcast.

## Wallets, QR and live status

EVM wallets use EIP-6963 discovery, switch/add the selected chain, check token
balance and request ERC-20 `transfer` with the exact atomic amount. Only the
explicit `wallet_rpc_url` is exposed for wallet network setup; private backend
RPC endpoints and keys are not sent to the browser. Solana uses Wallet Standard
and a locally bundled modern Solana SDK; it creates the recipient associated
token account idempotently and uses `TransferChecked`. TRON uses TronLink/TronWeb
with the exact TRC-20 amount. Wallets display and approve transfer fees, including
Solana token-account creation rent when applicable. The site never signs on a
user's behalf. The returned hash is saved in session storage before submission
so an interrupted verification does not cause another wallet transfer.

QR codes are generated locally by the server. EVM payment requests follow
EIP-681; Solana follows Solana Pay and includes the token and amount. There is
also a plain-address QR for wallets/exchanges without URI support. TRON's plain
address QR cannot prefill an amount; the TronLink button can. Unsupported wallets
can always use a manual transfer and paste the transaction ID.

Checkout uses authenticated Server-Sent Events (SSE), with reconnect snapshots
and keepalives. It does not repeatedly fetch payment status. WSS subscriptions
wake known, observed transactions when blocks/finality advance; reconnect and
restart recover persisted hashes. Unknown hashes retain exponential backoff.
For public endpoints without standard WSS (including the default Plasma, TRON,
HyperEVM and Robinhood endpoints), known transaction IDs receive bounded retries
through the confirmation deadline. No address-history scans or continuous
per-order address polling occur. Operators may configure WSS providers or signed
chain-event webhooks for faster updates.

## Webhooks

Outgoing notifications reuse the durable notification outbox in Admin →
Notifications. An endpoint is optional; the site settles payments independently
of delivery availability. Events include `donation.confirmed` and `payment.*`
state changes (`submitted`, `confirming`, `partial`, `paid`, `failed`, `expired`) with a stable event ID, expected/received atomic and decimal
amounts, network, asset, contract, recipient, last transaction ID and verification
result. `payment.paid` includes both normal and overpaid successes. Delivery
signatures, timestamp validation, retry schedule and receiver deduplication are
documented in [PAYMENTS.md](PAYMENTS.md). The outbox snapshots configuration and
queues settlement notifications in the same SQLite transaction as settlement.

Optional incoming chain hints use `POST /api/crypto/events`. Configure a separate
`crypto.event_secret` with 32–256 characters. Send:

```json
{"event_id":"provider-event-unique-id","network":"plasma","tx_id":"0x..."}
```

Headers: `X-Donate-Timestamp` (Unix seconds) and `X-Donate-Signature` =
`sha256=` + hex HMAC-SHA256(secret, timestamp + "." + raw request body). The allowed
clock skew is five minutes. Events deduplicate for 30 days. Omitting `tx_id` wakes
waiting transactions for that configured network. An event's claimed amount or
paid status is always ignored: the server reads chain evidence before settling.
An unset secret disables incoming events.

## Build and verification

`npm ci && npm run build:wallet` updates the committed Solana browser bundle.
Node is a build dependency only. `make check` runs vet and the race-enabled Go
suite, including exact-precision tolerance, finality/reorg, net receipts, partial
payments, replay protection, immutable quotes, authorization, SSE reconnect,
RPC failures and signed incoming-event spoof/replay checks. No automated test
moves real funds. A real wallet transfer remains a separate acceptance step.

Preset source references (verified 2026-10-04):

- [Tether supported protocols](https://tether.to/en/supported-protocols/)
- [USDT0 deployments](https://docs.usdt0.to/technical-documentation/deployments)
- [Circle USDC contracts](https://developers.circle.com/stablecoins/usdc-contract-addresses)
- [Robinhood Chain contracts](https://docs.robinhood.com/chain/contracts/)
- [Plasma token list and network metadata](https://github.com/PlasmaLaboratories/plasma-tokenlist)
- [HyperEVM RPC](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/hyperevm)
- [EIP-681](https://eips.ethereum.org/EIPS/eip-681)
- [Solana Pay](https://github.com/solana-labs/solana-pay/blob/master/SPEC.md)
- [TRON confirmation semantics](https://developers.tron.network/docs/confirmation-semantics)
