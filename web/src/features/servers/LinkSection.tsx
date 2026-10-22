import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import {
  useRemoveServer,
  useRevokeServer,
  useUpdateServer,
} from "../../data/mutations";
import type { Server } from "../../data/types";
import { ago, plural } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  SectionHead,
  StateText,
} from "../../ui/kit";
import { serverState } from "../../ui/status";
import { Version } from "./parts";

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <>
      <dt className="text-mut">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </>
  );
}

const none = (text: string) => <span className="text-mut">{text}</span>;

/**
 * How a server is connected to the control plane (status, versions,
 * ingress, key), its base domain, and the admin's revoke and remove.
 */
export function LinkSection({ server: s }: { server: Server }) {
  const ips = s.public_ips ?? [];
  return (
    <section aria-labelledby="link" className="flex flex-col gap-2">
      <SectionHead id="link">Link</SectionHead>
      <dl className="grid grid-cols-[max-content_minmax(0,1fr)] gap-x-[18px] gap-y-1.5">
        <Fact label="Status">
          <StateText state={serverState(s.status)} />
          {s.status_since && (
            <span className="text-mut"> since {ago(s.status_since)}</span>
          )}
        </Fact>
        {!s.local && (
          <Fact label="Last seen">
            {s.status === "online"
              ? "now"
              : s.last_seen_at
                ? ago(s.last_seen_at)
                : none("never")}
          </Fact>
        )}
        <Fact label="Versions">
          <span className="text-mut">dock</span> <Version server={s} />
          {s.docker_version && (
            <>
              {" "}
              <span className="text-mut">· Docker</span>{" "}
              <span className="font-mono text-mut">{s.docker_version}</span>
            </>
          )}
        </Fact>
        <Fact label="Ingress">
          {s.ingress === "cloudflare_tunnel" ? (
            <>
              Cloudflare tunnel
              {s.tunnel_hostname && (
                <>
                  <span className="text-mut">, custom domains CNAME to </span>
                  <span className="font-mono">{s.tunnel_hostname}</span>
                </>
              )}
            </>
          ) : s.ingress === "direct" ? (
            <>
              direct
              {ips.length > 0 && (
                <>
                  <span className="text-mut">, DNS points at </span>
                  <span className="font-mono">{ips.join(", ")}</span>
                </>
              )}
            </>
          ) : (
            none("reported when it connects")
          )}
        </Fact>
        <Fact label="Key">
          {s.local ? (
            none("control plane, no link")
          ) : s.key_fingerprint ? (
            <span className="font-mono break-all">{s.key_fingerprint}</span>
          ) : s.status === "joining" ? (
            none("pinned when it joins")
          ) : (
            none("none, revoked")
          )}
        </Fact>
        <Fact label="Base domain">
          <BaseDomain key={s.base_domain ?? ""} server={s} />
        </Fact>
      </dl>
      {!s.local && <Removal server={s} />}
    </section>
  );
}

/** Generated hostnames go under the server's base domain. */
function BaseDomain({ server }: { server: Server }) {
  const update = useUpdateServer();
  const saved = server.base_domain ?? "";
  const [value, setValue] = useState(saved);
  const domain = value.trim().toLowerCase();
  const changed = domain !== saved;
  return (
    <form
      aria-label="Base domain"
      className="flex flex-wrap items-center gap-x-3 gap-y-1.5"
      onSubmit={(e) => {
        e.preventDefault();
        if (changed) {
          update.mutate({
            name: server.name,
            patch: { base_domain: domain },
          });
        }
      }}
    >
      <input
        aria-label="Base domain"
        placeholder="up.example.dev"
        spellCheck={false}
        value={value}
        onChange={(e) => setValue(e.target.value)}
        className="field w-56 py-[3px] font-mono"
      />
      {changed && (
        <Button small primary type="submit" disabled={update.isPending}>
          Save
        </Button>
      )}
      <span className="text-mut">
        {domain ? (
          <>
            generated hostnames look like{" "}
            <span className="font-mono">web-staging.{domain}</span>
          </>
        ) : (
          "none, so services here get no generated hostnames"
        )}
      </span>
      <code className="font-mono text-2xs text-mut">
        dock server update {server.name} --base-domain {domain || "<domain>"}
      </code>
      <ErrorText error={update.error} />
    </form>
  );
}

/** Revoke the key, or remove the server once nothing is placed on it. */
function Removal({ server: s }: { server: Server }) {
  const revoke = useRevokeServer();
  const remove = useRemoveServer();
  const navigate = useNavigate();
  const [ask, setAsk] = useState<"revoke" | "remove" | null>(null);
  const envs = s.environments ? plural(s.environments, "environment") : "";
  const placed = envs && `${envs} placed here`;

  if (ask === "revoke") {
    return (
      <Confirm
        danger
        action={`Revoke ${s.name}`}
        cli={`dock server revoke ${s.name}`}
        pending={revoke.isPending}
        error={revoke.error}
        onConfirm={() =>
          revoke.mutate(s.name, { onSuccess: () => setAsk(null) })
        }
        onCancel={() => {
          revoke.reset();
          setAsk(null);
        }}
      >
        Revoke <b className="font-mono font-medium">{s.name}</b>'s key?{" "}
        <span className="text-mut">
          It disconnects now and can't reconnect.{" "}
          {envs
            ? `Apps in its ${envs} keep running there, but deploys, logs and domain changes stop.`
            : "Nothing is placed here."}
        </span>
      </Confirm>
    );
  }
  if (ask === "remove") {
    return (
      <Confirm
        danger
        action={`Remove ${s.name}`}
        cli={`dock server remove ${s.name}`}
        pending={remove.isPending}
        error={remove.error}
        onConfirm={() =>
          remove.mutate(s.name, {
            onSuccess: () => navigate({ to: "/servers" }),
          })
        }
        onCancel={() => {
          remove.reset();
          setAsk(null);
        }}
      >
        Remove <b className="font-mono font-medium">{s.name}</b>?{" "}
        <span className="text-mut">
          Its key and statistics are deleted. To use the machine again, add it
          as a new server.
        </span>
      </Confirm>
    );
  }
  return (
    <div className="flex flex-wrap items-start gap-2.5 pt-1">
      <Act
        danger
        cli={`dock server revoke ${s.name}`}
        onClick={() => setAsk("revoke")}
      >
        Revoke key
      </Act>
      <Act
        danger
        cli={`dock server remove ${s.name}`}
        disabled={!!placed}
        title={placed ? `${placed}. Remove them first.` : undefined}
        onClick={() => setAsk("remove")}
      >
        Remove server
      </Act>
      {placed && (
        <span className="self-center text-mut">
          Remove needs an empty server: {placed}.
        </span>
      )}
    </div>
  );
}
