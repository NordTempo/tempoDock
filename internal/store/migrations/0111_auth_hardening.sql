-- Sign-in hardening before Dock goes public.
--
-- users.email_proven: whether the account's email was ever shown to
-- reach its owner. An invite sign-up sets a password without proving the
-- mailbox, so a single sign-on with that email doesn't link to the
-- account as it is: it takes the account over for the mailbox's owner
-- (core.ssoUser). Accounts made before now count as proven, as do ones
-- an operator or a single sign-on makes and ones a reset link proved.
ALTER TABLE users ADD COLUMN email_proven BOOLEAN NOT NULL DEFAULT 1;

-- sessions.legacy_cookie marks the sessions signed in before session
-- cookies took the __Host- prefix over HTTPS (a sibling app hostname
-- can't set one of those). Only these still authenticate from the old
-- cookie name on HTTPS, so nobody is signed out mid-session and a cookie
-- an app tosses at Dock under the old name can't carry a new session.
-- They all expire within core.SessionTTL.
ALTER TABLE sessions ADD COLUMN legacy_cookie BOOLEAN NOT NULL DEFAULT 0;
UPDATE sessions SET legacy_cookie = 1;
