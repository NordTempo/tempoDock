import { type ReactNode, useState } from "react";
import { ago, clock, day } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  CopyButton,
  ErrorText,
  Page,
  PageBar,
  Panel,
  StateText,
} from "../../ui/kit";
import { domainState, type State } from "../../ui/status";
import { Saved } from "../create/form";
import {
  type Address,
  useAddress,
  useRemoveAddress,
  useRetryWildcards,
  useSetAddress,
  type Wildcard,
} from "./data";

// The console's Address page (mock A): Dock's public address, its status
// the way a domain's shows, the DNS record it needs, and the private
// address that always works (ADR 0029). Under it, the wildcard
// certificate of each base domain servers generate hostnames under (ADR
// 0090, mock B).

export function AddressPage() {
  const { data: address, refetch, isFetching } = useAddress();
  return (
    <>
      <PageBar title="Address" />
      <Page>
        <Panel>
          <table>
            {/* Labels sit level with their controls. */}
            <tbody className="[&>tr>td]:align-middle">
              <tr>
                {/* Level with the input, above its hint line. */}
                <td className="w-36 pt-3 align-top! text-faint">
                  Public address
                </td>
                <td className="py-1.5">
                  <HostnameForm address={address} />
                </td>
              </tr>
              {address.hostname && address.status && (
                <tr>
                  <td className="py-2.5 text-faint">Status</td>
                  <td className="py-1.5 whitespace-normal">
                    <span className="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <StateText
                        state={domainState({ status: address.status })}
                      />
                      {address.checked_at && (
                        <span className="text-faint">
                          · checked {ago(address.checked_at)}
                        </span>
                      )}
                      {address.status !== "active" && (
                        <Act
                          small
                          cli="dock address"
                          disabled={isFetching}
                          onClick={() => refetch()}
                        >
                          Check again
                        </Act>
                      )}
                    </span>
                    {address.status_reason && (
                      <span
                        className={`block ${address.status === "error" ? "text-fail" : "text-mut"}`}
                      >
                        {address.status_reason}
                      </span>
                    )}
                  </td>
                </tr>
              )}
              {address.hostname && (
                <tr>
                  <td className="py-2.5 text-faint">DNS</td>
                  <td className="py-2.5 font-mono whitespace-normal">
                    <DNS address={address} />
                  </td>
                </tr>
              )}
              <tr>
                <td className="py-2.5 text-faint">Private address</td>
                <td className="py-2.5">
                  <span className="font-mono">{address.private_url}</span>{" "}
                  <span className="text-faint">· always on</span>
                </td>
              </tr>
            </tbody>
          </table>
        </Panel>
        {(address.wildcards ?? []).length > 0 && (
          <Wildcards wildcards={address.wildcards ?? []} />
        )}
        <p className="text-mut">
          <span className="font-mono">dock login {address.url ?? "<url>"}</span>{" "}
          works from anywhere once it is active. Logins are rate limited per
          visitor.
        </p>
      </Page>
    </>
  );
}

/**
 * The hostname, in the app's save model for text: Save and Cancel while it
 * differs from the saved one, Enter saves, Esc cancels, "Saved" after.
 * Remove asks first. An empty Save says what to type.
 */
function HostnameForm({ address }: { address: Address }) {
  const set = useSetAddress();
  const remove = useRemoveAddress();
  const [draft, setDraft] = useState(address.hostname);
  const [asking, setAsking] = useState(false);
  const [empty, setEmpty] = useState(false);
  const hostname = draft.trim().toLowerCase();
  const changed = hostname !== address.hostname;
  return (
    <form
      className="flex flex-col gap-1"
      onSubmit={(e) => {
        e.preventDefault();
        if (!hostname) return setEmpty(true);
        set.mutate(hostname);
      }}
      onKeyDown={(e) => {
        if (e.key !== "Escape" || !changed) return;
        setDraft(address.hostname);
        setEmpty(false);
        set.reset();
      }}
    >
      <span className="flex flex-wrap items-center gap-2">
        <input
          aria-label="Public address"
          placeholder="dock.example.com"
          value={draft}
          onChange={(e) => {
            setEmpty(false);
            set.reset();
            setDraft(e.target.value);
          }}
          spellCheck={false}
          className="field min-w-0 flex-[1_1_240px] py-[5px] font-mono"
        />
        {(changed || !address.hostname) && (
          <Act
            small
            primary
            type="submit"
            cli={`dock address set ${hostname || "<hostname>"}`}
            disabled={set.isPending}
          >
            Save
          </Act>
        )}
        {changed && address.hostname && (
          <Button
            small
            onClick={() => {
              setDraft(address.hostname);
              set.reset();
            }}
          >
            Cancel
          </Button>
        )}
        <Saved ok={set.isSuccess && !changed} />
        {/* Beside the field while it holds the saved name, where Save and
            Cancel go once it changes; it asks first. */}
        {address.hostname && !changed && (
          <Button small danger onClick={() => setAsking(true)}>
            Remove
          </Button>
        )}
      </span>
      {empty ? (
        <span role="alert" className="text-xs text-fail">
          Type the name Dock answers at, like dock.example.com.
        </span>
      ) : (
        <span className="text-xs text-mut">
          Needed for invite links and GitHub pushes.
        </span>
      )}
      <ErrorText error={set.error} />
      {asking && (
        <Confirm
          danger
          action={`Remove ${address.hostname}`}
          cli="dock address remove"
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(undefined, {
              onSuccess: () => {
                setDraft("");
                setAsking(false);
              },
            })
          }
          onCancel={() => {
            remove.reset();
            setAsking(false);
          }}
        >
          Dock stops answering at <b>{address.hostname}</b>. Invite links and
          GitHub webhooks that use it stop working, and so do sessions and CLI
          logins there. Dock stays at {address.private_url}.
        </Confirm>
      )}
    </form>
  );
}

