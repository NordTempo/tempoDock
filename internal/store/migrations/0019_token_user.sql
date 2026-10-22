-- A `dock login` token acts for the user who approved it, so it records
-- them by ID: authentication checks that user on every request (gone or
-- disabled: refused; scope capped at their current role), and disabling,
-- deleting or changing the password of a user revokes their login tokens.
-- Manual tokens are not tied to a user and leave it NULL.
ALTER TABLE tokens ADD COLUMN user_id INTEGER REFERENCES users (id) ON DELETE SET NULL;

-- Login tokens recorded their user by email only. A user created after the
-- token (the email reused after a deletion) is not its owner.
UPDATE tokens SET user_id = (
    SELECT users.id FROM users
    WHERE users.email = tokens.created_by AND users.created_at <= tokens.created_at
) WHERE origin = 'cli_login';

-- Login tokens whose user is gone or disabled stop working for good.
UPDATE tokens SET revoked_at = CURRENT_TIMESTAMP
WHERE origin = 'cli_login' AND revoked_at IS NULL
    AND (user_id IS NULL OR user_id IN (SELECT id FROM users WHERE disabled_at IS NOT NULL));

CREATE INDEX tokens_user ON tokens (user_id) WHERE user_id IS NOT NULL;
