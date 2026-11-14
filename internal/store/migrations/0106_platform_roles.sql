-- The platform apart (ADR 0080, phase 2). Running the platform is a
-- platform role, never an org permission, and its own credentials.
--
-- users.platform_role replaces users.operator: 'operator' runs the
-- platform, 'viewer' reads the console; NULL is neither.
ALTER TABLE users ADD COLUMN platform_role TEXT CHECK (platform_role IN ('operator', 'viewer'));
UPDATE users SET platform_role = 'operator' WHERE operator;

-- A `dock login` token got its user's operator power. New ones act in
-- orgs only (`dock login --platform` makes a platform login); the ones
-- operators have now keep it until they expire, as legacy tokens that
-- are both (tokens.operator, like the org tokens that were made
-- operators by 0022).
UPDATE tokens SET operator = 1
WHERE origin = 'cli_login' AND revoked_at IS NULL
  AND user_id IN (SELECT id FROM users WHERE operator);

ALTER TABLE users DROP COLUMN operator;

-- tokens.platform marks a platform token, which acts in no org (org_id
-- NULL): 'operator' or 'viewer' for one an operator made in the console,
-- 'login' for a `dock login --platform`, which has its user's platform
-- role as it is now.
ALTER TABLE tokens ADD COLUMN platform TEXT CHECK (platform IN ('operator', 'viewer', 'login'));

-- The console lists the platform's tokens and the legacy ones that are
-- both; a handful among an install's tokens.
CREATE INDEX tokens_platform ON tokens (id) WHERE (platform IS NOT NULL OR operator) AND revoked_at IS NULL;

-- A `dock login --platform` waiting for approval asks for a platform login.
ALTER TABLE cli_logins ADD COLUMN platform BOOLEAN NOT NULL DEFAULT 0;
