import { useState } from "react";
import { ago } from "../../ui/format";
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
import { domainState } from "../../ui/status";
import {
  type Address,
  useAddress,
  useRemoveAddress,
  useSetAddress,
} from "./data";

// The console's Address page (mock A): Dock's public address, its status
// the way a domain's shows, the DNS record it needs, and the private
// address that always works (ADR 0029).

export function AddressPage() {
  const { data: address, refetch, isFetching } = useAddress();
  return (
    <>
      <PageBar title="Address" />
      <Page width="max-w-[900px]" gap="gap-5">
        <Panel>
          <table>
            <tbody>
              <tr>
                <td className="w-36 py-2.5 text-faint">Public address</td>
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
        <p className="text-faint">
          Invite links and GitHub commit statuses use the public address once
          set.{" "}
          <span className="font-mono">dock login {address.url ?? "<url>"}</span>{" "}
          works from anywhere once it is active. Logins are rate limited per
          visitor.
        </p>
      </Page>
    </>
  );
}

/** The hostname, saved with Save; Remove asks first. */
function HostnameForm({ address }: { address: Address }) {
  const set = useSetAddress();
  const remove = useRemoveAddress();
  const [draft, setDraft] = useState(address.hostname);
  const [asking, setAsking] = useState(false);
  const hostname = draft.trim().toLowerCase();
  if (asking) {
    return (
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
        onCancel={() => setAsking(false)}
      >
        Stop serving Dock at <b className="font-semibold">{address.hostname}</b>
        ?{" "}
        <span className="text-mut">
          Sessions and CLI logins there stop working. Dock stays at{" "}
          {address.private_url}.
        </span>
      </Confirm>
    );
  }
  return (
    <form
      className="flex flex-wrap items-center gap-2"
      onSubmit={(e) => {
        e.preventDefault();
        set.mutate(hostname);
      }}
    >
      <input
        aria-label="Public address"
        placeholder="dock.example.com"
        value={draft}
        onChange={(e) => setDraft(e.target.value)}
        spellCheck={false}
        className="field min-w-0 flex-[1_1_240px] py-[5px] font-mono"
      />
      <Act
        small
        primary
        type="submit"
        cli={`dock address set ${hostname || "<hostname>"}`}
        disabled={!hostname || hostname === address.hostname || set.isPending}
      >
        Save
      </Act>
      {address.hostname && (
        <Button small onClick={() => setAsking(true)}>
          Remove
        </Button>
      )}
      <ErrorText error={set.error} />
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
