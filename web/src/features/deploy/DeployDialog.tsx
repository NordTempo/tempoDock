import {
  type QueryClient,
  useMutation,
  useQueryClient,
} from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import {
  type FormEvent,
  useEffect,
  useEffectEvent,
  useRef,
  useState,
} from "react";
import { api, body } from "../../api/client";
import { useBuildLog } from "../../data/live";
import {
  keys,
  servicePath,
  useDeployBlock,
  useReleases,
  useService,
} from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { useOrg } from "../../shell/location";
import { Dialog } from "../../ui/Dialog";
import { plural, size } from "../../ui/format";
import { Act, Button, DialogFoot, ErrorText, StateText } from "../../ui/kit";
import { failurePhrase, releaseState, stepPhrase } from "../../ui/status";
import {
  commitText,
  type GitConnection,
  gitDeployCli,
  targetText,
  useGitDeploy,
  useGitSource,
} from "../git/api";
import { errorSummary, imageSetting, shortImage } from "../service/releases";
import { type PackStats, pack } from "./pack";
import {
  archiveType,
  type Folder,
  fromDirectoryEntry,
  fromFileList,
  type Sent,
  uploadSource,
} from "./sources";
import type { DeployMode, DeployStart, Dropped } from "./start";

/**
 * The Deploy window: a folder (packed here like `dock deploy`), an image,
 * or the connected GitHub branch. Once a release starts it follows the
 * build log. Loaded on first open (start.tsx).
 */
export default function DeployDialog({
  serviceRef: ref,
  start,
  onClose,
}: {
  serviceRef: ServiceRef;
  start: DeployStart;
  onClose: () => void;
}) {
  const view = useService(ref);
  const blocked = useDeployBlock(ref);
  // The release this window started, once it has; then it follows it.
  const [release, setRelease] = useState<number | null>(null);
  // Not refetched on every release event while following one.
  const git = useGitSource(ref, release === null);
  const conn = git.data?.connection;
  const [chosen, setChosen] = useState(start.dropped ? "folder" : start.mode);
  const mode: DeployMode =
    chosen ?? (view.service.image ? "image" : conn ? "git" : "folder");
  // A source or image deploy is refused while another one runs.
  const busy =
    blocked ??
    (view.deploying
      ? `r${view.deploying.number} is deploying; deploy again once it finishes`
      : undefined);

  const modes: [DeployMode, string][] = [
    ["folder", "Folder"],
    ["image", "Image"],
    ...(conn ? [["git", "GitHub"] satisfies [DeployMode, string]] : []),
  ];

  return (
    <Dialog
      title={`Deploy ${ref.service} to ${ref.environment}`}
      onClose={onClose}
      wide
    >
      {release !== null ? (
        <Following serviceRef={ref} number={release} onClose={onClose} />
      ) : (
        <div className="flex flex-col gap-3">
          <fieldset aria-label="Deploy from" className="seg self-start">
            {modes.map(([m, label]) => (
              <button
                key={m}
                type="button"
                aria-pressed={m === mode}
                onClick={() => setChosen(m)}
              >
                {label}
              </button>
            ))}
          </fieldset>
          {mode === "folder" && (
            <FolderDeploy
              serviceRef={ref}
              dropped={start.dropped}
              busy={busy}
              onStarted={setRelease}
            />
          )}
          {mode === "image" && (
            <ImageDeploy
              serviceRef={ref}
              busy={busy}
              onStarted={setRelease}
              onClose={onClose}
            />
          )}
          {mode === "git" && conn && (
            <GitDeploy serviceRef={ref} conn={conn} onClose={onClose} />
          )}
        </div>
      )}
    </Dialog>
  );
}

/**
 * Marks the control the window focuses when it opens: the native <dialog>
 * focuses an [autofocus] element, and React's autoFocus doesn't set one.
 */
const autofocus = (el: HTMLElement | null) => el?.setAttribute("autofocus", "");

/** What a service's deploy changes: its view, the overview and the audit log. */
const touched = (client: QueryClient, ref: ServiceRef) =>
  Promise.all(
    [keys.service(ref), keys.overview(ref.project), ["audit"]].map((queryKey) =>
      client.invalidateQueries({ queryKey }),
    ),
  );

type Source = { folder: Folder } | { file: File };

