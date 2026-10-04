# Product

<!-- impeccable:product-schema 1 -->

## Platform
web

## Stack
Implementation choice: Go single binary with embedded plain HTML/CSS/JavaScript and pure-Go SQLite. Linux distributions should not require Node or a compiler to run releases. Packaging supports common native package formats, OCI container, and portable archive.

## Users
People donating as guests or optional Passkey account holders; one administrator configuring text, payment methods, notifications, and recording offline donations.

## Product Purpose
Allow visitors to enter or choose an amount and donate using configured payment methods. Give the owner accurate confirmed payment records and aggregate statistics.

## Capabilities and Constraints
Waffo Pancake primary provider, Stripe and PayPal adapters, configurable QR and payment links. Custom text, optional donor name/email/message. SQLite. Webhook notification includes donor, amount, currency, payment method, and time. Optional SMTP owner notifications. Statistics API. Manual donation entry and confirmation for offline payments. Hidden admin entrance after five logo activations. Administrator first uses CLI-only bootstrap password, binds Passkey, and password authentication is then disabled until CLI reset. Optional donor Passkey accounts have stable identity, private donation history and backup credentials, without admin privileges. Guest checkout stays available. No games or benefit promises are implemented. No invented confirmed donations or payment support claims. Credentials are configured by the operator; never commit credentials or production data.

Configurable noncustodial stablecoin payments use shared EVM, TRON and Solana receiving addresses, with editable networks and contracts. USD checkout supports wallet transfers with prefilled amounts, local QR codes, manual transaction-ID verification, partial top-ups and a configurable underpayment tolerance. Receipts distinguish expected amounts, confirmed receipts, pending confirmations and verification failures. Server-verified chain evidence settles the ledger; SSE updates the browser and durable signed webhooks can notify an optional receiver. The public source link shows a real cached GitHub star count when available.

## Brand Commitments
The user specifies Donate as the title, a small animated ASCII coffee logo, and a strictly black-and-white minimal interface. No slogans, explanatory introductions, decorative dashboard icons, or large hero art. Optional custom copy remains editable and is blank by default. Donation currency defaults by language: zh-CN uses CNY, zh-TW uses TWD, and English uses USD.

## Product Principles
Payment amount and action stay obvious. Only verified callbacks, server-verified chain receipts or administrator confirmation count as donated. Private donor information and credentials stay private. Admin setup and recovery work from local CLI.

## Open Decisions
Domain and payment credentials are operator configuration. Legal policies are short editable defaults. The plain web stack serves the Linux packaging requirement.
