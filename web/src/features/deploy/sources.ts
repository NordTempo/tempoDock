import { ApiError, currentOrg } from "../../api/client";
import type { Release, ServiceRef } from "../../data/types";
import type { Entry, List } from "./pack";

// Where a deploy's source comes from in the browser, and its upload.

/** A folder to pack: its name and a lister for pack(). */
export type Folder = { name: string; list: List };

type Tree = { dirs: Map<string, Tree>; files: Map<string, File> };

/**
 * A folder picked with <input webkitdirectory>. The browser lists every
 * file up front, with paths like "app/src/main.ts"; empty directories are
 * not listed.
 */
export function fromFileList(files: Iterable<File>): Folder | null {
  const root: Tree = { dirs: new Map(), files: new Map() };
  let name: string | undefined;
  for (const file of files) {
    const [top, ...parts] = file.webkitRelativePath.split("/");
    const base = parts.pop();
    if (!top || !base) continue;
    name ??= top;
    let node = root;
    for (const part of parts) {
      let next = node.dirs.get(part);
      if (!next) {
        next = { dirs: new Map(), files: new Map() };
        node.dirs.set(part, next);
      }
      node = next;
    }
    node.files.set(base, file);
  }
  if (!name) return null;
  const list =
    (node: Tree): List =>
    async () => [
      ...[...node.dirs].map(
        ([dir, sub]): Entry => ({ kind: "dir", name: dir, list: list(sub) }),
      ),
      ...[...node.files].map(
        ([file, f]): Entry => ({
          kind: "file",
          name: file,
          file: async () => f,
        }),
      ),
    ];
  return { name, list: list(root) };
}

const isDir = (e: FileSystemEntry): e is FileSystemDirectoryEntry =>
  e.isDirectory;
const isFile = (e: FileSystemEntry): e is FileSystemFileEntry => e.isFile;

/** A dropped folder, read lazily: ignored directories are never listed. */
export function fromDirectoryEntry(dir: FileSystemDirectoryEntry): Folder {
  const list =
    (d: FileSystemDirectoryEntry): List =>
    async () => {
      const reader = d.createReader();
      const out: Entry[] = [];
      // readEntries returns a batch at a time, then an empty one.
      for (;;) {
        const batch = await new Promise<FileSystemEntry[]>((resolve, reject) =>
          reader.readEntries(resolve, reject),
        );
        if (batch.length === 0) return out;
        for (const e of batch) {
          if (isDir(e)) out.push({ kind: "dir", name: e.name, list: list(e) });
          else if (isFile(e)) {
            out.push({
              kind: "file",
              name: e.name,
              file: () =>
                new Promise((resolve, reject) => e.file(resolve, reject)),
            });
          }
        }
      }
    };
  return { name: dir.name, list: list(dir) };
}

/** The upload's Content-Type for an archive, or null for other files. */
export function archiveType(file: File) {
  if (/\.(tar\.gz|tgz)$/i.test(file.name)) return "application/gzip";
  if (/\.tar$/i.test(file.name)) return "application/x-tar";
  return null;
}

/** Bytes sent of the whole. */
export type Sent = { loaded: number; total: number };

/**
 * Uploads a source archive to POST .../releases/source with the session
 * cookie. XMLHttpRequest, not fetch: it reports upload progress and sends
 * a Blob over HTTP/1.1, where browsers can't stream a request body.
 */
export function uploadSource(
  ref: ServiceRef,
  archive: Blob,
  type: string,
  onProgress: (sent: Sent) => void,
  signal: AbortSignal,
): Promise<Release> {
  const path = [ref.project, ref.environment, ref.service].map(
    encodeURIComponent,
  );
  const url = `/api/v1/projects/${path[0]}/environments/${path[1]}/services/${path[2]}/releases/source`;
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", url);
    xhr.setRequestHeader("Content-Type", type);
    const org = currentOrg();
    if (org) xhr.setRequestHeader("Dock-Org", org);
    xhr.responseType = "json";
    xhr.upload.onprogress = (e) =>
      onProgress({ loaded: e.loaded, total: e.total || archive.size });
    xhr.onload = () => {
      const res: unknown = xhr.response;
      if (xhr.status === 202 && isRelease(res)) return resolve(res);
      reject(problem(xhr.status, res));
    };
    xhr.onerror = () =>
      reject(
        new Error(
          "The upload was cut off. If a deploy is running, wait for it, then try again.",
        ),
      );
    xhr.onabort = () => reject(new DOMException("Cancelled", "AbortError"));
    signal.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(archive);
  });
}

const isRelease = (v: unknown): v is Release =>
  typeof v === "object" && v !== null && "number" in v && "status" in v;

/** The API's problem document as an ApiError, as api/client does. */
function problem(status: number, body: unknown) {
  if (typeof body !== "object" || body === null) {
    return new ApiError(status, "", `Upload failed (HTTP ${status})`);
  }
  const p: { detail?: unknown; code?: unknown } = body;
  return new ApiError(
    status,
    typeof p.code === "string" ? p.code : "",
    typeof p.detail === "string" ? p.detail : `Upload failed (HTTP ${status})`,
  );
}
