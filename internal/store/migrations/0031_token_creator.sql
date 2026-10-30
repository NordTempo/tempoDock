-- A manual token now records the user who created it in user_id, so each
-- person sees and revokes their own tokens on their account page. It still
-- belongs to its org and acts for it, not for that user: authentication
-- only acts for user_id on `dock login` tokens (origin cli_login), and
-- disabling the user leaves it working.

-- Manual tokens recorded their creator by email only. A user created after
-- the token (the email reused after a deletion) is not its creator.
UPDATE tokens SET user_id = (
    SELECT users.id FROM users
    WHERE users.email = tokens.created_by AND users.created_at <= tokens.created_at
) WHERE origin = 'manual' AND user_id IS NULL;
