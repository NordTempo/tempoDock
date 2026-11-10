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
  { tabs: readonly DataPanelView["tab"][]; dialect: DataEngine }
> = {
  postgres: {
    tabs: ["tables", "sql", "schema", "changes", "health"],
    dialect: "postgres",
  },
  mysql: {
    tabs: ["tables", "sql", "schema", "changes", "health"],
    dialect: "mysql",
  },
};
