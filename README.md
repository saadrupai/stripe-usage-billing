# Stripe Usage-Based Billing Service (Go)

A production-style backend for **usage-based (metered) billing** on Stripe, the pricing model most AI and API products use.
It ingests usage events, reports them to **Stripe Billing Meters**, and keeps each customer's subscription status in sync through **verified, idempotent webhooks**.

> Open-source demo project by [Saad Rupai](https://github.com/saadrupai), a backend engineer who builds billing, payments and AI systems.

## Why this exists

Most Stripe integrations break the same ways:

| Common bug | How this service handles it |
|---|---|
| Webhooks processed twice, so customers get double-charged or downgraded twice | Every Stripe event ID is recorded; duplicates are acknowledged and ignored |
| Fake webhook requests accepted | HMAC-SHA256 `Stripe-Signature` verification with a 5-minute replay window |
| Client retries double-count usage | `Idempotency-Key` header required; the key is the primary key |
| Stripe outage blocks the product | Usage is written locally first and reported asynchronously |
| Crash after Stripe accepted a report leads to double billing on retry | Each batch has a deterministic identifier, so Stripe de-duplicates it |
| Unpaid users keep using the product | Entitlement check on ingest, with a grace period while `past_due` |

## Architecture

```
client ──POST /v1/usage──▶ API ──▶ Postgres (usage_events)
                                        │
                         Reporter (every 30s) aggregates unreported usage
                                        │
                                        ▼
                       Stripe Billing Meter events (identifier = batch hash)

Stripe ──POST /webhooks/stripe──▶ verify signature ──▶ dedupe event ──▶ update subscription status
```

## API

All `/v1` endpoints need `Authorization: Bearer $API_KEY`.

```bash
# Create a customer linked to a Stripe customer
curl -X POST localhost:8080/v1/customers -H "Authorization: Bearer $API_KEY" \
  -d '{"id":"acme","email":"billing@acme.io","stripe_customer_id":"cus_123"}'

# Record usage (retries with the same Idempotency-Key are counted once)
curl -X POST localhost:8080/v1/usage -H "Authorization: Bearer $API_KEY" \
  -H "Idempotency-Key: req_8f2a" \
  -d '{"customer_id":"acme","metric":"api_requests","quantity":25}'

# Current-period usage
curl localhost:8080/v1/customers/acme/usage -H "Authorization: Bearer $API_KEY"
```

| Endpoint | Purpose |
|---|---|
| `POST /v1/customers` | Create a customer |
| `POST /v1/usage` | Ingest a usage event. Returns `202`, `200` for a duplicate, or `402` if the subscription is inactive |
| `GET /v1/customers/{id}/usage` | Usage totals for the current billing period |
| `POST /webhooks/stripe` | Stripe webhooks (`customer.subscription.*`, `invoice.paid`, `invoice.payment_failed`) |

## Run it

```bash
cp .env.example .env          # add Stripe TEST keys
docker compose up --build     # Postgres + API on :8080
stripe listen --forward-to localhost:8080/webhooks/stripe
```

In Stripe (test mode), create a **Meter** with event name `api_requests` and a metered price attached to it. Usage sent with `"metric":"api_requests"` then appears on the customer's upcoming invoice.

## Tests

```bash
make test
```

The tests cover signature verification (tampering, wrong secret, replay), duplicate webhook delivery, the payment-failed then paid flow, cancellation revoking access, idempotent usage ingestion, and retry behavior when Stripe returns 5xx.

## Stack

Go 1.27 (standard library `net/http` routing), PostgreSQL with pgx, Docker. The Stripe client is dependency-free.

## Need this in your product?

I build and fix Stripe billing for SaaS and AI products: webhooks, subscriptions, metered pricing and failed-payment recovery. You can reach me on [Fiverr](https://www.fiverr.com/saadrupai69).

MIT License
