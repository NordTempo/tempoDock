import type { ServiceRef } from "../../data/types";
import { ErrorText } from "../../ui/kit";
import { useSetSensitive } from "./api";
import { kindNames, subjectNames, usePolicy } from "./policy";
import type { DataColumnRef, DataPolicyRule, DataTable } from "./types";

// Marking sensitive columns (ADR 0016 section 5), admins only: which
// columns are marked, what that means for whom here, and suggestions by
// column name. PUT /data/sensitive-columns replaces the whole list, so
// every change sends the current marks plus or minus some.

/** Names that suggest personal or secret data. */
const likelyName =
  /e-?mail|passw(or)?d|hash|token|secret|phone|ssn|iban|address|birth/i;

/** Suggestions shown at once; "mark all" still marks every one. */
const maxSuggestions = 30;

const refKey = (c: DataColumnRef) => `${c.schema}.${c.table}.${c.column}`;

/** schema.table.column, or table.column in public (the CLI's form). */
const cliName = (c: DataColumnRef) =>
  c.schema === "public" ? `${c.table}.${c.column}` : refKey(c);

/** Columns of `tables` that match `pick`, as references. */
function columnsWhere(
  tables: readonly DataTable[],
  pick: (c: NonNullable<DataTable["columns"]>[number]) => boolean,
): DataColumnRef[] {
  return tables.flatMap((t) =>
    (t.columns ?? [])
      .filter(pick)
      .map((c) => ({ schema: t.schema, table: t.name, column: c.name })),
  );
}

/** The marks, and changing them: toggle one column or mark several. */
export function useSensitiveMarks(
  serviceRef: ServiceRef,
  tables: readonly DataTable[],
) {
  const set = useSetSensitive(serviceRef);
  const marked = columnsWhere(tables, (c) => c.sensitive);
  return {
    marked,
    pending: set.isPending,
    error: set.error,
    toggle: (ref: DataColumnRef, on: boolean) =>
      set.mutate(
        on ? [...marked, ref] : marked.filter((m) => refKey(m) !== refKey(ref)),
      ),
    mark: (refs: DataColumnRef[]) => set.mutate([...marked, ...refs]),
  };
}

export type SensitiveMarks = ReturnType<typeof useSensitiveMarks>;

/** What a policy rule means for sensitive values, in a few words. */
function seesText(rule: DataPolicyRule) {
  if (rule.level === "none" || rule.level === "schema") return "no rows";
  return rule.sensitive === "visible" ? "visible after a reveal" : "masked";
}

/**
 * The Sensitive columns section of the Schema tab: what masking means,
 * who sees what in this environment (its effective policy), the marked
 * columns and suggestions by name across the whole schema.
 */
export function SensitiveSection({
  serviceRef,
  tables,
  marks,
  flags,
}: {
  serviceRef: ServiceRef;
  tables: readonly DataTable[];
  marks: SensitiveMarks;
  flags: string;
}) {
  const policy = usePolicy({
    project: serviceRef.project,
    environment: serviceRef.environment,
  });
  const rules = policy.data?.effective ?? [];
  const markedKeys = new Set(marks.marked.map(refKey));
  const suggested = columnsWhere(
    tables,
    (c) => !c.sensitive && likelyName.test(c.name),
  ).filter((c) => !markedKeys.has(refKey(c)));
  const cli = marks.marked.length
    ? `dock db sensitive set ${marks.marked.map(cliName).join(" ")} ${flags}`
    : `dock db sensitive set --clear ${flags}`;

  return (
    <section className="flex flex-col gap-2">
      <h3 className="text-sm font-semibold">Sensitive columns</h3>
      <p className="max-w-[720px] text-xs text-mut">
        Marked columns show as •••••• in table pages and references. Callers
        whose access says masked never see them: their SQL runs as
        dock_reader_masked, which can't select them. Callers with visible reveal
        them per column in Tables, and each reveal is audited (data.reveal).
        Views need their own marks.
      </p>
      <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 font-mono text-xs">
        <dt className="text-mut">admins</dt>
        <dd>visible after a reveal</dd>
        {rules.map((r) => (
          <div key={r.subject} className="contents">
            <dt className="text-mut">{subjectNames[r.subject]}</dt>
            <dd>
              {seesText(r)}
              <span className="text-faint">
                {" "}
                {r.level} on {kindNames[r.kind]}
                {r.subject === "tokens" && ", unless a token has its own"}
              </span>
            </dd>
          </div>
        ))}
      </dl>
      {policy.error && <ErrorText error={policy.error} />}

      <p className="font-mono text-xs">
        <span className="text-mut">marked </span>
        {marks.marked.length
          ? marks.marked.map((c) => refKey(c)).join(", ")
          : "none"}
      </p>
      {suggested.length > 0 && (
        <div className="flex flex-col gap-1 font-mono text-xs">
          <p>
            <span className="text-mut">likely sensitive by name </span>
            <button
              type="button"
              className="lnk"
              disabled={marks.pending}
              onClick={() => marks.mark(suggested)}
            >
              Mark all {suggested.length}
            </button>
          </p>
          <ul className="flex flex-wrap gap-x-4 gap-y-0.5">
            {suggested.slice(0, maxSuggestions).map((c) => (
              <li key={refKey(c)}>
                {c.schema === "public" ? "" : `${c.schema}.`}
                {c.table}.<span className="text-white">{c.column}</span>{" "}
                <button
                  type="button"
                  className="lnk"
                  disabled={marks.pending}
                  onClick={() => marks.toggle(c, true)}
                >
                  Mark
                </button>
              </li>
            ))}
            {suggested.length > maxSuggestions && (
              <li className="text-mut">
                and {suggested.length - maxSuggestions} more
              </li>
            )}
          </ul>
        </div>
      )}
      <ErrorText error={marks.error} />
      <p className="font-mono text-2xs text-mut">{cli}</p>
    </section>
  );
}
