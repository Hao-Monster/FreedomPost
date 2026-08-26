ALTER TABLE affiliates
  ADD COLUMN IF NOT EXISTS credential_version INTEGER NOT NULL DEFAULT 1;

ALTER TABLE affiliates
  DROP CONSTRAINT IF EXISTS chk_affiliates_credential_version;

ALTER TABLE affiliates
  ADD CONSTRAINT chk_affiliates_credential_version CHECK (credential_version > 0);

ALTER TABLE attachments
  ADD COLUMN IF NOT EXISTS claim_token_hash CHAR(64);

CREATE UNIQUE INDEX IF NOT EXISTS uniq_attachments_claim_token_hash
  ON attachments (claim_token_hash)
  WHERE claim_token_hash IS NOT NULL;

ALTER TABLE affiliate_orders
  ADD COLUMN IF NOT EXISTS base_commission_cents INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS markup_commission_cents INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS notes TEXT NOT NULL DEFAULT '';

UPDATE affiliate_orders
SET base_commission_cents = commission_cents,
    markup_commission_cents = 0
WHERE base_commission_cents = 0
  AND markup_commission_cents = 0
  AND commission_cents > 0;

ALTER TABLE affiliate_orders
  DROP CONSTRAINT IF EXISTS chk_affiliate_orders_base_commission,
  DROP CONSTRAINT IF EXISTS chk_affiliate_orders_markup_commission,
  DROP CONSTRAINT IF EXISTS chk_affiliate_orders_commission_sum;

ALTER TABLE affiliate_orders
  ADD CONSTRAINT chk_affiliate_orders_base_commission CHECK (base_commission_cents >= 0),
  ADD CONSTRAINT chk_affiliate_orders_markup_commission CHECK (markup_commission_cents >= 0),
  ADD CONSTRAINT chk_affiliate_orders_commission_sum
    CHECK (commission_cents = base_commission_cents + markup_commission_cents);
