-- Protected environments (ADR 0080, phase 7): a lock can hold people's
-- changes too, not only agents'; then whoever may approve there still
-- changes it, and everyone else's change waits as an access request.
-- user_id is the person who asked, replayed as them once approved.
ALTER TABLE environments ADD COLUMN lock_people BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE access_requests ADD COLUMN user_id INTEGER REFERENCES users (id) ON DELETE CASCADE;
CREATE INDEX access_requests_user ON access_requests (user_id, id) WHERE user_id IS NOT NULL;
