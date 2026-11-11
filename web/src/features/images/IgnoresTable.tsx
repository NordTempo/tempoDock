import { useSession } from "../../data/queries";
import { day } from "../../ui/format";
import { Act, ErrorText, Panel } from "../../ui/kit";
import type { VulnIgnore } from "./data";
import { mayIgnore, useRemoveIgnore, where } from "./ignores";

// Ignores as a table, for the org's Ignores page (mock L1) and an
// image's Ignored view (L2): what, where, why, until when and by whom,
// with Remove for those who may.

/** "Feb 8", "always" or "expired Nov 2". */
function until(ig: VulnIgnore) {
  if (!ig.expires) return "always";
  return ig.expired ? `expired ${day(ig.expires)}` : day(ig.expires);
}

export function IgnoresTable({
  ignores,
  versions,
}: {
  ignores: VulnIgnore[];
  /** The installed version of each package, on an image's page. */
  versions?: Map<string, string>;
}) {
  const session = useSession();
  const remove = useRemoveIgnore();
  return (
    <>
      <ErrorText error={remove.error} />
      <Panel>
        <table>
          <thead>
            <tr>
              <th>Vulnerability</th>
              <th>Package</th>
              <th>Where</th>
              <th>Why</th>
              <th>Until</th>
              <th>By</th>
              <th>
                <span className="sr-only">Remove</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {ignores.map((ig) => (
              <tr key={ig.id} className={ig.expired ? "text-faint" : ""}>
                <td className="font-mono">{ig.vulnerability}</td>
                <td className="font-mono">
                  {ig.package ? (
                    <>
                      {ig.package}
                      {versions?.get(ig.package) && (
                        <span className="text-mut">
                          {" "}
                          {versions.get(ig.package)}
                        </span>
                      )}
                    </>
                  ) : (
                    <span className="font-sans text-mut">any</span>
                  )}
                </td>
                <td>{where(ig)}</td>
                <td className="w-full min-w-[220px] whitespace-normal">
                  {ig.reason}
                </td>
                <td className="whitespace-nowrap text-mut">{until(ig)}</td>
                <td className="whitespace-nowrap text-mut">{ig.by}</td>
                <td className="py-1.5 text-right">
                  {mayIgnore(session, ig) && (
                    <Act
                      small
                      cli={`dock vulns unignore ${ig.id}`}
                      disabled={remove.isPending}
                      title="Its findings count again"
                      onClick={() => remove.mutate(ig)}
                    >
                      Remove
                    </Act>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </Panel>
    </>
  );
}
