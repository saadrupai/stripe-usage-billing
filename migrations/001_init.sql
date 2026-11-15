CREATE TABLE IF NOT EXISTS customers (
    id                  TEXT PRIMARY KEY,
    email               TEXT NOT NULL,
    stripe_customer_id  TEXT UNIQUE,
    subscription_status TEXT NOT NULL DEFAULT 'trialing',
    current_period_end  TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- id is the client-supplied idempotency key: retries never double-count usage.
CREATE TABLE IF NOT EXISTS usage_events (
    id          TEXT PRIMARY KEY,
    customer_id TEXT NOT NULL REFERENCES customers(id),
    metric      TEXT NOT NULL,
    quantity    BIGINT NOT NULL CHECK (quantity > 0),
    occurred_at TIMESTAMPTZ NOT NULL,
    reported_at TIMESTAMPTZ,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS usage_unreported_idx ON usage_events (customer_id, metric) WHERE reported_at IS NULL;
CREATE INDEX IF NOT EXISTS usage_customer_time_idx ON usage_events (customer_id, occurred_at);

-- Stripe retries webhooks and may deliver duplicates; we process each event exactly once.
CREATE TABLE IF NOT EXISTS processed_webhooks (
    event_id     TEXT PRIMARY KEY,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