type Phase =
  | { step: "choose" }
  | { step: "packing"; name: string; stats: PackStats; compressed: number }
  | { step: "uploading"; name: string; summary: string; sent: Sent }
  | { step: "failed"; error: Error };

/** The server's upload limit, from /version; 0 when unknown. */
const maxUpload = (client: QueryClient) =>
  client
    .fetchQuery({
      queryKey: ["version"],
      queryFn: () => body(api.GET("/api/v1/version")),
      staleTime: Number.POSITIVE_INFINITY,
    })
    .then((v) => v.max_upload_bytes)
    .catch(() => 0);

const tooLarge = (max: number) =>
  new Error(
    `The archive is larger than the server's limit of ${Math.floor(max / 2 ** 20)} MB. Add large files to .dockerignore, or deploy an image.`,
  );

/**
 * Packs a folder into a gzip Blob, stopping once it passes the server's
 * limit. A Blob, not a stream: browsers upload streams only over HTTP/2
 * and without progress. Only the compressed archive is held; the files
 * are read a chunk at a time.
 */
async function packToBlob(
  folder: Folder,
  dockerfile: string,
  max: number,
  onProgress: (stats: PackStats, compressed: number) => void,
  signal: AbortSignal,
) {
  let compressed = 0;
  const { stream, stats } = pack(folder.list, {
    dockerfile,
    onProgress: () => onProgress(stats, compressed),
  });
  // Read here, not with Response.blob(): it replaces the packer's errors
  // ("x: its size changed") with a bare "Failed to fetch".
  const reader = stream.getReader();
  const parts: Uint8Array<ArrayBuffer>[] = [];
  try {
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      signal.throwIfAborted();
      compressed += value.byteLength;
      if (max && compressed > max) throw tooLarge(max);
      parts.push(value);
    }
  } catch (e) {
    // Not awaited: with the gzip stage backed up, cancelling settles late.
    reader.cancel().catch(() => {});
    throw e;
  }
  return { blob: new Blob(parts), stats, compressed };
}

