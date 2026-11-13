import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useMemo, useRef, useState } from "react";
import type { components } from "../../api/schema";
import type { EnvRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Button, DialogFoot, ErrorText, Menu, menuItem } from "../../ui/kit";
import { useGitHubBranches, useGitHubPaths } from "../git/api";
import { RepoPicker } from "../git/PickList";
import { input, invalid, nameProblem, toName } from "./form";
import { useCreateService } from "./mutations";

type Schemas = components["schemas"];
type Repo = Schemas["GitHubRepo"];

/**
 * Creating services from GitHub (plan mock A, with C's multi-select):
 * pick one of the repositories the org's GitHub App reads, then one or
 * more of its directories at a branch. Each becomes a service connected
 * through the App whose branch head deploys at once, as
 * `dock service create <name> --repo <repo> --dir <dir>`. `head` is the
 * dialog's rows above (environment, source).
 */
export function FromGitHub({
  env,
  repos,
  taken,
  head,
  onClose,
}: {
  env: EnvRef;
  repos: readonly Repo[];
  /** The environment's service names, which a new one can't have. */
  taken: readonly string[];
  head: ReactNode;
  onClose: () => void;
}) {
  const [repo, setRepo] = useState<Repo | null>(null);
  return repo ? (
    <PathStep
      env={env}
      repo={repo}
      taken={taken}
      head={head}
      onBack={() => setRepo(null)}
      onClose={onClose}
    />
  ) : (
    <RepoStep repos={repos} head={head} onPick={setRepo} onClose={onClose} />
  );
}

/** Step one: the repositories, newest push first, filtered as typed. */
function RepoStep({
  repos,
  head,
  onPick,
  onClose,
}: {
  repos: readonly Repo[];
  head: ReactNode;
  onPick: (repo: Repo) => void;
  onClose: () => void;
}) {
  const org = useOrg();
  return (
    <div>
      {head}
      <div className="mt-3">
        <RepoPicker repos={repos} onPick={onPick} />
      </div>
      <p className="mt-2 text-faint text-xs">
        Missing one? Give the GitHub App access to it on GitHub, from{" "}
        <Link to="/$org/settings/github" params={{ org }} onClick={onClose}>
          the organization's GitHub settings
        </Link>
        .
      </p>
      <DialogFoot cli="dock github repos">
        <Button onClick={onClose}>Cancel</Button>
      </DialogFoot>
    </div>
  );
}

/** A service to create: the directory it builds and its name. */
type Pick = { dir: string; name: string };

/**
 * A DNS-safe service name for a directory: its last part, or the
 * repository's name at the root, with -2, -3... when taken.
 */
function suggestName(repo: string, dir: string, taken: readonly string[]) {
  const base =
    toName(dir.split("/").at(-1) || repo.split("/").at(-1) || "web")
      .replace(/[^a-z0-9-]/g, "-")
      .replace(/^-+|-+$/g, "")
      .slice(0, 28) || "web";
  let name = base;
  for (let n = 2; taken.includes(name); n++) name = `${base}-${n}`;
  return name;
}

/**
 * Step two: the branch, then the directories that look like an app
 * (ticked ones become services), and one typed directory for any other.
 */
