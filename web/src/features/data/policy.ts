import type {
  DataEnvironmentKind,
  DataLevel,
  DataSensitivity,
  DataSubject,
} from "./types";

// The data access policy's vocabulary (ADR 0016 section 4), shared by Dock
// and tempoBase. Each host fetches and stores the policy itself (Dock:
// ./dockPolicy.ts).

export const levels: DataLevel[] = [
  "none",
  "schema",
  "read",
  "propose",
  "write",
];
export const sensitivities: DataSensitivity[] = ["masked", "visible"];
export const subjects: DataSubject[] = ["role:read", "role:deploy", "tokens"];
export const kinds: DataEnvironmentKind[] = ["non_production", "production"];

/** What each level allows, in a few words. */
export const levelNotes: Record<DataLevel, string> = {
  none: "nothing",
  schema: "tables and columns, no rows",
  read: "browse rows, read-only SQL",
  propose: "also draft changes an admin approves",
  write: "also commit their own changes",
};

export const kindNames: Record<DataEnvironmentKind, string> = {
  non_production: "non-production",
  production: "production",
};
