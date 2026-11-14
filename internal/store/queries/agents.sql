-- ListAgentSettings is what a person's agent may use in an org: the
-- person's setting (token_id NULL) and the agent's own narrowing, read
-- with every request of a `dock login` token.
-- name: ListAgentSettings :many
SELECT * FROM agent_settings
WHERE user_id = +sqlc.arg(user_id) AND org_id = +sqlc.arg(org_id)
  AND (token_id IS NULL OR token_id = +sqlc.arg(token_id));

-- name: DeleteAgentSettings :exec
DELETE FROM agent_settings
WHERE user_id = +sqlc.arg(user_id) AND org_id = +sqlc.arg(org_id) AND coalesce(token_id, 0) = +sqlc.arg(token_id);

-- name: AddAgentSetting :exec
INSERT INTO agent_settings (user_id, org_id, token_id, area, kind, level, ask) VALUES (?, ?, ?, ?, ?, ?, ?);
