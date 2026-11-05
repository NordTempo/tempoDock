import { useSuspenseQuery } from "@tanstack/react-query";
import { useState } from "react";
import type { components } from "../../api/schema";
import { useIsAdmin, useRevealedQuery } from "../../data/queries";
import { since } from "../../ui/format";
import { Act, Button, ErrorText, Page, PageBar, Panel } from "../../ui/kit";
import {
  orgQueries,
  useDeleteOrgVariable,
  useOrgName,
  useRedeployOrgVariable,
  useSetOrgVariable,
} from "./data";

type Use = components["schemas"]["VariableUse"];

// biome-ignore lint/suspicious/noTemplateCurlyInString: the reference syntax, shown as is
const orgRef = "${{org.KEY}}";
type Rollout = components["schemas"]["OrgVariableRollout"];

/** "shop/production/web" or "blog/staging (shared)". */
const useName = (u: Use) =>
  `${u.project}/${u.environment}${u.service ? `/${u.service}` : " (shared)"}`;

/**
 * The organization's variables (`dock org env`): shared by every project,
 * used where a variable's value says ${{org.KEY}}, never added by
 * themselves. Admins set and reveal them; everyone sees where each is used.
 */
export function VariablesPage() {
  const admin = useIsAdmin();
  const [reveal, setReveal] = useState(false);
  const masked = useSuspenseQuery(orgQueries.variables(false)).data;
  // Values load beside the masked list, so a step-up (ADR 0057) or its
  // refusal never blanks the page.
  const values = useRevealedQuery(orgQueries.variables(true), reveal && admin);
  const vars = (reveal && values.data) || masked;
  // The variable changed last, until its users are redeployed or it's
  // dismissed.
  const [changed, setChanged] = useState<string>();
  const changedVar = vars.find((v) => v.key === changed);
  return (
    <>
      <PageBar
        parent={useOrgName()}
        title="Variables"
        after={
          <span className="text-xs text-faint">
            shared by every project, used only as{" "}
            <span className="font-mono">{orgRef}</span>
          </span>
        }
        actions={
          admin && (
            <Act
              cli="dock org env list --reveal"
              onClick={() => setReveal(!reveal)}
            >
              {reveal ? "Hide values" : "Reveal values"}
            </Act>
          )
        }
      />
      <Page width="max-w-[900px]" gap="gap-5">
        {changedVar?.used_by && changedVar.used_by.length > 0 && (
          <Changed
            name={changedVar.key}
            uses={changedVar.used_by}
            onDone={() => setChanged(undefined)}
          />
        )}
        {reveal && <ErrorText error={values.error} />}
        {vars.length === 0 ? (
          <p className="text-mut">No organization variables yet.</p>
        ) : (
          <Panel>
            <table>
              <thead>
                <tr>
                  <th>Name</th>
                  <th>Value</th>
                  <th>Used by</th>
                  <th>Updated</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {vars.map((v) => (
                  <VariableRow
                    key={v.key}
                    name={v.key}
                    value={v.value}
                    uses={v.used_by ?? []}
                    updated={v.updated_at}
                    admin={admin}
                    onChanged={() => setChanged(v.key)}
                  />
                ))}
              </tbody>
            </table>
          </Panel>
        )}
        {admin && <AddVariable onAdded={setChanged} />}
      </Page>
    </>
  );
}

/** The banner after a change: who uses it, and redeploying them. */
function Changed({
  name,
  uses,
  onDone,
}: {
  name: string;
  uses: Use[];
  onDone: () => void;
}) {
  const redeploy = useRedeployOrgVariable();
  const [result, setResult] = useState<Rollout>();
  return (
    <section
      aria-label={`${name} changed`}
      className="flex flex-col gap-2 rounded-panel border border-warn-edge bg-stage-bg px-3.5 py-3"
    >
      <p>
        <b className="font-semibold text-warn-text">{name} changed.</b>{" "}
        <span className="text-mut">
          {uses.length === 1
            ? "1 variable uses"
            : `${uses.length} variables use`}{" "}
          it; services get it on their next release.
        </span>
      </p>
      <p className="font-mono text-2xs text-faint">
        {uses.map(useName).join(" · ")}
      </p>
      {result ? (
        <p className="text-xs">
          {(result.started ?? []).map((r) => (
            <span key={`${r.project}/${r.environment}/${r.service}`}>
              <span className="text-ok">✓</span> {r.project}/{r.environment}/
              {r.service} release {r.number}{" "}
            </span>
          ))}
          {(result.skipped ?? []).map((r) => (
            <span
              key={`${r.project}/${r.environment}/${r.service}`}
              className="text-mut"
            >
              · {r.project}/{r.environment}/{r.service}: {r.reason}{" "}
            </span>
          ))}
        </p>
      ) : null}
      <div className="flex flex-wrap items-center gap-2">
        {!result && (
          <Act
            small
            primary
            cli={`dock org env redeploy ${name}`}
            disabled={redeploy.isPending}
            onClick={() => redeploy.mutate(name, { onSuccess: setResult })}
          >
            Redeploy now
          </Act>
        )}
        <Button small onClick={onDone}>
          {result ? "Done" : "Later"}
        </Button>
        <ErrorText error={redeploy.error} />
      </div>
    </section>
  );
}

