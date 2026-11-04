import {
  lazy,
  Suspense,
  useEffect,
  useEffectEvent,
  useState,
  useSyncExternalStore,
} from "react";
import type { ServiceRef } from "../../data/types";

// What the shell needs to start a deploy: opening the Deploy window from
// anywhere (the service header, the palette, a release page), reading a
// drop, and the drop target. The window itself and the packer load only
// when it opens.

export type DeployMode = "folder" | "image" | "git";

/** Something dropped onto the service: a folder, or an archive file. */
export type Dropped = { folder: FileSystemDirectoryEntry } | { file: File };

/** How the Deploy window opens: on a mode, or with what was dropped. */
export type DeployStart = { mode?: DeployMode; dropped?: Dropped };

const Window = lazy(() => import("./DeployDialog"));

type Open = { serviceRef: ServiceRef; start: DeployStart; n: number };

// The open window lives outside React, like the create dialogs, so the
// palette and pages can open one without a provider of their own.
let current: Open | null = null;
let opened = 0;
const listeners = new Set<() => void>();

function set(next: Open | null) {
  current = next;
  for (const listener of listeners) listener();
}

/** Opens the Deploy window for a service; each call opens a fresh one. */
export const openDeploy = (serviceRef: ServiceRef, start: DeployStart = {}) =>
  set({ serviceRef, start, n: ++opened });

const subscribe = (listener: () => void) => {
  listeners.add(listener);
  return () => listeners.delete(listener);
};

/** Renders the open Deploy window, loaded on first open. The shell mounts it once. */
export function DeployHost() {
  const open = useSyncExternalStore(subscribe, () => current);
  if (!open) return null;
  return (
    <Suspense fallback={null}>
      <Window
        key={open.n}
        serviceRef={open.serviceRef}
        start={open.start}
        onClose={() => set(null)}
      />
    </Suspense>
  );
}

const isDirectory = (e: FileSystemEntry): e is FileSystemDirectoryEntry =>
  e.isDirectory;

/**
 * The deployable item of a drop. Entries must be taken during the drop
 * event; the browser empties the DataTransfer afterwards.
 */
function readDrop(data: DataTransfer): Dropped | null {
  const item = data.items[0];
  const entry = item?.webkitGetAsEntry();
  if (entry && isDirectory(entry)) return { folder: entry };
  const file = data.files[0];
  return file ? { file } : null;
}

/**
 * Takes a folder or an archive dropped anywhere on the page while
 * mounted (and enabled). Returns whether files are being dragged over it.
 */
export function useDropTarget(
  enabled: boolean,
  onDrop: (dropped: Dropped) => void,
) {
  const [over, setOver] = useState(false);
  const handle = useEffectEvent(onDrop);
  useEffect(() => {
    if (!enabled) return;
    // dragenter and dragleave fire for every element crossed; count them.
    let depth = 0;
    const files = (e: DragEvent) =>
      e.dataTransfer?.types.includes("Files") ?? false;
    const enter = (e: DragEvent) => {
      if (!files(e)) return;
      e.preventDefault();
      depth++;
      setOver(true);
    };
    const over = (e: DragEvent) => {
      if (!files(e) || !e.dataTransfer) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = "copy";
    };
    const leave = (e: DragEvent) => {
      if (!files(e)) return;
      depth = Math.max(0, depth - 1);
      if (depth === 0) setOver(false);
    };
    const drop = (e: DragEvent) => {
      if (!files(e) || !e.dataTransfer) return;
      e.preventDefault();
      depth = 0;
      setOver(false);
      const dropped = readDrop(e.dataTransfer);
      if (dropped) handle(dropped);
    };
    window.addEventListener("dragenter", enter);
    window.addEventListener("dragover", over);
    window.addEventListener("dragleave", leave);
    window.addEventListener("drop", drop);
    return () => {
      window.removeEventListener("dragenter", enter);
      window.removeEventListener("dragover", over);
      window.removeEventListener("dragleave", leave);
      window.removeEventListener("drop", drop);
      setOver(false);
    };
  }, [enabled]);
  return over;
}

/** Shown over the page while a folder is dragged over it. */
export function DropOverlay({ service }: { service: string }) {
  return (
    <div
      aria-hidden
      className="pointer-events-none fixed inset-0 z-30 grid place-items-center border-2 border-dashed border-info bg-page/80"
    >
      <p className="text-md">
        Drop a folder or a .tar.gz to deploy{" "}
        <b className="font-mono font-medium">{service}</b>
      </p>
    </div>
  );
}
