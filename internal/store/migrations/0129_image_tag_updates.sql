-- Newer images for images deployed by tag (ADR 0074, amendment
-- 2026-11-25). base_latest: what the base tag pointed at at the last daily
-- check, so a newer image can be deployed by digest (Update) and a new
-- template database starts on it.
ALTER TABLE image_scans ADD COLUMN base_latest TEXT NOT NULL DEFAULT '';

-- An image deployed as repo:tag@digest (the database templates) is its
-- own base: the daily check follows its tag. Existing scans get it from
-- their pull reference when its last path segment has a tag; new ones
-- get it when a release is attached (syncScans).
UPDATE image_scans
SET base_image = substr(pull_ref, 1, instr(pull_ref, '@') - 1),
    base_digest = substr(pull_ref, instr(pull_ref, '@') + 1)
WHERE base_image = '' AND local_ref = '' AND instr(pull_ref, '@sha256:') > 0
  AND instr(
    -- The reference's last path segment (after the last '/'), before '@'.
    substr(substr(pull_ref, 1, instr(pull_ref, '@') - 1),
           length(rtrim(substr(pull_ref, 1, instr(pull_ref, '@') - 1),
                        replace(substr(pull_ref, 1, instr(pull_ref, '@') - 1), '/', ''))) + 1),
    ':') > 0;