/** The record to create, and what the name resolves to now. */
function DNS({ address }: { address: Address }) {
  const records = address.dns_records ?? [];
  const resolved = address.resolved_ips ?? [];
  const ok =
    address.status === "active" || address.status === "issuing_certificate";
  return (
    <span className="flex flex-col gap-1">
      {records.map((r) => (
        <span key={`${r.type} ${r.value}`}>
          {r.type} {r.name} → {r.value} <CopyButton text={r.value} />
        </span>
      ))}
      {records.length === 0 && (
        <span className="font-sans text-mut">
          This server has no public IP in its config; point the name at it.
        </span>
      )}
      <span className="font-sans">
        {ok ? (
          <span className="text-ok">✓ resolves</span>
        ) : (
          <span className="text-mut">
            Resolves now to{" "}
            <span className="font-mono">
              {resolved.length ? resolved.join(", ") : "nothing"}
            </span>
            . Create the record at your DNS provider, without a proxy.
          </span>
        )}
      </span>
    </span>
  );
}

/** A base domain's certificate as its status reads. */
function wildcardState(w: Wildcard): State {
  switch (w.status) {
    case "active":
      return w.reason
        ? { tone: "warn", glyph: "▲", label: "renewal failing" }
        : { tone: "ok", glyph: "●", label: "wildcard" };
    case "issuing":
      return { tone: "prog", glyph: "◐", label: "issuing" };
    case "failed":
      return { tone: "fail", glyph: "✕", label: "failed" };
    default:
      return { tone: "mut", glyph: "○", label: "one per hostname" };
  }
}

/**
 * Each base domain's wildcard certificate: served, being issued, failing
 * (with Retry), or why its hostnames get a certificate each.
 */
function Wildcards({ wildcards }: { wildcards: Wildcard[] }) {
  const retry = useRetryWildcards();
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Generated hostnames</th>
            <th>Servers</th>
            <th>Certificate</th>
            <th>Expires</th>
            <th>Renews</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {wildcards.map((w) => {
            const failing = w.status === "failed" || !!w.reason;
            return (
              <WildcardRow
                key={w.base_domain}
                wildcard={w}
                retry={
                  failing &&
                  w.status !== "unavailable" && (
                    <Act
                      small
                      cli="dock cloudflare retry"
                      disabled={retry.isPending}
                      onClick={() => retry.mutate()}
                    >
                      Retry
                    </Act>
                  )
                }
              />
            );
          })}
        </tbody>
      </table>
      <ErrorText error={retry.error} />
    </Panel>
  );
}

function WildcardRow({
  wildcard: w,
  retry,
}: {
  wildcard: Wildcard;
  retry: ReactNode;
}) {
  const renews = w.retry_at
    ? `retry ${clock(w.retry_at)}`
    : w.renew_at
      ? day(w.renew_at)
      : "–";
  return (
    <>
      <tr>
        <td className="py-2 font-mono">*.{w.base_domain}</td>
        <td className="py-2 text-mut">{(w.servers ?? []).join(", ")}</td>
        <td className="py-2">
          <StateText state={wildcardState(w)} />
        </td>
        <td className="py-2 text-mut">
          {w.not_after ? day(w.not_after) : "–"}
        </td>
        <td className="py-2 text-mut">{renews}</td>
        <td className="w-full py-1.5 text-right">{retry}</td>
      </tr>
      {w.reason && (
        <tr>
          <td colSpan={6} className="pb-2 whitespace-normal text-mut">
            {w.reason}
          </td>
        </tr>
      )}
    </>
  );
}
