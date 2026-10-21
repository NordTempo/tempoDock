-- Archived projects are hidden from lists by default; their services are
-- archived (volumes and history kept).
ALTER TABLE projects ADD COLUMN archived_at DATETIME;
