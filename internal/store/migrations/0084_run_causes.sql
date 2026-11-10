-- The likely cause of a failed one-off command or job run (ADR 0071,
-- phase 3), from the deploy diagnosis's rules over its exit code and
-- output, kept when it ends so lists need no output. The rule's
-- sentence, never log text; empty when no rule matched.
ALTER TABLE service_runs ADD COLUMN cause TEXT NOT NULL DEFAULT '';
