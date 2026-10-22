import type { PolicyRule } from "../../../src/features/data/policy";
import type { DataSubject } from "../../../src/features/data/types";
import type { PolicyScope } from "../api/types";

// tempoBase's side of the data access policy (ADR 0024 section 2): its
// words for the subjects and scopes, and its built-in defaults.

/** The policy's subjects in tempoBase's words (the wire keeps Dock's). */
export const subjectNames: Record<DataSubject, string> = {
  "role:read": "viewers",
  "role:deploy": "editors",
  tokens: "agents",
};

/**
 * The built-in defaults, the same for both kinds: a viewer is there to
 * view, editors are trusted people, agents get nothing until granted.
 */
export function builtIn(
  subject: DataSubject,
): Pick<PolicyRule, "level" | "sensitive"> {
  const level =
    subject === "role:read"
      ? "read"
      : subject === "role:deploy"
        ? "write"
        : "none";
  return { level, sensitive: "masked" };
}

/** Where an effective rule comes from, after "from". */
export function fromText(from: PolicyScope | undefined, connection?: string) {
  switch (from) {
    case "connection":
      return `connection ${connection ?? ""}`.trim();
    case "default":
      return "the default policy";
    default:
      return "the built-in defaults";
  }
}
