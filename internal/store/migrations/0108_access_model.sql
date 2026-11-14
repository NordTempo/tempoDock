-- Whether ADR 0080's permission model decides people's checks (enforce)
-- or only watches beside their roles (shadow), the platform's choice.
-- Shadow until an operator reads a clean diff and switches.
ALTER TABLE platform_settings ADD COLUMN access_model TEXT NOT NULL DEFAULT 'shadow';
