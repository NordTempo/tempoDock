import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import type { EnvRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Button, ErrorText } from "../../ui/kit";
import {
  autofocus,
  Field,
  Fields,
  Footer,
  input,
  invalid,
  nameProblem,
  toName,
  useValidation,
} from "../create/form";
import {
  type AppTemplate,
  appTemplates,
  type TemplateInput,
  useUseTemplate,
} from "./data";
import { Mark } from "./Mark";

/** Categories in the order the list shows them; others follow. */
const order = [
  "analytics",
  "automation",
  "monitoring",
  "dev",
  "security",
  "internal tools",
  "docs",
  "email",
  "crm",
  "personal",
  "ai",
  "databases",
];

/** Single-quotes a CLI argument when the shell would split it. */
const arg = (v: string) =>
  /^[\w@%+=:,./-]+$/.test(v) ? v : `'${v.replaceAll("'", `'\\''`)}'`;

/** MB as the picker shows it: 512 MB, 1.5 GB. */
export const memText = (mb: number) =>
  mb >= 1024 ? `${Math.round((mb / 1024) * 10) / 10} GB` : `${mb} MB`;

/** An image without its registry and digest: umami:3.5.0. */
const shortImage = (image: string) =>
  (image.split("@")[0] ?? image).split("/").at(-1) ?? image;

/**
 * The Template source of New service and New project (ADR 0082, mock A):
 * the catalog on the left with search, the picked template on the right
 * with its name, domain and inputs, and the services it creates. Using it
 * is `dock template use`; it opens the main service, or lists what didn't
 * work (a domain, a first deploy). `head` is the dialog's fields above
 * (environment, source); `before` runs first (New project creates the
 * project), and `missing` names what it still needs.
 */
export function FromTemplate({
  env,
  taken,
  head,
  initial,
  before,
  beforeCli = [],
  missing,
  onClose,
}: {
  env: EnvRef;
  /** Service names already in the environment. */
  taken: readonly string[];
  head: ReactNode;
  initial?: string;
  before?: () => Promise<void>;
  /** The commands `before` amounts to, for the footer. */
  beforeCli?: readonly string[];
  missing?: string | false;
  onClose: () => void;
}) {
  const org = useOrg();
  const navigate = useNavigate();
  const catalog = useQuery(appTemplates());
  const use = useUseTemplate();
  const v = useValidation();
  const [search, setSearch] = useState("");
  const [picked, setPicked] = useState(initial ?? "");
  const [typed, setTyped] = useState<string | null>(null);
  const [domain, setDomain] = useState("");
  const [inputs, setInputs] = useState<Record<string, string>>({});
  const [failed, setFailed] = useState<{
    services: string[];
    failed: string[];
  } | null>(null);

  const all = catalog.data ?? [];
  const q = search.trim().toLowerCase();
  const shown = all
    .filter(
      (t) =>
        !q ||
        `${t.name} ${t.title} ${t.summary} ${t.category}`
          .toLowerCase()
          .includes(q),
    )
    .sort(
      (a, b) =>
        rank(a.category) - rank(b.category) || a.title.localeCompare(b.title),
    );
  const t =
    shown.find((x) => x.name === picked) ??
    shown[0] ??
    all.find((x) => x.name === picked);

  function pick(next: AppTemplate | undefined) {
    if (!next || next.name === t?.name) return;
    setPicked(next.name);
    setTyped(null);
    setInputs({});
    use.reset();
  }

  if (failed) {
    const main = failed.services[0] ?? "";
    return (
      <div className="flex flex-col gap-3">
        <p>
          Created{" "}
          <span className="font-mono">{failed.services.join(", ")}</span>, but
          not everything worked:
        </p>
        <ul className="list-disc pl-5 text-warn-text">
          {failed.failed.map((f) => (
            <li key={f}>{f}</li>
          ))}
        </ul>
        <div className="flex gap-2">
          <Link
            to="/$org/$project/$environment/$service"
            params={{ org, ...env, service: main }}
            className="btn pri"
            ref={autofocus}
            onClick={onClose}
          >
            Open {main}
          </Link>
          <Button onClick={onClose}>Close</Button>
        </div>
      </div>
    );
  }

  const name = typed ?? t?.name ?? "";
  const names = (t?.services ?? []).map((s) =>
    s.main ? name : `${name}-${s.name}`,
  );
  const nameError =
    nameProblem("service", name, taken) ??
    (names.find((n) => taken.includes(n) || n.length > 32) &&
      `${names.find((n) => taken.includes(n) || n.length > 32)} would be taken or too long. Pick another name.`) ??
    v.required(!name, "Enter a name.");
  const value = (key: string, fallback = "") => inputs[key] ?? fallback;
  const inputError = (i: TemplateInput) => {
    const val = value(i.key, i.default).trim();
    if (i.kind === "email" && val && !val.includes("@"))
      return "Enter an email address.";
    return v.required(!val && !i.optional, `Enter ${i.label.toLowerCase()}.`);
  };
  const need = [
    missing ?? false,
    ...(t?.inputs ?? [])
      .filter((i) => !i.optional && !value(i.key, i.default).trim())
      .map((i) => i.label.toLowerCase()),
  ];
  const blocked =
    !t ||
    need.some(Boolean) ||
    !!nameProblem("service", name, taken) ||
    names.some((n) => taken.includes(n) || n.length > 32) ||
    (t.inputs ?? []).some(
      (i) =>
        i.kind === "email" && !!value(i.key) && !value(i.key).includes("@"),
    );

  const sets = (t?.inputs ?? [])
    .filter((i) => inputs[i.key] !== undefined && inputs[i.key] !== i.default)
    .map(
      (i) =>
        ` --set ${i.key}=${i.kind === "secret" ? "<secret>" : arg(value(i.key))}`,
    )
    .join("");
  const cli = [
    ...beforeCli,
    `dock template use ${t?.name ?? "<template>"}${typed && typed !== t?.name ? ` --name ${typed}` : ""}${domain.trim() ? ` --domain ${domain.trim()}` : ""}${sets} -e ${env.environment} -p ${env.project || "<project>"}`,
  ];

  async function submit() {
    if (!t || !v.check(!blocked)) return;
    await before?.();
    const filled = Object.fromEntries(
      (t.inputs ?? [])
        .map((i) => [i.key, value(i.key, i.default).trim()] as const)
        .filter(([, val]) => val !== ""),
    );
    const res = await use.mutateAsync({
      env,
      template: t.name,
      name,
      ...(t.domain && domain.trim() && { domain: domain.trim() }),
      inputs: filled,
    });
    const failures = res.failed ?? [];
    if (failures.length > 0) {
      setFailed({ services: res.services ?? [], failed: failures });
      return;
    }
    await navigate({
      to: "/$org/$project/$environment/$service",
      params: { org, ...env, service: res.services?.[0] ?? name },
    });
    onClose();
  }

  let lastCategory = "";
  return (
    <form
      ref={v.form}
      aria-label="New service from a template"
      onSubmit={(e) => {
        e.preventDefault();
        submit().catch(() => {});
      }}
    >
      {head}
      <div className="mt-3 grid min-h-[420px] grid-cols-[280px_minmax(0,1fr)] border-t border-line">
        <div className="flex max-h-[460px] flex-col border-r border-line py-2.5 pr-2.5">
          <input
            ref={initial ? undefined : autofocus}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            onKeyDown={(e) => {
              if (e.key !== "ArrowDown" && e.key !== "ArrowUp") return;
              e.preventDefault();
              const i = shown.findIndex((x) => x.name === t?.name);
              pick(shown[i + (e.key === "ArrowDown" ? 1 : -1)]);
            }}
            placeholder={`Search ${all.length || ""} templates`}
            aria-label="Search templates"
            spellCheck={false}
            autoComplete="off"
            className={`${input} w-full font-sans`}
          />
          <div
            role="listbox"
            aria-label="Templates"
            className="mt-1.5 min-h-0 flex-1 overflow-y-auto"
          >
            {catalog.error && <ErrorText error={catalog.error} />}
            {shown.map((x) => {
              const header = x.category !== lastCategory && x.category;
              lastCategory = x.category;
              return (
                <div key={x.name}>
                  {header && (
                    <div className="px-2.5 pt-2.5 pb-1 text-xs text-mut capitalize">
                      {header}
                    </div>
                  )}
                  <button
                    type="button"
                    role="option"
                    aria-selected={x.name === t?.name}
                    onClick={() => pick(x)}
                    className={`flex w-full items-center gap-2.5 rounded-ctl px-2.5 py-1.5 text-left ${x.name === t?.name ? "bg-sel" : "hover:bg-hover"}`}
                  >
                    <Mark path={x.mark} title={x.title} />
                    <span className="flex-none text-fg">{x.title}</span>
                    <span className="min-w-0 truncate text-xs text-mut">
                      {x.summary}
                    </span>
                    <span className="ml-auto flex-none pl-1 font-mono text-[11.5px] text-faint">
                      {memText(x.memory_mb)}
                    </span>
                  </button>
                </div>
              );
            })}
            {catalog.data && shown.length === 0 && (
              <p className="px-2.5 py-2 text-xs text-mut">
                No template matches. Compose files import from New project.
              </p>
            )}
          </div>
        </div>
        {t && (
          <div className="min-w-0 py-4 pl-4">
            <div className="flex items-center gap-2.5">
              <Mark path={t.mark} title={t.title} large />
              <div className="min-w-0">
                <div className="text-md font-semibold">{t.title}</div>
                <div className="text-xs text-mut">
                  {t.summary}{" "}
                  <span className="text-faint">
                    · {t.license}
                    {t.website && (
                      <>
                        {" · "}
                        <a
                          href={t.website}
                          target="_blank"
                          rel="noreferrer"
                          className="text-faint hover:text-fg"
                        >
                          {t.website.replace(/^https:\/\/(www\.)?/, "")}
                        </a>
                      </>
                    )}
                  </span>
                </div>
              </div>
            </div>
            <div className="mt-4">
              <Fields>
                <Field label="Name" error={nameError || undefined}>
                  <input
                    ref={initial ? autofocus : undefined}
                    value={name}
                    onChange={(e) => setTyped(toName(e.target.value))}
                    spellCheck={false}
                    autoComplete="off"
                    aria-invalid={!!nameError}
                    className={`${input} ${invalid(nameError || undefined)}`}
                  />
                </Field>
                {t.domain && (
                  <Field label="Domain">
                    <input
                      value={domain}
                      onChange={(e) => setDomain(e.target.value.trim())}
                      placeholder="generated"
                      spellCheck={false}
                      autoComplete="off"
                      className={input}
                    />
                  </Field>
                )}
                {(t.inputs ?? []).map((i) => {
                  const error = inputError(i);
                  const set = (val: string) =>
                    setInputs((cur) => ({ ...cur, [i.key]: val }));
                  return (
                    <Field
                      key={i.key}
                      label={i.label}
                      error={error}
                      note={i.help}
                    >
                      {i.kind === "choice" ? (
                        <select
                          value={value(i.key, i.default)}
                          onChange={(e) => set(e.target.value)}
                          className="w-fit font-mono"
                        >
                          {(i.choices ?? []).map((c) => (
                            <option key={c} value={c}>
                              {c}
                            </option>
                          ))}
                        </select>
                      ) : (
                        <input
                          type={i.kind === "secret" ? "password" : "text"}
                          value={value(i.key, i.default)}
                          onChange={(e) => set(e.target.value)}
                          placeholder={i.optional ? "optional" : ""}
                          spellCheck={false}
                          autoComplete={
                            i.kind === "secret" ? "new-password" : "off"
                          }
                          aria-invalid={!!error}
                          className={`${input} ${invalid(error)}`}
                        />
                      )}
                    </Field>
                  );
                })}
              </Fields>
            </div>
            <div className="mt-4 text-xs text-mut">Creates</div>
            <ul className="mt-1 font-mono text-xs leading-[1.8] text-sec">
              {(t.services ?? []).map((s, i) => (
                <li key={s.name} className="flex gap-2">
                  <span className="text-ok">+</span>
                  <span className="flex-none whitespace-nowrap text-fg">
                    {names[i] || "<name>"}
                  </span>
                  <span className="min-w-0 text-faint">
                    {[
                      s.template
                        ? `${s.template} template`
                        : shortImage(s.image ?? ""),
                      s.port && `port ${s.port}`,
                      memText(s.memory_mb),
                      ...(s.volumes ?? []).map((p) => `volume ${p}`),
                      s.main &&
                        t.domain &&
                        (domain.trim() || "generated domain"),
                      s.template && "daily backup",
                      (s.generated ?? []).length > 0 &&
                        `${(s.generated ?? []).join(", ")} generated`,
                    ]
                      .filter(Boolean)
                      .join(" · ")}
                  </span>
                </li>
              ))}
            </ul>
            <p className="mt-1 text-xs text-faint">
              {memText(t.memory_mb)} of memory in all
            </p>
            {t.notes && <p className="mt-3 text-xs text-mut">{t.notes}</p>}
          </div>
        )}
      </div>
      <Footer
        submit={
          t && t.services.length > 1
            ? `Create ${t.services.length} services`
            : "Create service"
        }
        pending={use.isPending}
        problem={v.problem(need, blocked)}
        error={use.error}
        cli={cli}
        onClose={onClose}
      />
    </form>
  );
}

const rank = (category: string) => {
  const i = order.indexOf(category);
  return i < 0 ? order.length : i;
};