/** Pick or drop a folder or archive; pack, upload, then hand over the release. */
function FolderDeploy({
  serviceRef: ref,
  dropped,
  busy,
  onStarted,
}: {
  serviceRef: ServiceRef;
  dropped: Dropped | undefined;
  busy: string | undefined;
  onStarted: (release: number) => void;
}) {
  const client = useQueryClient();
  const { service } = useService(ref);
  const [phase, setPhase] = useState<Phase>({ step: "choose" });
  const folderInput = useRef<HTMLInputElement | null>(null);
  const fileInput = useRef<HTMLInputElement>(null);
  const abort = useRef<AbortController | null>(null);
  // Packing reports every file; the screen updates once a frame.
  const frame = useRef(0);
  const later = useRef<Phase | null>(null);
  const showLater = (p: Phase) => {
    later.current = p;
    frame.current ||= requestAnimationFrame(() => {
      frame.current = 0;
      if (later.current) setPhase(later.current);
    });
  };
  const show = (p: Phase) => {
    cancelAnimationFrame(frame.current);
    frame.current = 0;
    later.current = null;
    setPhase(p);
  };

  const run = async (src: Source) => {
    abort.current?.abort();
    const ac = new AbortController();
    abort.current = ac;
    try {
      if (busy) throw new Error(busy);
      const max = await maxUpload(client);
      let archive: Blob;
      let type: string;
      let name: string;
      let summary: string;
      if ("file" in src) {
        const t = archiveType(src.file);
        if (!t) {
          throw new Error(
            `${src.file.name} is not a folder or a .tar.gz, .tgz or .tar archive.`,
          );
        }
        if (max && src.file.size > max) throw tooLarge(max);
        archive = src.file;
        type = t;
        name = src.file.name;
        summary = size(src.file.size);
      } else {
        name = `${src.folder.name}/`;
        const none = { files: 0, bytes: 0, notes: [] };
        show({ step: "packing", name, stats: none, compressed: 0 });
        const packed = await packToBlob(
          src.folder,
          service.dockerfile_path,
          max,
          (stats, compressed) =>
            showLater({
              step: "packing",
              name,
              stats: { ...stats },
              compressed,
            }),
          ac.signal,
        );
        const { stats } = packed;
        archive = packed.blob;
        type = "application/gzip";
        summary = `${plural(stats.files, "file")}, ${size(stats.bytes)} (${size(packed.compressed)} compressed)${stats.notes.length ? `, using ${stats.notes.join(", ")}` : ""}`;
      }
      const sent = { loaded: 0, total: archive.size };
      show({ step: "uploading", name, summary, sent });
      const release = await uploadSource(
        ref,
        archive,
        type,
        (s) => setPhase({ step: "uploading", name, summary, sent: s }),
        ac.signal,
      );
      await touched(client, ref);
      onStarted(release.number);
    } catch (e) {
      if (ac.signal.aborted) return;
      show({
        step: "failed",
        error: e instanceof Error ? e : new Error(String(e)),
      });
    }
  };

  const startDropped = useEffectEvent(() => {
    if (!dropped) return;
    void run(
      "folder" in dropped
        ? { folder: fromDirectoryEntry(dropped.folder) }
        : { file: dropped.file },
    );
  });
  useEffect(() => {
    startDropped();
    return () => {
      abort.current?.abort();
      cancelAnimationFrame(frame.current);
    };
  }, []);

  const working = phase.step === "packing" || phase.step === "uploading";
  const cli = `dock deploy ${phase.step === "choose" || phase.step === "failed" ? "<dir>" : phase.name} -s ${ref.service} -e ${ref.environment}`;
  return (
    <div className="flex flex-col gap-3">
      <input
        ref={(el) => {
          folderInput.current = el;
          el?.setAttribute("webkitdirectory", "");
        }}
        type="file"
        hidden
        onChange={(e) => {
          const folder = fromFileList(e.target.files ?? []);
          e.target.value = "";
          if (folder) void run({ folder });
        }}
      />
      <input
        ref={fileInput}
        type="file"
        accept=".tar.gz,.tgz,.tar"
        hidden
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          if (file) void run({ file });
        }}
      />
      <Progress phase={phase} />
      {!working && <Cutover serviceRef={ref} />}
      <div className="flex flex-wrap items-start gap-2">
        {working ? (
          <Button
            onClick={() => {
              abort.current?.abort();
              show({ step: "choose" });
            }}
          >
            Cancel
          </Button>
        ) : (
          <>
            <Act
              ref={autofocus}
              primary
              cli={cli}
              disabled={!!busy}
              onClick={() => folderInput.current?.click()}
            >
              Choose folder
            </Act>
            <Button
              disabled={!!busy}
              onClick={() => fileInput.current?.click()}
            >
              Choose .tar.gz
            </Button>
            <span className="self-center text-xs text-mut">
              or drop one onto the page
            </span>
          </>
        )}
      </div>
      {busy && !working && <p className="text-xs text-fail">{busy}.</p>}
      {/* mt-4 leaves room for the CLI line under the focused button. */}
      <p className="mt-4 text-xs text-faint">
        Packed here like <code className="font-mono">dock deploy</code>:
        .dockerignore, else .gitignore files.
      </p>
    </div>
  );
}

/** Packing and upload progress as numbers and a static bar. */
function Progress({ phase }: { phase: Phase }) {
  switch (phase.step) {
    case "choose":
      return null;
    case "failed":
      return <ErrorText error={phase.error} />;
    case "packing":
      return (
        <p className="font-mono text-xs" aria-live="polite">
          Packing {phase.name} {plural(phase.stats.files, "file")},{" "}
          {size(phase.stats.bytes)}, {size(phase.compressed)} compressed
        </p>
      );
    case "uploading": {
      const { loaded, total } = phase.sent;
      const pct = total ? Math.floor((loaded / total) * 100) : 0;
      return (
        <div className="flex flex-col gap-1.5" aria-live="polite">
          <p className="font-mono text-xs">
            Uploading {phase.name} {size(loaded)} of {size(total)} ({pct}%)
          </p>
          <div
            role="progressbar"
            aria-label="Upload"
            aria-valuenow={pct}
            className="h-1 overflow-hidden rounded-full bg-line"
          >
            <div className="h-full bg-fg" style={{ width: `${pct}%` }} />
          </div>
          <p className="text-xs text-mut">{phase.summary}</p>
        </div>
      );
    }
  }
}

/**
 * Deploys an image, by default the service's image setting again; the
 * serving image instead when the setting's image failed last time, with
 * the other one a click away. A known-failed image is never the primary.
 */
