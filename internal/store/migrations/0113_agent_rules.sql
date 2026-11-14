-- The organization's agent rules (ADR 0080, phase 6c): per area role and
-- environment kind, the most any of its agents (a person's `dock login`
-- or one of its tokens) may use, and whether their changes there wait
-- for an approver. No row: no rule. tokens.rules_bypass exempts one org
-- agent (a trusted CI) from them.
CREATE TABLE agent_rules (
    org_id INTEGER NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    area   TEXT NOT NULL,
    kind   INTEGER NOT NULL CHECK (kind IN (1, 2, 4)),
    level  TEXT NOT NULL,
    ask    BOOLEAN NOT NULL DEFAULT 0,
    PRIMARY KEY (org_id, area, kind)
) WITHOUT ROWID;

ALTER TABLE tokens ADD COLUMN rules_bypass BOOLEAN NOT NULL DEFAULT 0;
