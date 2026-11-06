import type { ServiceBackups } from "../../data/types";

type Kind = ServiceBackups["kind"];

/** How a kind of backup reads the data. */
export const backupHow: Record<Kind, string> = {
  postgres: "pg_dump -Fc, streamed to restic",
  redis: "Redis RDB snapshot, to restic",
  volume: "file-level volume backup with restic",
};

/** "03:00" */
export const hourLabel = (h: number) => `${String(h).padStart(2, "0")}:00`;
