import { ago, since } from "../../ui/format";
import { ErrorText, Glyph, MoreItem, MoreMenu, Panel } from "../../ui/kit";
import { toneText } from "../../ui/status";
import { type Alert, type AlertScope, useResolveAlert } from "./data";
import { AlertLink, alertText } from "./source";
import { type AlertGroup, groupTitle } from "./summary";

// Alert rows (ADR 0062): what, where, for how long and the button to its
// fix, with Resolve in each row's "···". The Inbox lists them all; the
// Projects page's Needs you shows the first few.

/** The CLI flag that picks the scope: --platform for the platform's. */
const flag = (scope: AlertScope) => (scope === "platform" ? " --platform" : "");

const severityTone = { critical: "fail", warning: "warn" } as const;

/** Where an alert is: project/environment/service, or a server. */
function where(a: Alert) {
  if (a.project && a.environment && a.service) {
    return `${a.project}/${a.environment}/${a.service}`;
  }
  return a.server ?? a.project ?? "";
}

/** Alert rows: what, where, for how long and the button to the fix. */
export function AlertTable({
  scope,
  groups,
  resolvable,
  brief,
}: {
  scope: AlertScope;
  groups: AlertGroup[];
  resolvable?: boolean;
  /** Each alert's text on one line (the whole of it in its title). */
  brief?: boolean;
}) {
  return (
    <Panel>
      <table>
        <thead className="sr-only">
          <tr>
            <th>Severity</th>
            <th>Alert</th>
            <th>Where</th>
            <th>{resolvable ? "Open for" : "Resolved"}</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody>
          {groups.map((g) => (
            <AlertRow
              key={g.lead.id}
              scope={scope}
              group={g}
              resolvable={resolvable}
              brief={brief}
            />
          ))}
        </tbody>
      </table>
    </Panel>
  );
}

/**
 * One row: the severity's mark, the alert (or how many like it), where,
 * how long, then its fixing link and a ··· with Resolve. A group's link
 * opens all of its errors, and Resolve closes every alert in it.
 */
function AlertRow({
  scope,
  group,
  resolvable,
  brief,
}: {
  scope: AlertScope;
  group: AlertGroup;
  resolvable?: boolean;
  brief?: boolean;
}) {
  const resolve = useResolveAlert();
  const { lead: a, alerts } = group;
  const many = alerts.length > 1;
  const tone = a.resolved_at ? "text-mut" : toneText[severityTone[a.severity]];
  const { text, cli } = alertText(a);
  // A group's link goes to the site's (or service's) errors, not one issue.
  const linked = many
    ? { ...a, target: { ...a.target, error_group: undefined } }
    : a;
  const opened = alerts.reduce(
    (first, x) => (x.opened_at < first ? x.opened_at : first),
    a.opened_at,
  );
  // One at a time; a failure stops there and shows as resolve.error.
  const resolveAll = async () => {
    try {
      for (const x of alerts) await resolve.mutateAsync(x.id);
    } catch {
      return;
    }
  };
  return (
    <tr>
      <td className={`w-0 py-2 whitespace-nowrap ${tone}`} title={a.severity}>
        <Glyph char={a.severity === "critical" ? "✕" : "▲"} />
        <span className="sr-only">{a.severity}</span>
      </td>
      <td className="w-full min-w-[240px] py-2 whitespace-normal">
        {groupTitle(group)}
        {!many && a.count > 1 && (
          <span className="text-mut"> {a.count} times</span>
        )}
        {!many && text && (
          <span
            className={`text-mut ${brief ? "line-clamp-1" : "block"}`}
            title={brief ? text : undefined}
          >
            {text}
          </span>
        )}
        {/* Settling (ADR 0097): the condition cleared, and the alert
            resolves once it has stayed clear. */}
        {a.cleared_at && !a.resolved_at && (
          <span className="block text-ok">
            Clear since {ago(a.cleared_at)}, resolves once it stays clear
          </span>
        )}
      </td>
      <td className="py-2 pr-7 font-mono text-mut">{where(a)}</td>
      <td className="py-2 whitespace-nowrap text-mut">
        {a.resolved_at ? `${since(a.resolved_at)} ago` : since(opened)}
      </td>
      <td className="py-1.5 text-right">
        <span className="inline-flex items-center gap-2 whitespace-nowrap">
          <ErrorText error={resolve.error} />
          <AlertLink
            alert={linked}
            className="btn sm no-underline"
            title={cli}
          />
          {resolvable && (
            <MoreMenu label={`More for: ${groupTitle(group)}`}>
              {(close) => (
                <MoreItem
                  close={close}
                  disabled={resolve.isPending}
                  cli={alerts
                    .map((x) => `dock alerts resolve ${x.id}${flag(scope)}`)
                    .join("; ")}
                  onClick={() => void resolveAll()}
                >
                  {many ? `Resolve all ${alerts.length}` : "Resolve"}
                  <span className="block text-2xs text-faint">
                    Opens again if it's still happening
                  </span>
                </MoreItem>
              )}
            </MoreMenu>
          )}
        </span>
      </td>
    </tr>
  );
}
