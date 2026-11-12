import { type FormEvent, useState } from "react";
import { since } from "../../ui/format";
import {
  Act,
  Button,
  Confirm,
  ErrorText,
  Page,
  PageBar,
  Panel,
  SectionHead,
} from "../../ui/kit";
import {
  type NewStorage,
  type Storage,
  type StorageChange,
  useAddStorage,
  useRemoveStorage,
  useStorage,
  useUpdateStorage,
} from "./data";

// The console's Storage page (ADR 0034): S3-compatible buckets defined
// once, by name, and what keeps data in each. Keys are write-only.

export function StoragePage() {
  const storage = useStorage();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<string>();
  const edited = storage.find((s) => s.name === editing);
  return (
    <>
      <PageBar
        title="Storage"
        actions={
          !adding && (
            <Button
              primary
              onClick={() => {
                setEditing(undefined);
                setAdding(true);
              }}
            >
              Add storage bucket
            </Button>
          )
        }
      />
      <Page>
        <section aria-labelledby="connections" className="flex flex-col gap-2">
          <SectionHead id="connections">Storage buckets</SectionHead>
          {storage.length > 0 ? (
            <Panel>
              <table>
                <thead>
                  <tr>
                    <th>Name</th>
                    <th>Endpoint</th>
                    <th>Bucket</th>
                    <th>Region</th>
                    <th>Addressing</th>
                    <th>Used by</th>
                    <th>Changed</th>
                    <th />
                  </tr>
                </thead>
                <tbody>
                  {storage.map((s) => (
                    <StorageRow
                      key={s.name}
                      storage={s}
                      onEdit={() => {
                        setAdding(false);
                        setEditing(s.name);
                      }}
                    />
                  ))}
                </tbody>
              </table>
            </Panel>
          ) : (
            !adding && (
              <p className="py-2 text-mut">
                No storage buckets yet. An S3-compatible bucket can hold the
                registry's images and the backups; until then they stay on the
                servers' disks. Add one with Add storage bucket.
              </p>
            )
          )}
          {adding && <StorageForm onClose={() => setAdding(false)} />}
          {edited && (
            <StorageForm
              key={edited.name}
              storage={edited}
              onClose={() => setEditing(undefined)}
            />
          )}
        </section>
      </Page>
    </>
  );
}

function StorageRow({
  storage: s,
  onEdit,
}: {
  storage: Storage;
  onEdit: () => void;
}) {
  const remove = useRemoveStorage();
  const [confirming, setConfirming] = useState(false);
  const used = (s.used_by ?? []).length > 0;
  return (
    <tr>
      <td className="py-2 font-mono">{s.name}</td>
      <td className="py-2 font-mono">{s.endpoint}</td>
      <td className="py-2 font-mono">{s.bucket}</td>
      <td className="py-2 font-mono">
        {s.region || <span className="text-faint">·</span>}
      </td>
      <td className="py-2 text-mut">{s.path_style ? "path" : "host"}</td>
      <td className="py-2">
        {used ? (
          (s.used_by ?? []).join(", ")
        ) : (
          <span className="text-faint">·</span>
        )}
      </td>
      <td className="py-2 text-mut">
        {since(s.updated_at)} ago · {s.updated_by}
      </td>
      <td className="w-full py-1.5 text-right">
        <span className="inline-flex items-center gap-3">
          <Button small onClick={onEdit}>
            Change
          </Button>
          <Button
            danger
            small
            disabled={used}
            reason="in use; move the registry's images first"
            onClick={() => setConfirming(true)}
          >
            Remove
          </Button>
          {confirming && (
            <Confirm
              danger
              action={`Remove ${s.name}`}
              cli={`dock storage remove ${s.name}`}
              pending={remove.isPending}
              error={remove.error}
              onConfirm={() =>
                remove.mutate(s.name, { onSuccess: () => setConfirming(false) })
              }
              onCancel={() => {
                remove.reset();
                setConfirming(false);
              }}
            >
              Dock forgets the connection to <b>{s.bucket}</b>. The bucket and
              what is in it stay as they are.
            </Confirm>
          )}
        </span>
      </td>
    </tr>
  );
}

/**
 * Adds a connection, or changes one (storage). When changing, blank keys
 * keep the stored ones and only changed fields are sent; the endpoint and
 * bucket of storage in use can't change.
 */
