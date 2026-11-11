import type { DataPanelView } from "./DataPanel";
import type { DataInfo, DataInsightView } from "./types";

/** A database engine the panel supports (ADR 0072). */
export type DataEngine = DataInfo["engine"];

/**
 * What changes in the panel per engine: its tabs, in order, the SQL
 * dialect the editor highlights and completes with, and its Health views
 * (the API's InsightViewsOf). Everything else is the same panel.
 */
export const engines: Record<
  DataEngine,
  {
    tabs: readonly DataPanelView["tab"][];
    dialect: DataEngine;
    /** Tab names that differ from the panel's (Redis' SQL is Commands). */
    labels?: Partial<Record<DataPanelView["tab"], string>>;
    health: readonly DataInsightView[];
    healthLabels?: Partial<Record<DataInsightView, string>>;
  }
> = {
  postgres: {
    tabs: ["tables", "sql", "schema", "changes", "health"],
    dialect: "postgres",
    health: ["overview", "queries", "sessions", "tables", "indexes"],
  },
  mysql: {
    tabs: ["tables", "sql", "schema", "changes", "health"],
    dialect: "mysql",
    health: ["overview", "queries", "sessions", "tables", "indexes"],
  },
  redis: {
    tabs: ["keys", "sql", "changes", "health"],
    dialect: "redis",
    labels: { sql: "Commands" },
    health: ["overview", "queries", "sessions", "keys"],
    healthLabels: { queries: "Commands", sessions: "Clients" },
  },
};
