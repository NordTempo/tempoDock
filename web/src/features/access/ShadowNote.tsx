import { useSession } from "../../data/queries";

/**
 * One line while the platform's permission model only watches (ADR 0080's
 * shadow mode): roles still decide, so groups and grants change nothing
 * yet.
 */
export function ShadowNote() {
  if (useSession()?.access_model !== "shadow") return null;
  return (
    <p className="text-warn-text">
      Roles still decide here: groups and grants take over when the platform's
      operator switches the permission model to enforce.
    </p>
  );
}