function VariableRow({
  name,
  value,
  uses,
  updated,
  admin,
  onChanged,
}: {
  name: string;
  value?: string;
  uses: Use[];
  updated: string;
  admin: boolean;
  onChanged: () => void;
}) {
  const set = useSetOrgVariable();
  const remove = useDeleteOrgVariable();
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  return (
    <tr>
      <td className="py-2 font-mono">{name}</td>
      <td className="max-w-[260px] py-1.5">
        {editing ? (
          <form
            className="flex items-center gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              set.mutate(
                { key: name, value: draft },
                {
                  onSuccess: () => {
                    setEditing(false);
                    onChanged();
                  },
                },
              );
            }}
          >
            <input
              aria-label={`New value of ${name}`}
              value={draft}
              onChange={(e) => setDraft(e.target.value)}
              className="field min-w-0 flex-1 py-[3px] font-mono"
            />
            <Act small primary type="submit" cli={`dock org env set ${name}=…`}>
              Save
            </Act>
            <Button small onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </form>
        ) : (
          <span className="block truncate font-mono text-faint">
            {value ?? "••••••••"}
          </span>
        )}
        <ErrorText error={set.error} />
      </td>
      <td className="py-2" title={uses.map(useName).join("\n")}>
        {uses.length === 0 ? (
          <span className="text-faint">not used</span>
        ) : (
          <span className="text-sec">
            {uses.length === 1 && uses[0]
              ? useName(uses[0])
              : `${uses.length} variables`}
          </span>
        )}
      </td>
      <td className="py-2 text-faint">{since(updated)}</td>
      <td className="w-full py-1.5 text-right whitespace-nowrap">
        {admin && !editing && (
          <span className="inline-flex gap-2">
            <Button
              small
              onClick={() => {
                setDraft(value ?? "");
                setEditing(true);
              }}
            >
              Edit
            </Button>
            <Act
              small
              cli={`dock org env unset ${name}`}
              disabled={uses.length > 0 || remove.isPending}
              reason={
                uses.length > 0 ? "Remove the references first" : undefined
              }
              onClick={() => remove.mutate(name)}
            >
              Remove
            </Act>
          </span>
        )}
        <ErrorText error={remove.error} />
      </td>
    </tr>
  );
}

function AddVariable({ onAdded }: { onAdded: (name: string) => void }) {
  const set = useSetOrgVariable();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  return (
    <form
      aria-label="Add a variable"
      className="flex flex-wrap items-center gap-2.5"
      onSubmit={(e) => {
        e.preventDefault();
        set.mutate(
          { key: name, value },
          {
            onSuccess: (v) => {
              setName("");
              setValue("");
              onAdded(v.key);
            },
          },
        );
      }}
    >
      <input
        aria-label="Name"
        placeholder="NAME"
        value={name}
        onChange={(e) => setName(e.target.value.toUpperCase())}
        spellCheck={false}
        className="field w-48 py-[7px] font-mono"
      />
      <input
        aria-label="Value"
        placeholder="value"
        value={value}
        onChange={(e) => setValue(e.target.value)}
        spellCheck={false}
        className="field min-w-0 flex-[1_1_220px] py-[7px] font-mono"
      />
      <Act
        type="submit"
        cli={`dock org env set ${name || "NAME"}=…`}
        disabled={!name || set.isPending}
      >
        Add
      </Act>
      <ErrorText error={set.error} />
    </form>
  );
}