function StorageForm({
  storage,
  onClose,
}: {
  storage?: Storage;
  onClose: () => void;
}) {
  const add = useAddStorage();
  const update = useUpdateStorage();
  const [v, setV] = useState({
    name: storage?.name ?? "",
    endpoint: storage?.endpoint ?? "",
    region: storage?.region ?? "",
    bucket: storage?.bucket ?? "",
    pathStyle: storage?.path_style ?? true,
    keyId: "",
    secret: "",
  });
  const set =
    (k: "name" | "endpoint" | "region" | "bucket" | "keyId" | "secret") =>
    (e: { target: { value: string } }) =>
      setV((s) => ({ ...s, [k]: e.target.value }));
  const inUse = (storage?.used_by?.length ?? 0) > 0;
  const pending = add.isPending || update.isPending;

  const submit = (e: FormEvent) => {
    e.preventDefault();
    const t = {
      endpoint: v.endpoint.trim(),
      region: v.region.trim(),
      bucket: v.bucket.trim(),
    };
    if (!storage) {
      const input: NewStorage = {
        name: v.name.trim(),
        ...t,
        path_style: v.pathStyle,
        access_key_id: v.keyId.trim(),
        secret_access_key: v.secret.trim(),
      };
      add.mutate(input, { onSuccess: onClose });
      return;
    }
    const change: StorageChange = {
      ...(v.name.trim() !== storage.name && { name: v.name.trim() }),
      ...(t.endpoint !== storage.endpoint && { endpoint: t.endpoint }),
      ...(t.region !== storage.region && { region: t.region }),
      ...(t.bucket !== storage.bucket && { bucket: t.bucket }),
      ...(v.pathStyle !== storage.path_style && { path_style: v.pathStyle }),
      ...(v.keyId.trim() && { access_key_id: v.keyId.trim() }),
      ...(v.secret.trim() && { secret_access_key: v.secret.trim() }),
    };
    update.mutate({ name: storage.name, change }, { onSuccess: onClose });
  };

  // The CLI equivalent: add, or a rename or key change of this one.
  const cli = !storage
    ? `dock storage add ${v.name.trim() || "<name>"} --endpoint ${v.endpoint.trim() || "<url>"} --bucket ${v.bucket.trim() || "<bucket>"}${v.region.trim() ? ` --region ${v.region.trim()}` : ""}${v.pathStyle ? "" : " --path-style=false"} --access-key-id … --secret-access-key-stdin`
    : v.name.trim() !== storage.name
      ? `dock storage rename ${storage.name} ${v.name.trim()}`
      : `dock storage set ${storage.name} --access-key-id … --secret-access-key-stdin`;
  return (
    <form
      aria-label={
        storage ? `Change storage bucket ${storage.name}` : "Add storage bucket"
      }
      className="flex max-w-[760px] flex-col gap-3.5 border-t border-line pt-3.5"
      onKeyDown={(e) => e.key === "Escape" && onClose()}
      onSubmit={submit}
    >
      <div className="flex flex-wrap gap-3">
        <label className="flex flex-[1_1_160px] flex-col gap-1.5">
          <span>Name</span>
          <input
            // biome-ignore lint/a11y/noAutofocus: the form opens on request
            autoFocus
            value={v.name}
            onChange={set("name")}
            placeholder="hetzner"
            className="field font-mono"
          />
        </label>
        <label className="flex flex-[3_1_320px] flex-col gap-1.5">
          <span>
            Endpoint <span className="text-mut">base URL, no bucket</span>
          </span>
          <input
            value={v.endpoint}
            onChange={set("endpoint")}
            disabled={inUse}
            placeholder="https://fsn1.your-objectstorage.com"
            className="field font-mono"
          />
        </label>
      </div>
      <div className="flex flex-wrap gap-3">
        <label className="flex flex-[2_1_200px] flex-col gap-1.5">
          <span>Bucket</span>
          <input
            value={v.bucket}
            onChange={set("bucket")}
            disabled={inUse}
            placeholder="dock-images"
            className="field font-mono"
          />
        </label>
        <label className="flex flex-[1_1_140px] flex-col gap-1.5">
          <span>
            Region <span className="text-mut">empty: us-east-1</span>
          </span>
          <input
            value={v.region}
            onChange={set("region")}
            placeholder="fsn1"
            className="field font-mono"
          />
        </label>
      </div>
      <div className="flex flex-wrap gap-3">
        <label className="flex flex-[1_1_180px] flex-col gap-1.5">
          <span>Access key ID</span>
          <input
            value={v.keyId}
            onChange={set("keyId")}
            placeholder={storage ? "unchanged" : ""}
            autoComplete="off"
            className="field font-mono"
          />
        </label>
        <label className="flex flex-[1_1_180px] flex-col gap-1.5">
          <span>Secret access key</span>
          <input
            type="password"
            value={v.secret}
            onChange={set("secret")}
            placeholder={storage ? "unchanged" : ""}
            autoComplete="new-password"
            className="field font-mono"
          />
        </label>
      </div>
      <label className="flex items-center gap-2">
        <input
          type="checkbox"
          checked={v.pathStyle}
          onChange={(e) => setV((s) => ({ ...s, pathStyle: e.target.checked }))}
        />
        <span>
          Path-style addressing{" "}
          <span className="text-mut">
            https://endpoint/bucket; off puts the bucket in the host name
          </span>
        </span>
      </label>
      {inUse && (
        <p className="text-mut">
          The registry keeps its images here: new keys restart it for a second.
          Move the images with{" "}
          <span className="font-mono">dock registry migrate</span> to change the
          endpoint or bucket.
        </p>
      )}
      <ErrorText error={add.error ?? update.error} />
      <div className="flex items-center gap-2.5">
        <Act primary type="submit" cli={cli} disabled={pending}>
          {pending
            ? "Checking the bucket…"
            : storage
              ? "Save"
              : "Add storage bucket"}
        </Act>
        <Button onClick={onClose}>Cancel</Button>
      </div>
    </form>
  );
}