function ImageDeploy({
  serviceRef: ref,
  busy,
  onStarted,
  onClose,
}: {
  serviceRef: ServiceRef;
  busy: string | undefined;
  onStarted: (release: number) => void;
  onClose: () => void;
}) {
  const client = useQueryClient();
  const { service, latest_release } = useService(ref);
  const releases = useReleases(ref);
  const setting = imageSetting(service, releases);
  const serving = setting.serving;
  const next = (latest_release?.number ?? 0) + 1;
  const current = service.image;
  const servingImage = setting.serving?.image_ref ?? "";
  const [image, setImage] = useState(
    setting.differs && setting.failed ? servingImage : current,
  );
  const wanted = image.trim();
  // The newest release of this very image, when it failed: say so before
  // the same deploy runs again.
  const lastOfImage = releases.find((r) => r.image_ref === wanted);
  const failedBefore =
    lastOfImage?.status === "failed" ? lastOfImage : undefined;
  // The image setting and the serving image, when they differ: one click.
  const choices = setting.differs
    ? [
        { ref: servingImage, label: `serving as r${setting.serving?.number}` },
        {
          ref: current,
          label: setting.failed
            ? `failed as r${setting.failed.number}`
            : "next deploy",
        },
      ]
    : [];
  const deploy = useMutation({
    mutationFn: (img: string) =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/releases",
          { params: { path: servicePath(ref) }, body: { image: img } },
        ),
      ),
    onSuccess: async (release) => {
      await touched(client, ref);
      onStarted(release.number);
    },
  });
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (wanted) deploy.mutate(wanted);
  };
  return (
    <form className="flex flex-col gap-3" onSubmit={submit}>
      <label className="flex flex-col gap-1">
        <span className="text-xs text-faint">Image</span>
        <input
          ref={autofocus}
          value={image}
          placeholder="ghcr.io/acme/web:1.4"
          spellCheck={false}
          onChange={(e) => setImage(e.target.value)}
          className="field w-full font-mono text-xs"
        />
      </label>
      {choices.length > 0 && (
        <p className="flex flex-wrap items-center gap-2 text-xs">
          {choices.map((c) => (
            <Button
              key={c.ref}
              small
              aria-pressed={wanted === c.ref}
              onClick={() => setImage(c.ref)}
            >
              <span className="font-mono">{shortImage(c.ref)}</span>{" "}
              <span className="text-mut">{c.label}</span>
            </Button>
          ))}
        </p>
      )}
      {failedBefore && (
        <p className="text-xs text-warn-text">
          r{failedBefore.number} {failurePhrase(failedBefore)} with this image
          {failedBefore.error ? `: ${failedBefore.error}` : "."}
        </p>
      )}
      {/* What it deploys, what serves now, and whether that means downtime,
          in the words the rollback confirm uses. */}
      {wanted && (
        <div className="flex flex-col gap-1 text-xs text-sec [&_b]:font-semibold [&_b]:text-fg">
          <p>
            Deploys <span className="font-mono">{shortImage(wanted)}</span> as r
            {next}
            {serving && (
              <>
                , in place of r{serving.number} (
                <span className="font-mono">{shortImage(setting.now)}</span>)
              </>
            )}
            .
            {wanted !== current &&
              (current
                ? ` Later deploys use it too, not ${shortImage(current)}.`
                : " Later deploys use it too.")}
          </p>
          <Cutover serviceRef={ref} />
        </div>
      )}
      <DialogFoot
        className="mt-1"
        cli={`dock deploy --image ${wanted || "<image>"} -s ${ref.service} -e ${ref.environment}`}
        note={
          <>
            {busy && <span className="text-fail">{busy}. </span>}
            {!wanted && !busy && (
              <span className="text-faint">Type an image first</span>
            )}
            <ErrorText error={deploy.error} />
          </>
        }
      >
        <Button onClick={onClose}>Cancel</Button>
        <Button
          primary={!failedBefore}
          type="submit"
          disabled={!wanted || !!busy || deploy.isPending}
          title={busy}
        >
          {failedBefore ? "Deploy failed image again" : "Deploy image"}
        </Button>
      </DialogFoot>
    </form>
  );
}

/**
 * What the new release does to the serving one, in the words the rollback
 * confirm uses: a service with a volume is down while the old container
 * stops; others keep serving until the new release passes its check.
 */
