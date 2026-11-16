import { useQuery } from "@tanstack/react-query";
import { api, body, list } from "../../api/client";
import { useAction } from "../../data/mutations";
import { keys, useSession } from "../../data/queries";
import { CliHint } from "../../ui/CliHint";
import { Act, ErrorText, SectionHead } from "../../ui/kit";

// The permission model of ADR 0080: whether groups and grants decide
// people's checks (enforce) or only watch beside their roles (shadow),
// and where the model answered differently from today's checks since the
// daemon started. Read when the page opens; nothing polls.

type Mode = "shadow" | "enforce";
const modelKey = ["console", "access-model"];

/** The mode with its switch, and the disagreements when there are any. */
export function AccessDiff() {
  const { data } = useQuery({
    queryKey: ["console", "access-diff"],
    queryFn: () => list(api.GET("/api/v1/console/access-diff")),
  });
  const model = useQuery({
    queryKey: modelKey,
    queryFn: () => body(api.GET("/api/v1/console/access-model")),
  });
  const operator = useSession()?.platform === "operator";
  const set = useAction(
    (mode: Mode) =>
      body(api.PUT("/api/v1/console/access-model", { body: { mode } })),
    () => [modelKey, keys.session],
    (mode) =>
      mode === "enforce"
        ? "Groups and grants decide now"
        : "Roles decide again; the model watches",
  );
  if (!data || !model.data) return null;
  const mode = model.data.mode;
  const next: Mode = mode === "enforce" ? "shadow" : "enforce";
  return (
    <section aria-labelledby="access-diff" className="flex flex-col gap-1.5">
      <SectionHead id="access-diff">
        <CliHint cli="dock platform access-diff" className="">
          <span>Permission model</span>
        </CliHint>
      </SectionHead>
      <div className="flex flex-wrap items-center gap-2.5">
        <span>
          {mode === "enforce"
            ? "Enforce: groups and grants decide people's checks."
            : "Shadow: roles decide; the model watches beside them."}
        </span>
        <Act
          small
          cli={`dock platform access-model ${next}`}
          disabled={!operator || set.isPending}
          reason={operator ? undefined : "operators switch it"}
          onClick={() => set.mutate(next)}
        >
          {next === "enforce" ? "Enforce" : "Back to shadow"}
        </Act>
      </div>
      {set.error && <ErrorText error={set.error} />}
      {data.length === 0 ? (
        <p className="text-mut">
          No check has come out differently from the roles since Dock last
          started.
        </p>
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
