import { useQuery } from "@tanstack/react-query";
import { api, list } from "../../api/client";
import { CliHint } from "../../ui/CliHint";

// The permission model of ADR 0080 watching before it decides (phase 3):
// where it answered differently from today's checks since the daemon
// started. Read when the page opens; nothing polls.

/** A line, and the disagreements when there are any. */
export function AccessDiff() {
  const { data } = useQuery({
    queryKey: ["console", "access-diff"],
    queryFn: () => list(api.GET("/api/v1/console/access-diff")),
  });
  if (!data) return null;
  return (
    <section aria-labelledby="access-diff" className="flex flex-col gap-1.5">
      <h2 id="access-diff" className="font-semibold">
        <CliHint cli="dock platform access-diff" className="">
          <span>Permission model</span>
        </CliHint>
      </h2>
      {data.length === 0 ? (
        <p>Agreed with every check since Dock started.</p>
      ) : (
        <ul className="flex flex-col gap-1 font-mono text-xs">
          {data.slice(0, 20).map((d) => (
            <li key={d.check}>
              <span className="text-warn">{d.count}×</span> {d.check}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
