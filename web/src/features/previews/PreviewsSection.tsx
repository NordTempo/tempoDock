import { useQuery } from "@tanstack/react-query";
import { lazy, type ReactNode, Suspense, useState } from "react";
import { useSession } from "../../data/queries";
import type { Session } from "../../data/types";
import { ago } from "../../ui/format";
import { Act, Confirm, ErrorText, SectionHead } from "../../ui/kit";
import { previewQueries, previewsCli, useDisablePreviews } from "./data";

const PreviewsForm = lazy(() => import("./PreviewsForm"));

/** Changing previews needs admin on the whole project, as core checks it. */
const canManage = (s: Session | null, project: string) =>
  s?.scope === "admin" &&
  !s.environment &&
  (!s.project || s.project === project);

/**
 * The project's Previews settings (ADR 0056): whether pull requests get a
 * preview environment, cloned from which base, how many at once, which
 * literal variables they copy and whether forks count. Admins edit them;
 * everyone else sees them read-only.
 */
export function PreviewsSection({ project }: { project: string }) {
  const settings = useQuery(previewQueries.settings(project));
  const admin = canManage(useSession(), project);
  const disable = useDisablePreviews();
  const [editing, setEditing] = useState(false);
  const [disabling, setDisabling] = useState(false);
  const s = settings.data;
  return (
    <section aria-labelledby="previews" className="flex flex-col">
      <SectionHead
        id="previews"
        right={
          admin &&
          s?.enabled &&
          !editing && (
            <>
              <button
                type="button"
                className="text-xs text-mut hover:text-fg"
                title={previewsCli(`enable ${s.base ?? "<base>"}`, project)}
                onClick={() => setEditing(true)}
              >
                Edit
              </button>
              <button
                type="button"
                className="text-xs text-mut hover:text-fail"
                title={previewsCli("disable", project)}
                onClick={() => setDisabling(true)}
              >
                Turn off
              </button>
            </>
          )
        }
      >
        Previews
      </SectionHead>
      {settings.error ? (
        <ErrorText error={settings.error} />
      ) : !s ? null : editing ? (
        <Suspense fallback={null}>
          <PreviewsForm
            project={project}
            settings={s}
            onDone={() => setEditing(false)}
          />
        </Suspense>
      ) : (
        <>
          {disabling && (
            <div className="mb-2.5">
              <Confirm
                danger
                action="Turn previews off"
                cli={previewsCli("disable", project)}
                pending={disable.isPending}
                error={disable.error}
                onConfirm={() =>
                  disable.mutate(project, {
                    onSuccess: () => setDisabling(false),
                  })
                }
                onCancel={() => setDisabling(false)}
              >
                New pull requests get no preview. Open previews stay until their
                pull requests close.
              </Confirm>
            </div>
          )}
          {s.enabled ? (
            <dl className="props">
              <Item label="Base">
                <span className="font-mono">{s.base}</span>
              </Item>
              <Item label="Open">
                {s.previews.length} of {s.max_previews}
              </Item>
              <Item label="Copied variables">
                {s.copy_variables.length > 0 ? (
                  <span className="font-mono">
                    {s.copy_variables.join(", ")}
                  </span>
                ) : (
                  <span className="text-mut">none</span>
                )}
              </Item>
              <Item label="Forks">
                {s.allow_forks ? (
                  <span className="text-warn">previewed</span>
                ) : (
                  <span className="text-mut">not previewed</span>
                )}
              </Item>
              {s.updated_at && (
                <Item label="Changed">
                  {ago(s.updated_at)}
                  {s.updated_by && ` by ${s.updated_by}`}
                </Item>
              )}
            </dl>
          ) : (
            <p className="flex flex-wrap items-center gap-2.5 text-mut">
              Off.
              {admin && (
                <Act
                  small
                  cli={previewsCli("enable <base>", project)}
                  onClick={() => setEditing(true)}
                >
                  Turn on
                </Act>
              )}
            </p>
          )}
        </>
      )}
      <p className="mt-2 text-xs text-mut">
        Each pull request to the branch the base environment deploys from gets
        its own environment, pr-&lt;number&gt;. References and database
        credentials are set in it; other values are copied only if you list
        them.
      </p>
    </section>
  );
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{children}</dd>
    </>
  );
}
