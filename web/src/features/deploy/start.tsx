import { lazy, Suspense, useEffect, useEffectEvent, useState } from "react";
import type { ServiceRef } from "../../data/types";

// What the service layout needs to start a deploy: the window's options,
// reading a drop, and the drop target. The window itself and the packer
// load only when it opens.

export type DeployMode = "folder" | "image" | "git";

/** Something dropped onto the service: a folder, or an archive file. */
export type Dropped = { folder: FileSystemDirectoryEntry } | { file: File };

/** How the Deploy window opens: on a mode, or with what was dropped. */
export type DeployStart = { mode?: DeployMode; dropped?: Dropped };

const Window = lazy(() => import("./DeployDialog"));

/** The Deploy window, loaded on first open. */
export function DeployWindow(props: {
  serviceRef: ServiceRef;
  start: DeployStart;
  onClose: () => void;
}) {
  return (
    <Suspense fallback={null}>
      <Window {...props} />
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
