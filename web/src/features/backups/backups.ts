import type { ServiceBackups } from "../../data/types";

type Kind = ServiceBackups["kind"];

/** How a kind of backup reads the data. */
export const backupHow: Record<Kind, string> = {
  postgres: "pg_dump -Fc, streamed to restic",
  redis: "Redis RDB snapshot, to restic",
  volume: "file-level volume backup with restic",
};

/** The variable other services reference for a restored database. */
export const exportOf = (kind: Kind) =>
  kind === "redis" ? "REDIS_URL" : "DATABASE_URL";

/** "03:00" */
export const hourLabel = (h: number) => `${String(h).padStart(2, "0")}:00`;
