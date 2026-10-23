import type { Session } from "../../data/types";

// Who may create what, as core checks it. The UI hides what the caller
// can't do; the server still decides.

/** Projects: deploy or admin, not limited to a project (requireUnlimited). */
export const canCreateProject = (s: Session | null) =>
  !!s && s.scope !== "read" && !s.project;

/** Environments, new or cloned: deploy on the whole project (checkProject). */
export const canCreateEnvironment = (s: Session | null, project: string) =>
  !!s &&
  s.scope !== "read" &&
  (!s.project || s.project === project) &&
  !s.environment;
