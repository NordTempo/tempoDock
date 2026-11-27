import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { useShortcuts } from "../../shell/keys";
import { isoDay } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  MoreItem,
  MoreMenu,
  Page,
  Panel,
} from "../../ui/kit";
import { OrgSettingsBar } from "../org/SettingsBar";
import {
  type Certificate,
  certificateQueries,
  daysLeft,
  useAddCertificate,
  useRemoveCertificate,
} from "./data";

/**
 * Settings › Certificates (ADR 0112, mock B): the organization's own TLS
 * certificates for names Let's Encrypt can't cover. One table of them
 * with their expiry and the domains each serves, a line over it while one
 * that serves domains is close to its end, and Upload in the bar, which
 * opens the form under the table. Keys are write-only. Managing them takes
 * domains edit on the whole org; the API says so to anyone else.
 */
export function CertificatesPage() {
  const certs = useQuery(certificateQueries.list());
  const [adding, setAdding] = useState(false);
  useShortcuts([
    { key: "n", label: "upload certificate", run: () => setAdding(true) },
  ]);
  const list = certs.data ?? [];
  const ending = list.filter(
    (c) =>
      c.domains.length > 0 &&
      (c.status === "expiring" || c.status === "expired"),
  );
  return (
    <>
      <OrgSettingsBar
        actions={
          !adding &&
          !certs.error && (
            <Button primary onClick={() => setAdding(true)}>
              Upload certificate <kbd>N</kbd>
            </Button>
          )
        }
      />
      <Page>
        {certs.error ? (
          <ErrorText error={certs.error} />
        ) : (
          <>
            {ending.map((c) => (
              <EndingLine key={c.id} cert={c} />
            ))}
            {list.length === 0
              ? !adding &&
                certs.isSuccess && (
                  <p className="text-mut">
                    No certificates. Domains get theirs from Let's Encrypt.
                    Upload one for a Cloudflare origin, an internal CA or a CA
                    you must use.
                  </p>
                )
              : list.length > 0 && <CertificateTable certs={list} />}
            {adding && <UploadForm onClose={() => setAdding(false)} />}
          </>
        )}
      </Page>
    </>
  );
}

/** A certificate that serves domains and is close to its end, or past it. */
function EndingLine({ cert: c }: { cert: Certificate }) {
  const left = daysLeft(c.not_after);
  const n = c.domains.length;
  const what = `${c.names[0]} ${left < 0 ? `expired on ${isoDay(c.not_after)}` : `expires in ${left} day${left === 1 ? "" : "s"}`}`;
  return (
    <p
      role="status"
      className={`rounded-ctl border px-3 py-2 ${left < 0 ? "border-fail-edge bg-fail-bg text-fail" : "border-warn-edge bg-stage-bg text-warn-text"}`}
    >
      {what} and serves {n} domain{n === 1 ? "" : "s"}.{" "}
      {left < 0
        ? "They get Let's Encrypt certificates where it can issue them; upload a renewed certificate."
        : "Upload its renewal: the one that expires last takes over at once."}
    </p>
  );
}

/** The certificates, newest first, as the API lists them. */
function CertificateTable({ certs }: { certs: Certificate[] }) {
  return (
    <Panel>
      <table>
        <thead>
          <tr>
            <th>Names</th>
            <th>Issuer</th>
            <th>Expires</th>
            <th>Serves</th>
            <th>Uploaded</th>
            <th>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {certs.map((c) => (
            <CertificateRow key={c.id} cert={c} />
          ))}
        </tbody>
      </table>
    </Panel>
  );
}

/** How far a certificate's end is, coloured by how close it is. */
function Expiry({ cert: c }: { cert: Certificate }) {
  const left = daysLeft(c.not_after);
  const when =
    left < 0
      ? "expired"
      : left >= 730
        ? `${Math.floor(left / 365)} years`
        : `${left} day${left === 1 ? "" : "s"}`;
  const tone =
    c.status === "expired"
      ? "text-fail"
      : c.status === "expiring"
        ? "text-warn"
        : "";
  return (
    <span className={tone}>
      {isoDay(c.not_after)} <span className={tone || "text-mut"}>· {when}</span>
    </span>
  );
}

/** One certificate; Remove is in its "···" menu. */
function CertificateRow({ cert: c }: { cert: Certificate }) {
  const remove = useRemoveCertificate();
  const [asking, setAsking] = useState(false);
  const cli = `dock certificate remove ${c.id}`;
  const unused = c.domains.length === 0;
  return (
    <tr>
      <td className="py-2 font-mono">
        {c.names.slice(0, shownDomains).map((n, i) => (
          <div key={n} className={i > 0 ? "text-mut" : ""}>
            {n}
          </div>
        ))}
        {c.names.length > shownDomains && (
          <div className="font-sans text-mut">
            and {c.names.length - shownDomains} more
          </div>
        )}
      </td>
      <td className="py-2">
        {c.issuer}
        {!c.public && <span className="text-mut"> · not public</span>}
      </td>
      <td className="py-2">
        <Expiry cert={c} />
      </td>
      <td className="py-2 font-mono">
        {unused ? (
          <span className="font-sans text-mut">
            {c.status === "expired"
              ? "nothing"
              : "nothing: no domain matches, or a later one covers them"}
          </span>
        ) : (
          <>
            {c.domains.slice(0, shownDomains).map((d) => (
              <div
                key={d.hostname}
                title={`${d.project}/${d.environment}/${d.service}`}
              >
                {d.hostname}
              </div>
            ))}
            {c.domains.length > shownDomains && (
              <div className="font-sans text-mut">
                and {c.domains.length - shownDomains} more (
                <span className="font-mono">dock certificate list --json</span>)
              </div>
            )}
          </>
        )}
      </td>
      <td className="py-2 text-mut">
        {c.created_by} · {isoDay(c.created_at)}
      </td>
      <td className="w-full py-1.5 text-right">
        <MoreMenu label={`More for certificate ${c.id}`}>
          {(close) => (
            <MoreItem
              close={close}
              danger
              cli={cli}
              disabled={remove.isPending}
              onClick={() => setAsking(true)}
            >
              Remove certificate…
            </MoreItem>
          )}
        </MoreMenu>
        {asking && (
          <Confirm
            danger
            action={`Remove the certificate for ${c.names[0]}`}
            cli={cli}
            pending={remove.isPending}
            error={remove.error}
            onConfirm={() =>
              remove.mutate(c.id, { onSuccess: () => setAsking(false) })
            }
            onCancel={() => {
              remove.reset();
              setAsking(false);
            }}
          >
            {unused
              ? "It serves no domain now."
              : `${hostList(c.domains.map((d) => d.hostname))} get another uploaded certificate that covers them, or Let's Encrypt certificates, at once.`}
          </Confirm>
        )}
      </td>
    </tr>
  );
}

