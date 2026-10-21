-- carried is the JSON of the staged changes a release made live (variables
-- with their previous and applied sealed values, settings, volumes). If
-- the release fails they are staged again, so the live values stay those
-- of the active release. Empty: it carried none.
ALTER TABLE releases ADD COLUMN carried TEXT NOT NULL DEFAULT '';

-- Why the environment's staged changes are back: the last release that
-- carried them failed. Cleared by the next apply or discard.
ALTER TABLE environments ADD COLUMN apply_error TEXT NOT NULL DEFAULT '';