function PathStep({
  env,
  repo,
  taken,
  head,
  onBack,
  onClose,
}: {
  env: EnvRef;
  repo: Repo;
  taken: readonly string[];
  head: ReactNode;
  onBack: () => void;
  onClose: () => void;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const create = useCreateService();
  const [branch, setBranch] = useState(repo.default_branch);
  const branches = useGitHubBranches(repo.full_name);
  const paths = useGitHubPaths(repo.full_name, branch);
  const [picks, setPicks] = useState<Pick[]>([]);
  // A directory typed by hand, for one that doesn't look like an app.
  const [other, setOther] = useState("");
  // Its name, once typed; until then one is suggested from it.
  const [otherName, setOtherName] = useState<string | null>(null);
  const otherDir = other.trim().replace(/^\/+|\/+$/g, "");
  const created = useRef<string[]>([]);

  const toggle = (dir: string) =>
    setPicks((ps) =>
      ps.some((p) => p.dir === dir)
        ? ps.filter((p) => p.dir !== dir)
        : [
            ...ps,
            {
              dir,
              name: suggestName(repo.full_name, dir, [
                ...taken,
                ...ps.map((p) => p.name),
              ]),
            },
          ],
    );
  const all: Pick[] = useMemo(
    () =>
      otherDir && !picks.some((p) => p.dir === otherDir)
        ? [
            ...picks,
            {
              dir: otherDir,
              name:
                otherName ??
                suggestName(repo.full_name, otherDir, [
                  ...taken,
                  ...picks.map((p) => p.name),
                ]),
            },
          ]
        : picks,
    [picks, otherDir, otherName, repo.full_name, taken],
  );
  const problems = all.map(
    (p, i) =>
      nameProblem("service", p.name, [
        ...taken,
        ...all.filter((_, j) => j !== i).map((q) => q.name),
      ]) ?? (p.name ? undefined : "Enter a name."),
  );
  const blocked = all.length === 0 || problems.some(Boolean);
  const suggestions = otherDir
    ? (paths.data?.dirs ?? [])
        .filter((d) => d.startsWith(otherDir) && d !== otherDir)
        .slice(0, 6)
    : [];

  const where = `-e ${env.environment} -p ${env.project}`;
  const cli = (all.length ? all : [{ dir: "<dir>", name: "<name>" }]).map(
    (p) =>
      `dock service create ${p.name} --repo ${repo.full_name}${branch !== repo.default_branch ? ` --branch ${branch}` : ""}${p.dir ? ` --dir ${p.dir}` : ""} ${where}`,
  );

  async function submit() {
    if (blocked) return;
    for (const p of all) {
      if (created.current.includes(p.name)) continue;
      await create.mutateAsync({
        env,
        name: p.name,
        git: { repo: repo.full_name, branch, dir: p.dir },
      });
      created.current.push(p.name);
    }
    const first = created.current[0];
    onClose();
    if (first) {
      await navigate({
        to: "/$org/$project/$environment/$service",
        params: { org, ...env, service: first },
      });
    }
  }

  return (
    <form
      aria-label="Pick directories"
      onSubmit={(e) => {
        e.preventDefault();
        submit().catch(() => {});
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          submit().catch(() => {});
        }
      }}
    >
      {head}
      <div className="mt-3 flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-medium font-mono text-fg">
          {repo.full_name}
        </span>
        <Menu
          label="Branch"
          right
          button={
            <span className="btn sm font-mono">
              {branch} <span aria-hidden>▾</span>
            </span>
          }
        >
          {(close) => (
            <div className="max-h-72 overflow-y-auto">
              {(branches.data?.branches ?? [repo.default_branch]).map((b) => (
                <button
                  key={b}
                  type="button"
                  role="menuitem"
                  aria-current={b === branch ? "page" : undefined}
                  className={`${menuItem} w-full font-mono`}
                  onClick={() => {
                    setBranch(b);
                    close();
                  }}
                >
                  {b}
                  {b === branches.data?.default_branch && (
                    <span className="text-faint">default</span>
                  )}
                </button>
              ))}
            </div>
          )}
        </Menu>
      </div>
      <fieldset
        aria-label="Directories"
        className="mt-2 max-h-72 overflow-y-auto border-line border-t"
      >
        {paths.error && (
          <p className="px-2 py-2">
            <ErrorText error={paths.error} />
          </p>
        )}
        {(paths.data?.roots ?? []).map((r) => {
          const on = picks.some((p) => p.dir === r.path);
          return (
            <label
              key={r.path}
              className={`grid cursor-pointer grid-cols-[18px_minmax(0,1fr)_auto_auto] items-center gap-3 border-soft border-b px-2 py-1.5 hover:bg-hover ${on ? "bg-sel" : ""}`}
            >
              <input
                type="checkbox"
                checked={on}
                onChange={() => toggle(r.path)}
              />
              <span
                className={`truncate font-mono ${on ? "text-fg" : "text-sec"}`}
              >
                {r.path || "/"}
              </span>
              <span className="truncate font-mono text-faint text-xs">
                {(r.markers ?? []).join(" · ")}
              </span>
              <span className="text-mut text-xs">
                {(r.used_by ?? []).map((u) => u.split("/").at(-1)).join(", ")}
              </span>
            </label>
          );
        })}
        {paths.data && (paths.data.roots ?? []).length === 0 && (
          <p className="px-2 py-2 text-mut">
            Nothing here looks like an app (a Dockerfile, package.json,
            go.mod...). Type the directory to build below.
          </p>
        )}
        <div className="grid grid-cols-[18px_minmax(0,1fr)] items-center gap-3 px-2 py-1.5">
          <span aria-hidden className="text-faint">
            +
          </span>
          <input
            aria-label="Another directory"
            value={other}
            placeholder="another directory, e.g. services/worker"
            spellCheck={false}
            autoComplete="off"
            onChange={(e) => {
              setOther(e.target.value);
              setOtherName(null);
            }}
            className={`${input} w-full`}
          />
          {suggestions.length > 0 && (
            <span className="col-start-2 flex flex-wrap gap-x-3 text-xs">
              {suggestions.map((d) => (
                <button
                  key={d}
                  type="button"
                  className="lnk font-mono"
                  onClick={() => {
                    setOther(d);
                    setOtherName(null);
                  }}
                >
                  {d}
                </button>
              ))}
            </span>
          )}
        </div>
      </fieldset>
      {paths.data?.truncated && (
        <p className="mt-1 text-warn-text text-xs">
          The repository is too big to list whole; type a directory that isn't
          listed.
        </p>
      )}
      {all.length > 0 && (
        <div className="mt-3 grid grid-cols-[minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1.5">
          {all.map((p, i) => (
            <PickRow
              key={p.dir}
              pick={p}
              problem={problems[i]}
              onName={(next) =>
                picks.some((q) => q.dir === p.dir)
                  ? setPicks((ps) =>
                      ps.map((q) =>
                        q.dir === p.dir ? { ...q, name: next } : q,
                      ),
                    )
                  : setOtherName(next)
              }
            />
          ))}
          <span className="col-span-2 text-mut text-xs">
            {all.length === 1 ? "It deploys" : "Each deploys"} the head of{" "}
            <span className="font-mono">{branch}</span> now; pushes that change
            nothing in {all.length === 1 ? "its directory" : "a directory"} are
            skipped.
          </span>
        </div>
      )}
      <DialogFoot cli={cli} note={<ErrorText error={create.error} />}>
        <Button onClick={onBack}>Back</Button>
        <button
          type="submit"
          className="btn pri"
          disabled={blocked || create.isPending}
        >
          {all.length > 1
            ? `Create ${all.length} and deploy`
            : "Create and deploy"}{" "}
          <kbd>⌘↵</kbd>
        </button>
      </DialogFoot>
    </form>
  );
}

/** A new service's name, next to the directory it builds. */
function PickRow({
  pick,
  problem,
  onName,
}: {
  pick: Pick;
  problem: string | undefined;
  onName: (name: string) => void;
}) {
  return (
    <>
      <input
        aria-label={`Name of the service for ${pick.dir || "the root"}`}
        value={pick.name}
        onChange={(e) => onName(toName(e.target.value))}
        spellCheck={false}
        autoComplete="off"
        aria-invalid={!!problem}
        className={`${input} w-full ${invalid(problem)}`}
      />
      <span className="font-mono text-faint text-xs">{pick.dir || "/"}</span>
      {problem && (
        <span role="alert" className="col-span-2 -mt-1 text-fail text-xs">
          {problem}
        </span>
      )}
    </>
  );
}
