import type { StatusComponent, StatusImpact } from "./data";

// Small pieces the status page and incident screens share.

/** What an incident does, in a line: "API partial outage, Jobs degraded". */
export const impactText = (ims: StatusImpact[]) =>
  ims
    .map((im) =>
      im.impact === "maintenance"
        ? im.component
        : `${im.component} ${im.impact.replace("_", " ")}`,
    )
    .join(", ");

/** What a component is built from, as the editor may show it. */
export function Sources({ component }: { component: StatusComponent }) {
  if (component.sources.length === 0)
    return <span className="text-faint">set by hand</span>;
  return (
    <span className="flex flex-col">
      {!component.auto && (
        <span className="text-faint">status set by hand</span>
      )}
      {component.sources.map((s, i) => (
        <span key={`${s.service ?? ""}${s.check ?? ""}${i}`}>
          {s.hidden
            ? "another project"
            : s.check
              ? `check ${s.check}`
              : s.service}
          <span className="text-faint">
            {s.check
              ? ""
              : s.checks
                ? ` · ${s.checks === 1 ? "1 check" : `${s.checks} checks`}`
                : " · health only"}
            {s.problem && ` · ${s.problem}`}
          </span>
        </span>
      ))}
    </span>
  );
}
