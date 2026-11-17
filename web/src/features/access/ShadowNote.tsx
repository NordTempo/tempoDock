import { Link } from "@tanstack/react-router";
import { useSession } from "../../data/queries";

/**
 * One line while the platform's permission model only watches (ADR 0080's
 * shadow mode): roles still decide, so groups and grants change nothing
 * yet. Members read it in plain words; operators, who switch the model,
 * also get the way to where they do.
 */
export function ShadowNote() {
  const session = useSession();
  if (session?.access_model !== "shadow") return null;
  if (!session.platform) {
    return (
      <p className="text-mut">
        For now each member's role decides what they can do; groups and grants
        don't apply yet.
      </p>
    );
  }
  return (
    <p className="text-warn-text">
      Groups and grants don't apply yet: roles decide until you enforce the
      permission model in{" "}
      <Link
        to="/console/users"
        hash="access-diff"
        className="text-warn underline"
      >
        Console › Users
      </Link>
      .
    </p>
  );
}
