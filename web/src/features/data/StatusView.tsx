import type { ReactNode } from "react";
import { Act, ErrorText } from "../../ui/kit";
import { useCli, useDataSetup, useDataSource } from "./api";
import type { DataInfo, DataPermission } from "./types";

/**
 * Why the panel can't show data: not set up (admins get the setup
 * button), unreachable, not running, unsupported, or no access. Renders
 * nothing when the database is ready and the caller has access.
 */
export function StatusView({ info }: { info: DataInfo }) {
  const { roles, words } = useDataSource();
  const cli = useCli();
  const setupCli = cli("dock db setup");
  const setup = useDataSetup();
  const { access } = info;

  switch (info.status) {
    case "setup_needed":
      return (
        <State glyph="▲" tone="text-warn" title="Not set up">
          <p className="max-w-[640px] text-mut">
            {words.product} reads this database through its own least-privilege
            roles, {roles.reader} and {roles.writer}. Setup creates them with{" "}
            {words.owner}; their passwords stay in {words.product} and never
            reach the app or the browser.
          </p>
          {access.manage ? (
            <div className="flex items-start gap-3">
              <Act
                primary
                cli={setupCli}
                disabled={setup.isPending}
                onClick={() => setup.mutate()}
              >
                {setup.isPending
                  ? "Setting up data access"
                  : "Set up data access"}
              </Act>
              <span className="self-center">
                <ErrorText error={setup.error} />
              </span>
            </div>
          ) : setupCli ? (
            <p>
              An admin can run <code className="font-mono">{setupCli}</code>.
            </p>
          ) : (
            <p>An admin can set it up.</p>
          )}
        </State>
      );
    case "unreachable":
      return (
        <State glyph="✕" tone="text-fail" title="Can't reach the database">
          <p className="max-w-[640px] text-mut">
            {info.reason ?? words.unreachable}
          </p>
        </State>
      );
    case "not_running":
      return (
        <State glyph="○" tone="text-mut" title="Not running">
          <p className="text-mut">{words.notRunning}</p>
        </State>
      );
    case "unsupported":
      return (
        <State glyph="▲" tone="text-warn" title="Unsupported version">
          <p className="text-mut">
            {info.reason ?? `Postgres ${info.server_version} is too old.`} The
            data panel needs Postgres 14 or later.
          </p>
        </State>
      );
    case "ready":
      if (access.level !== "none") return null;
      return (
        <State glyph="○" tone="text-mut" title="No data access">
          <p className="text-mut">
            An admin can grant it in the data access policy.
          </p>
          <Why access={access} />
        </State>
      );
  }
}

/** The steps that decided the caller's access, as the API explains them. */
export function Why({ access }: { access: DataPermission }) {
  const steps = access.why ?? [];
  if (!steps.length) return null;
  return (
    <ul className="font-mono text-xs text-mut">
      {steps.map((s) => (
        <li key={`${s.source}${s.detail}`}>
          {s.source}: {s.detail}
        </li>
      ))}
    </ul>
  );
}

function State({
  glyph,
  tone,
  title,
  children,
}: {
  glyph: string;
  tone: string;
  title: string;
  children: ReactNode;
}) {
  return (
    <section className="flex flex-col gap-3 py-6">
      <h2 className="text-lg font-semibold">
        <span className={tone} aria-hidden>
          {glyph}
        </span>{" "}
        {title}
      </h2>
      {children}
    </section>
  );
}
