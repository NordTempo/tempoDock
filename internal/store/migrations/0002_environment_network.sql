-- Docker resources are named by DB ID (dock_p<project>_e<env>...), because
-- names built from project and environment names collide: project "a" env
-- "b-c" and project "a-b" env "c" both gave network "dock-a-b-c".
--
-- network keeps the old name for environments that already have containers
-- on it, so their services keep reaching each other while they are redeployed
-- one by one. Empty means the ID-based name. Where two environments already
-- shared an old name, only the oldest keeps it.
ALTER TABLE environments ADD COLUMN network TEXT NOT NULL DEFAULT '';

UPDATE environments SET network = (
    SELECT 'dock-' || projects.name || '-' || environments.name FROM projects WHERE projects.id = environments.project_id
)
WHERE EXISTS (
    SELECT 1 FROM services JOIN releases ON releases.service_id = services.id
    WHERE services.environment_id = environments.id AND releases.container_id != ''
)
AND id IN (
    SELECT min(e.id) FROM environments e JOIN projects p ON p.id = e.project_id
    WHERE EXISTS (
        SELECT 1 FROM services JOIN releases ON releases.service_id = services.id
        WHERE services.environment_id = e.id AND releases.container_id != ''
    )
    GROUP BY 'dock-' || p.name || '-' || e.name
);

CREATE UNIQUE INDEX environments_network ON environments (network) WHERE network != '';
