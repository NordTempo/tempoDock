import type { ServiceBackups } from "../../data/types";

type Kind = ServiceBackups["kind"];

/** What a kind of backup keeps, as an outcome; the tools are in the log. */
export const backupWhat: Record<Kind, string> = {
  postgres: "Database dumps",
  redis: "Redis snapshots",
  mysql: "Database dumps",
  volume: "Copies of its volumes' files",
};

/** "03:00" */
export const hourLabel = (h: number) => `${String(h).padStart(2, "0")}:00`;
