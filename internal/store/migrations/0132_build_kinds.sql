-- Which environment kinds a builder builds images for, apart from where
-- they are placed (ADR 0014 amendment). A production build gets the
-- environment's variables, so it runs only where an operator said it may:
-- a homelab that hosts only staging can still build production's images.
-- Until now a builder built production only when it accepted production.
ALTER TABLE servers ADD COLUMN builds_production BOOLEAN NOT NULL DEFAULT 0;
ALTER TABLE servers ADD COLUMN builds_non_production BOOLEAN NOT NULL DEFAULT 1;
UPDATE servers SET builds_production = accepts_production;
