CREATE TABLE IF NOT EXISTS orders (
    id                      text PRIMARY KEY,
    checkout_token          text NOT NULL UNIQUE,
    merchant_order_no       text NOT NULL UNIQUE,
    channel                 text NOT NULL CHECK (channel IN ('alipay', 'wxpay')),
    name                    text NOT NULL,
    amount_cents            bigint NOT NULL CHECK (amount_cents > 0),
    status                  text NOT NULL CHECK (status IN ('pending', 'paid', 'closed', 'failed', 'refunded')),
    notify_url              text NOT NULL,
    return_url              text NOT NULL,
    provider_transaction_id text NOT NULL DEFAULT '',
    provider_pay_url        text NOT NULL DEFAULT '',
    provider_payload        bytea,
    created_at              timestamptz NOT NULL DEFAULT now(),
    updated_at              timestamptz NOT NULL DEFAULT now(),
    paid_at                 timestamptz,
    expires_at              timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS orders_pending_expiry_idx
    ON orders (expires_at) WHERE status = 'pending';

CREATE TABLE IF NOT EXISTS notify_deliveries (
    id              bigserial PRIMARY KEY,
    order_id        text NOT NULL UNIQUE REFERENCES orders(id) ON DELETE CASCADE,
    notify_url      text NOT NULL,
    attempts        integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    delivered_at    timestamptz,
    last_status     integer,
    last_error      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS notify_deliveries_due_idx
    ON notify_deliveries (next_attempt_at)
    WHERE delivered_at IS NULL;

