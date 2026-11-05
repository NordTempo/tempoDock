import { useCli, useDataPolicy, useDataSource, useSetSensitive } from "./api";
import { Btn, ErrorText } from "./kit";
import { kindNames } from "./policy";
import type { DataColumnRef, DataPolicyRule, DataTable } from "./types";

// Marking sensitive columns (ADR 0016 section 5), admins only: which
// columns are marked, what that means for whom here, and suggestions by
// column name. PUT /data/sensitive-columns replaces the whole list, so
// every change sends the current marks plus or minus some.

/** Names that suggest personal or secret data. */
const likelyName =
  /e-?mail|passw(or)?d|hash|token|secret|phone|ssn|iban|address|birth/i;

/** Suggestions named at once; "Mark all" still marks every one. */
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
export function useSensitiveMarks(tables: readonly DataTable[]) {
  const set = useSetSensitive();
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
 * who sees what for this database (its effective policy), the marked
 * columns and suggestions by name across the whole schema.
 */
export function SensitiveSection({
  tables,
  marks,
}: {
  tables: readonly DataTable[];
  marks: SensitiveMarks;
}) {
  const { roles, subjectNames } = useDataSource();
  const policy = useDataPolicy();
  const cliHint = useCli();
  const rules = policy.data?.effective ?? [];
  const markedKeys = new Set(marks.marked.map(refKey));
  const suggested = columnsWhere(
    tables,
    (c) => !c.sensitive && likelyName.test(c.name),
  ).filter((c) => !markedKeys.has(refKey(c)));
  const cli = cliHint(
    marks.marked.length
      ? `dock db sensitive set ${marks.marked.map(cliName).join(" ")}`
      : "dock db sensitive set --clear",
  );

  const one = suggested.length === 1 ? suggested[0] : undefined;

  return (
    <section className="flex flex-col gap-2 px-2">
      <h3 className="text-[13px] font-semibold">Sensitive columns</h3>
      <p className="flex flex-wrap items-center gap-x-1.5 gap-y-1">
        <span className="text-(--g-dim)">Marked:</span>
        <span className="font-mono text-xs">
          {marks.marked.length ? marks.marked.map(cliName).join(", ") : "none"}
        </span>
        {suggested.length > 0 && (
          <>
            <span className="ml-3 text-(--g-dim)">Suggested by name:</span>
            <span className="font-mono text-xs">
              {suggested.slice(0, maxSuggestions).map(cliName).join(", ")}
              {suggested.length > maxSuggestions &&
                ` and ${suggested.length - maxSuggestions} more`}
            </span>
            <Btn
              small
              className="ml-1.5"
              disabled={marks.pending}
              onClick={() => marks.mark(suggested)}
            >
              {one ? `Mark ${one.column}` : `Mark all ${suggested.length}`}
            </Btn>
          </>
        )}
      </p>
      <details className="text-xs text-(--g-dim)">
        <summary className="w-fit cursor-pointer">How masking works</summary>
        <div className="mt-2 flex max-w-[720px] flex-col gap-2">
          <p>
            Marked columns show as •••••• in table pages and references. Callers
            whose access says masked never see them: their SQL runs as{" "}
            {roles.masked}, which can't select them. Callers with visible reveal
            them per column in Tables, and each reveal is audited (data.reveal).
            Views need their own marks.
          </p>
          <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-4 gap-y-0.5">
            <dt>admins</dt>
            <dd className="text-(--g-text)">visible after a reveal</dd>
            {rules.map((r) => (
              <div key={r.subject} className="contents">
                <dt>{subjectNames[r.subject]}</dt>
                <dd>
                  <span className="text-(--g-text)">{seesText(r)}</span> (
                  {r.level} on {kindNames[r.kind]}
                  {r.subject === "tokens" && ", unless a token has its own"})
                </dd>
              </div>
            ))}
          </dl>
          {policy.error && <ErrorText error={policy.error} />}
        </div>
      </details>
      <ErrorText error={marks.error} />
      {cli && <p className="font-mono text-2xs text-(--g-mut)">{cli}</p>}
    </section>
  );
}
