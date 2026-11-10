import type { DataPanelView } from "./DataPanel";
import type { DataInfo } from "./types";

/** A database engine the panel supports (ADR 0072). */
export type DataEngine = DataInfo["engine"];

/**
 * What changes in the panel per engine: its tabs, in order, and the SQL
 * dialect the editor highlights and completes with. Everything else is
 * the same panel.
 */
export const engines: Record<
  DataEngine,
  {
    tabs: readonly DataPanelView["tab"][];
    dialect: DataEngine;
    /** Tab names that differ from the panel's (Redis' SQL is Commands). */
    labels?: Partial<Record<DataPanelView["tab"], string>>;
  }
> = {
  postgres: {
    tabs: ["tables", "sql", "schema", "changes", "health"],
    dialect: "postgres",
  },
  mysql: {
    tabs: ["tables", "sql", "schema", "changes", "health"],
    dialect: "mysql",
  },
  redis: {
    tabs: ["keys", "sql"],
    dialect: "redis",
    labels: { sql: "Commands" },
  },
};
