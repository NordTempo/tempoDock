import { useNavigate } from "@tanstack/react-router";
import { type ReactNode, useState } from "react";
import {
  useRemoveServer,
  useRevokeServer,
  useRotateServerKey,
  useUpdateServer,
} from "../../data/mutations";
import type { Server } from "../../data/types";
import { ago, plural, until } from "../../ui/format";
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
      <dt>{label}</dt>
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
      <dl className="props max-w-[960px] grid-cols-[140px_minmax(0,1fr)]">
        <Fact label="Status">
          <StateText state={serverState(s.status)} />{" "}
          <span className="text-mut">{statusDetail(s)}</span>
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
          {s.status === "joining" ? (
            none("reported when it connects")
          ) : s.ingress === "cloudflare_tunnel" ? (
            <>
              Cloudflare tunnel
              {s.tunnel_hostname && (
                <>
                  <span className="text-mut">, custom domains CNAME to </span>
                  <span className="font-mono">{s.tunnel_hostname}</span>
                </>
              )}
            </>
          ) : (
            <>
              direct
              {ips.length > 0 && (
                <>
                  <span className="text-mut">, DNS points at </span>
                  <span className="font-mono">{ips.join(", ")}</span>
                </>
              )}
            </>
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
          <BaseDomain key={s.base_domain} server={s} />
        </Fact>
      </dl>
      {!s.local && <Actions server={s} />}
    </section>
  );
}

/** When the status began, and why the link ended if it did. */
function statusDetail(s: Server) {
  switch (s.status) {
    case "online":
    case "incompatible":
      return s.connected_at && `connected ${ago(s.connected_at)}`;
    case "offline":
      return [
        s.last_seen_at && `since ${ago(s.last_seen_at)}`,
        s.disconnect_reason,
      ]
        .filter(Boolean)
        .join(", ");
    case "joining":
      if (!s.join_expires_at) return "";
      return Date.parse(s.join_expires_at) > Date.now()
        ? `join token expires in ${until(s.join_expires_at)}`
        : "join token expired; remove it and add it again";
    case "revoked":
      return s.revoked_at && ago(s.revoked_at);
  }
}

/** Generated hostnames go under the server's base domain. */
function BaseDomain({ server }: { server: Server }) {
  const update = useUpdateServer();
  const saved = server.base_domain;
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
            <span className="font-mono">web-shop-staging.{domain}</span>
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

type Ask = "rotate" | "revoke" | "remove";

/**
 * Rotate the key (online only), revoke it, and remove the server once
 * nothing is placed on it (or cancel one that is still joining).
 */
function Actions({ server: s }: { server: Server }) {
  const rotate = useRotateServerKey();
  const revoke = useRevokeServer();
  const remove = useRemoveServer();
  const navigate = useNavigate();
  const [ask, setAsk] = useState<Ask | null>(null);
  const envs = s.environments ? plural(s.environments, "environment") : "";
  const placed = envs && `${envs} placed here`;
  const joining = s.status === "joining";
  const hasKey = s.status !== "revoked" && !joining;
  const cancel = (reset: () => void) => () => {
    reset();
    setAsk(null);
  };
  const name = <b className="font-mono font-medium">{s.name}</b>;

  switch (ask) {
    case "rotate":
      return (
        <Confirm
          action="Rotate key"
          cli={`dock server rotate-key ${s.name}`}
          pending={rotate.isPending}
          error={rotate.error}
          onConfirm={() =>
            rotate.mutate(s.name, { onSuccess: () => setAsk(null) })
          }
          onCancel={cancel(rotate.reset)}
        >
          Give {name} a new link key?{" "}
          <span className="text-mut">
            It makes the key itself and reconnects with it within seconds; the
            old key stops working. Apps keep running.
          </span>
        </Confirm>
      );
    case "revoke":
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
          onCancel={cancel(revoke.reset)}
        >
          Revoke {name}'s key?{" "}
          <span className="text-mut">
            It disconnects now and can't connect again until it is added again
            under this name.{" "}
            {envs
              ? `Apps in its ${envs} keep running there, but deploys, logs and domain changes stop.`
              : "Nothing is placed here."}
          </span>
        </Confirm>
      );
    case "remove":
      return (
        <Confirm
          danger
          action={joining ? `Cancel ${s.name}` : `Remove ${s.name}`}
          cli={`dock server remove ${s.name}`}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(s.name, {
              onSuccess: () => navigate({ to: "/servers" }),
            })
          }
          onCancel={cancel(remove.reset)}
        >
          {joining ? (
            <>
              Cancel joining {name}?{" "}
              <span className="text-mut">Its join token stops working.</span>
            </>
          ) : (
            <>
              Remove {name}?{" "}
              <span className="text-mut">
                Its key and statistics are deleted. To use the machine again,
                add it as a new server.
              </span>
            </>
          )}
        </Confirm>
      );
  }
  return (
    <div className="flex flex-wrap items-start gap-2.5 pt-1">
      {hasKey && (
        <Act
          cli={`dock server rotate-key ${s.name}`}
          disabled={s.status !== "online"}
          title={
            s.status === "online"
              ? undefined
              : "The server must be online to make a new key"
          }
          onClick={() => setAsk("rotate")}
        >
          Rotate key
        </Act>
      )}
      {hasKey && (
        <Act
          danger
          cli={`dock server revoke ${s.name}`}
          onClick={() => setAsk("revoke")}
        >
          Revoke key
        </Act>
      )}
      <Act
        danger
        cli={`dock server remove ${s.name}`}
        disabled={!!placed}
        title={placed ? `${placed}. Remove them first.` : undefined}
        onClick={() => setAsk("remove")}
      >
        {joining ? "Cancel join" : "Remove server"}
      </Act>
      {placed && (
        <span className="self-center text-mut">
          Remove needs an empty server: {placed}.
        </span>
      )}
    </div>
  );
}
