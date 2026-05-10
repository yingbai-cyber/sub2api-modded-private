-- Store raw Kiro credits consumed per request for credits-based billing display.
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS kiro_credits DECIMAL(20,10) NOT NULL DEFAULT 0;
