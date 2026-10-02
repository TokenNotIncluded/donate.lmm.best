# Product

<!-- impeccable:product-schema 1 -->

## Platform
web

## Stack
Implementation choice: Go single binary with embedded plain HTML/CSS/JavaScript and pure-Go SQLite. Linux distributions should not require Node or a compiler to run releases. Packaging supports common native package formats, OCI container, and portable archive.

## Users
People donating to the site owner; one administrator configuring text, payment methods, notifications, and recording offline donations.

## Product Purpose
Allow visitors to enter or choose an amount and donate using configured payment methods. Give the owner accurate confirmed payment records and aggregate statistics.

## Capabilities and Constraints
Waffo Pancake primary provider, Stripe and PayPal adapters, configurable QR and payment links. Custom text, optional donor name/email/message. SQLite. Webhook notification includes donor, amount, currency, payment method, and time. Optional SMTP owner notifications. Statistics API. Manual donation entry and confirmation for offline payments. Hidden admin entrance after five logo activations. Administrator first uses CLI-only bootstrap password, binds Passkey, and password authentication is then disabled until CLI reset. No invented confirmed donations or payment support claims. Real provider credentials are absent.

## Brand Commitments
User requests ASCII art and interactive token cloud, alive and striking, with no ordinary technology-company template. User approved default name TOKEN / 留一点燃料 and default USD, editable in admin.

## Product Principles
Payment amount and action stay obvious. Only verified callbacks or administrator confirmation count as donated. Private donor information and credentials stay private. Admin setup and recovery work from local CLI.

## Open Decisions
Hosting domain and merchant credentials will be supplied by operator. Default copy is editable placeholder content. Plain web stack selected for packaging requirement.