function Cutover({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service } = useService(ref);
  const serving = service.active_release;
  if (!serving || service.stopped_at) return null;
  return service.stateful ? (
    <p className="text-xs [&_b]:font-semibold [&_b]:text-fg">
      <b>
        {ref.service} is down for a few seconds while r{serving} stops and the
        new release starts: only one copy can use its volume at a time.
      </b>
    </p>
  ) : (
    <p className="text-xs text-sec">
      r{serving} keeps serving until the new release passes its health check, so{" "}
      {ref.service} stays reachable.
    </p>
  );
}

/** Queues the connected branch's head, or another commit, branch or tag. */
function GitDeploy({
  serviceRef: ref,
  conn,
  onClose,
}: {
  serviceRef: ServiceRef;
  conn: GitConnection;
  onClose: () => void;
}) {
  const [gitRef, setGitRef] = useState("");
  const deploy = useGitDeploy(ref);
  const target = gitRef.trim();
  if (deploy.data) {
    return (
      <div className="flex flex-col gap-3">
        <p>
          Queued{" "}
          <span className="font-mono">{commitText(deploy.data.commit)}</span>.{" "}
          <span className="text-mut">
            It builds once no other deploy of {ref.service} runs.
          </span>
        </p>
        <Button className="self-start" onClick={onClose}>
          Close
        </Button>
      </div>
    );
  }
  return (
    <form
      className="flex flex-col gap-3"
      onSubmit={(e) => {
        e.preventDefault();
        deploy.mutate(target);
      }}
    >
      <p className="font-mono text-xs">{targetText(conn)}</p>
      <Cutover serviceRef={ref} />
      <label className="flex flex-col gap-1">
        <span className="text-xs text-faint">Commit, branch or tag</span>
        <input
          ref={autofocus}
          value={gitRef}
          placeholder={`${conn.branch} head`}
          spellCheck={false}
          onChange={(e) => setGitRef(e.target.value)}
          className="field w-full font-mono text-xs"
        />
      </label>
      <DialogFoot
        className="mt-1"
        cli={gitDeployCli(ref, target)}
        note={<ErrorText error={deploy.error} />}
      >
        <Button onClick={onClose}>Cancel</Button>
        <Button primary type="submit" disabled={deploy.isPending}>
          Deploy {target || conn.branch}
        </Button>
      </DialogFoot>
    </form>
  );
}

/** A started release: its step and the tail of its build log, live. */
function Following({
  serviceRef: ref,
  number,
  onClose,
}: {
  serviceRef: ServiceRef;
  number: number;
  onClose: () => void;
}) {
  const org = useOrg();
  const release = useReleases(ref).find((r) => r.number === number);
  const log = useBuildLog(ref, number);
  // Newest first in the stream; the last lines, oldest first, here.
  const lines = log.lines.slice(0, 14).reverse();
  const status = release?.status ?? "queued";
  return (
    <div className="flex flex-col gap-3">
      <p aria-live="polite">
        <span className="font-mono">r{number}</span>{" "}
        {release?.status === "failed" ? (
          <span className="text-fail">
            {failurePhrase(release)}: {errorSummary(release)}
          </span>
        ) : (
          <>
            <StateText state={releaseState(status)} />{" "}
            <span className="text-mut">{stepPhrase[status]}</span>
          </>
        )}
      </p>
      <div
        role="log"
        aria-label={`r${number} build log`}
        className="h-[260px] overflow-hidden rounded-ctl border border-line bg-side px-3 py-2 font-mono text-2xs leading-[1.6]"
      >
        {lines.length === 0 && (
          <div className="text-mut">
            {log.live
              ? "Waiting for build output."
              : (log.error ?? "No build output.")}
          </div>
        )}
        {lines.map((line) => (
          <div key={line.key} className="truncate whitespace-pre">
            {line.text}
          </div>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <Link
          to="/$org/$project/$environment/$service/history/$number"
          params={{ org, ...ref, number: String(number) }}
          className="btn"
          onClick={onClose}
        >
          Open release
        </Link>
        <Link
          to="/$org/$project/$environment/$service/logs"
          params={{ org, ...ref }}
          search={{ release: number, build: true }}
          className="btn"
          onClick={onClose}
        >
          Full build log
        </Link>
        <Button ref={(el) => el?.focus()} onClick={onClose}>
          Close
        </Button>
      </div>
    </div>
  );
}
