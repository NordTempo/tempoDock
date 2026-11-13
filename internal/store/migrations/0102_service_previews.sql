-- Previews belong to services (ADR 0077, amending ADR 0056). A project's
-- preview_settings row splits in two:
--
-- service_previews: whether pull requests into a service's branch build
-- it in a preview (build, with allow_forks), which of its own variables'
-- literal values previews copy (copy_variables, a JSON list of KEYs), and
-- whether its volumes start from its newest snapshot (seed).
--
-- environment_previews: the two numbers about a preview as a whole, for
-- previews cloned from an environment (max_previews open at once, and
-- sleep_after_minutes), and its shared variables previews copy
-- (copy_shared). No row means the defaults.
--
-- Rows move so the same pull request makes the same preview: every live
-- service of a base with a git connection builds pull requests (as each
-- one connected to the pull request's repository and branch did), with
-- the project's allow_forks; web.KEY copies go to web, shared.KEY ones
-- and the numbers to the base.
CREATE TABLE service_previews (
    service_id     INTEGER PRIMARY KEY REFERENCES services (id),
    build          BOOLEAN NOT NULL DEFAULT 0,
    allow_forks    BOOLEAN NOT NULL DEFAULT 0,
    copy_variables TEXT NOT NULL DEFAULT '[]',
    seed           BOOLEAN NOT NULL DEFAULT 0,
    updated_by     TEXT NOT NULL,
    updated_at     DATETIME NOT NULL
);

CREATE TABLE environment_previews (
    environment_id      INTEGER PRIMARY KEY REFERENCES environments (id),
    max_previews        INTEGER NOT NULL,
    sleep_after_minutes INTEGER NOT NULL,
    copy_shared         TEXT NOT NULL DEFAULT '[]',
    updated_by          TEXT NOT NULL,
    updated_at          DATETIME NOT NULL
);

INSERT INTO environment_previews (environment_id, max_previews, sleep_after_minutes, copy_shared, updated_by, updated_at)
SELECT base_environment_id, max_previews, sleep_after_minutes,
       (SELECT json_group_array(substr(value, 8)) FROM json_each(preview_settings.copy_variables)
        WHERE value LIKE 'shared.%'),
       updated_by, updated_at
FROM preview_settings;

INSERT INTO service_previews (service_id, build, allow_forks, copy_variables, seed, updated_by, updated_at)
SELECT services.id,
       EXISTS (SELECT 1 FROM git_connections WHERE git_connections.service_id = services.id),
       preview_settings.allow_forks
         AND EXISTS (SELECT 1 FROM git_connections WHERE git_connections.service_id = services.id),
       (SELECT json_group_array(substr(value, length(services.name) + 2)) FROM json_each(preview_settings.copy_variables)
        WHERE substr(value, 1, length(services.name) + 1) = services.name || '.'),
       EXISTS (SELECT 1 FROM json_each(preview_settings.seed_from_snapshot) WHERE value = services.name),
       preview_settings.updated_by, preview_settings.updated_at
FROM preview_settings
JOIN services ON services.environment_id = preview_settings.base_environment_id AND services.archived_at IS NULL
WHERE EXISTS (SELECT 1 FROM git_connections WHERE git_connections.service_id = services.id)
   OR EXISTS (SELECT 1 FROM json_each(preview_settings.copy_variables)
              WHERE substr(value, 1, length(services.name) + 1) = services.name || '.')
   OR EXISTS (SELECT 1 FROM json_each(preview_settings.seed_from_snapshot) WHERE value = services.name);

DROP TABLE preview_settings;

-- Open previews are counted per base environment now.
CREATE INDEX previews_base_open ON previews (base_environment_id) WHERE closed_at IS NULL;
