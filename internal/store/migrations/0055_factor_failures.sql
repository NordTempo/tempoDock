-- Wrong second factors per user (ADR 0051): authenticator and recovery
-- codes from every sign-in and step-up count here, so guessing them has an
-- overall cap. Past 10 the next try waits longer each time; at 100 codes
-- are refused until `dock admin reset-2fa` or a password reset. A right
-- code clears it.
ALTER TABLE users ADD COLUMN factor_failures INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN factor_failed_at DATETIME;