/**
 * How many of a certificate's domains its row lists: a wildcard may serve
 * hundreds, and the rest is a count.
 */
const shownDomains = 5;

/** Names hosts in a sentence: the first few, then how many more. */
function hostList(hosts: string[]) {
  if (hosts.length <= shownDomains) return hosts.join(", ");
  return `${hosts.slice(0, shownDomains).join(", ")} and ${hosts.length - shownDomains} more`;
}

/**
 * Reads a chosen PEM file into a field, unless it is larger than the API
 * takes (limit): then it says so and leaves the field as it was.
 */
function readFile(
  file: File | undefined,
  limit: number,
  set: (text: string) => void,
  refuse: (why: string) => void,
) {
  if (!file) return;
  if (file.size > limit) {
    refuse(
      `${file.name} is larger than ${limit >> 10} KB; choose the PEM file itself`,
    );
    return;
  }
  refuse("");
  void file.text().then(set);
}

/** The API's limits on an upload (AddCertificateRequest). */
const maxChain = 64 << 10;
const maxKey = 16 << 10;

/**
 * The upload form: the chain and the key as PEM, pasted or chosen as
 * files, and an optional domain to check it against. The key is sealed
 * and never shown again.
 */
function UploadForm({ onClose }: { onClose: () => void }) {
  const add = useAddCertificate();
  const [chain, setChain] = useState("");
  const [key, setKey] = useState("");
  const [domain, setDomain] = useState("");
  // A chosen file too large to be a PEM file, not read.
  const [fileError, setFileError] = useState("");
  const host = domain.trim();
  const cli = `dock certificate add --cert chain.pem --key key.pem${host ? ` --domain ${host}` : ""}`;
  return (
    <form
      aria-label="Upload a certificate"
      className="flex flex-col gap-3 border-t border-line pt-4"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(
          { chain, key, domain: host || undefined },
          { onSuccess: onClose },
        );
      }}
    >
      <div className="grid gap-3 [grid-template-columns:repeat(auto-fit,minmax(260px,1fr))]">
        <div className="flex flex-col gap-1.5">
          <span className="flex items-baseline justify-between gap-2">
            <label htmlFor="cert-chain">Certificate chain (PEM)</label>
            <input
              type="file"
              accept=".pem,.crt,.cer,text/plain"
              aria-label="Choose the chain file"
              className="max-w-44 text-xs text-mut"
              onChange={(e) =>
                readFile(e.target.files?.[0], maxChain, setChain, setFileError)
              }
            />
          </span>
          <textarea
            id="cert-chain"
            rows={6}
            value={chain}
            onChange={(e) => setChain(e.target.value)}
            placeholder="-----BEGIN CERTIFICATE-----"
            spellCheck={false}
            autoComplete="off"
            className="field font-mono text-xs"
          />
        </div>
        <div className="flex flex-col gap-1.5">
          <span className="flex items-baseline justify-between gap-2">
            <label htmlFor="cert-key">Private key (PEM)</label>
            <input
              type="file"
              accept=".pem,.key,text/plain"
              aria-label="Choose the key file"
              className="max-w-44 text-xs text-mut"
              onChange={(e) =>
                readFile(e.target.files?.[0], maxKey, setKey, setFileError)
              }
            />
          </span>
          <textarea
            id="cert-key"
            rows={6}
            value={key}
            onChange={(e) => setKey(e.target.value)}
            placeholder="-----BEGIN PRIVATE KEY-----"
            spellCheck={false}
            autoComplete="off"
            className="field font-mono text-xs"
          />
        </div>
      </div>
      <div className="flex flex-wrap items-center gap-2.5">
        <input
          aria-label="Check it covers this domain (optional)"
          placeholder="check it covers a domain (optional)"
          value={domain}
          onChange={(e) => setDomain(e.target.value)}
          spellCheck={false}
          className="field w-72 font-mono"
        />
        <Act
          type="submit"
          primary
          cli={cli}
          disabled={add.isPending || !chain.trim() || !key.trim()}
        >
          Upload
        </Act>
        <Button onClick={onClose}>Cancel</Button>
        <span className="text-mut">
          The key is stored encrypted and never shown again.
        </span>
      </div>
      {fileError && (
        <span role="alert" className="text-fail">
          {fileError}
        </span>
      )}
      <ErrorText error={add.error} />
    </form>
  );
}
