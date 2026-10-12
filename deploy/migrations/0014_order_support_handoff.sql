-- Additive only: existing orders and their financial states are unchanged.
CREATE TABLE IF NOT EXISTS affiliate_order_requests (
  owner_hash CHAR(64) NOT NULL,
  request_key UUID NOT NULL,
  input_hash CHAR(64) NOT NULL,
  order_id UUID NOT NULL UNIQUE REFERENCES affiliate_orders(id) ON DELETE RESTRICT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (owner_hash, request_key),
  CONSTRAINT chk_order_request_hashes CHECK (
    owner_hash ~ '^[0-9a-f]{64}$' AND input_hash ~ '^[0-9a-f]{64}$'
  )
);

CREATE TABLE IF NOT EXISTS affiliate_order_support_deliveries (
  order_id UUID PRIMARY KEY REFERENCES affiliate_orders(id) ON DELETE RESTRICT,
  status VARCHAR(24) NOT NULL DEFAULT 'awaiting_session',
  binding_id VARCHAR(256),
  message_id BIGINT,
  conversation_id BIGINT,
  attempts INT NOT NULL DEFAULT 0,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  lease_until TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT chk_order_support_status CHECK (status IN (
    'awaiting_session', 'sending', 'sent', 'retry_wait', 'reconciling', 'action_required'
  )),
  CONSTRAINT chk_order_support_attempts CHECK (attempts >= 0),
  CONSTRAINT chk_order_support_receipt CHECK (
    (status = 'sent' AND message_id IS NOT NULL AND conversation_id IS NOT NULL
      AND message_id > 0 AND conversation_id > 0 AND binding_id IS NOT NULL)
    OR (status <> 'sent' AND message_id IS NULL AND conversation_id IS NULL)
  ),
  CONSTRAINT chk_order_support_sending CHECK (status <> 'sending' OR (binding_id IS NOT NULL AND lease_until IS NOT NULL))
);
