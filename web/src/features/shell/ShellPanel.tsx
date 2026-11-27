import type { FitAddon } from "@xterm/addon-fit";
import type { Terminal } from "@xterm/xterm";
import { useEffect, useId, useRef, useState } from "react";
import { api, body } from "../../api/client";
import type { components } from "../../api/schema";
import { useAction } from "../../data/mutations";
import { servicePath, useService, useWhyNot } from "../../data/queries";
import type { ServiceRef } from "../../data/types";
import { clock } from "../../ui/format";
import { Act, Button, ErrorText } from "../../ui/kit";
import { Frame, FrameReader, frame, resizeFrame } from "./frames";

type ExecTicket = components["schemas"]["ExecTicket"];

/**
 * The Exit frame's payload, apitypes.ExecEnd. It travels only over the
 * socket, so OpenAPI has no schema for it to derive from.
 */
type ExecEnd = {
  exit_code?: number;
  code?: string;
  error?: string;
  container: string;
  release: number;
};

/** Where a session is: not opened, opening, open, or ended (how). */
type Session =
  | { at: "idle" }
  | { at: "connecting" }
  | { at: "open"; since: string }
  | { at: "ended"; end: ExecEnd | null; byYou: boolean };

/**
 * The service's Shell tab (ADR 0111, mock A): a terminal in the container
 * of its active release. Nothing connects until Connect: the console gets
 * a single-use ticket (a CSRF-guarded call that checks admin and the
 * lock), then opens the shell's WebSocket with it. The socket carries the
 * runframe frames `dock exec` uses. Leaving the tab closes it.
 */
export function ShellPanel({ serviceRef: ref }: { serviceRef: ServiceRef }) {
  const { service } = useService(ref);
  const box = useRef<HTMLDivElement>(null);
  const term = useRef<{ t: Terminal; fit: FitAddon } | null>(null);
  const socket = useRef<WebSocket | null>(null);
  const [session, setSession] = useState<Session>({ at: "idle" });
  const [ticket, setTicket] = useState<ExecTicket | null>(null);
  const [ready, setReady] = useState(false);
  const [loadFailed, setLoadFailed] = useState(false);
  // Counts connection attempts: Disconnect and leaving the tab move it on,
  // so a ticket that arrives for an older attempt opens nothing.
  const attempt = useRef(0);
  const connectId = useId();
  const denied = useWhyNot("admin", ref);
  // Set when Disconnect closes the socket, to say so.
  const leaving = useRef(false);
  const getTicket = useAction(
    () =>
      body(
        api.POST(
          "/api/v1/projects/{project}/environments/{environment}/services/{service}/exec-tickets",
          { params: { path: servicePath(ref) } },
        ),
      ),
    () => [],
  );

  // One terminal for the tab's life; a session writes into it. xterm.js
  // and its stylesheet load here, so no other page pays for them.
  useEffect(() => {
    let gone = false;
    let cleanup = () => {};
    void Promise.all([
      import("@xterm/xterm"),
      import("@xterm/addon-fit"),
      import("@xterm/xterm/css/xterm.css"),
    ])
      .catch(() => {
        if (!gone) setLoadFailed(true);
        return null;
      })
      .then((mods) => {
        if (!mods) return;
        const [{ Terminal }, { FitAddon }] = mods;
        if (gone || !box.current) return;
        const t = new Terminal({
          cursorBlink: false,
          fontFamily: '"JetBrains Mono", ui-monospace, monospace',
          fontSize: 13,
          lineHeight: 1.25,
          scrollback: 5000,
          theme: {
            background: "#000000",
            foreground: "#e4e4e7",
            cursor: "#e4e4e7",
            selectionBackground: "#2c2c30",
          },
        });
        const fit = new FitAddon();
        t.loadAddon(fit);
        t.open(box.current);
        fit.fit();
        term.current = { t, fit };
        setReady(true);
        // Fits to the box when the window or the layout changes it.
        const seen = new ResizeObserver(() => fit.fit());
        seen.observe(box.current);
        const input = t.onData((d) =>
          socket.current?.send(frame(Frame.stdin, new TextEncoder().encode(d))),
        );
        const resized = t.onResize(({ cols, rows }) =>
          socket.current?.send(resizeFrame(cols, rows)),
        );
        cleanup = () => {
          seen.disconnect();
          input.dispose();
          resized.dispose();
          t.dispose();
          term.current = null;
        };
      });
    return () => {
      gone = true;
      attempt.current++;
      socket.current?.close();
      cleanup();
    };
  }, []);

  const connect = async () => {
    const tt = term.current;
    if (!tt) return;
    const mine = ++attempt.current;
    setSession({ at: "connecting" });
    let tk: ExecTicket;
    try {
      tk = await getTicket.mutateAsync(undefined);
    } catch {
      if (attempt.current === mine) setSession({ at: "idle" });
      return;
    }
    // Disconnected (or left) while the ticket was on its way.
    if (attempt.current !== mine) return;
    setTicket(tk);
    tt.t.reset();
    const p = servicePath(ref);
    const url = new URL(
      `/api/v1/projects/${encodeURIComponent(p.project)}/environments/${encodeURIComponent(p.environment)}/services/${encodeURIComponent(p.service)}/exec/ws`,
      window.location.href,
    );
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("cols", String(tt.t.cols));
    url.searchParams.set("rows", String(tt.t.rows));
    const ws = new WebSocket(url, ["dock-exec", tk.ticket]);
    ws.binaryType = "arraybuffer";
    socket.current = ws;
    const reader = new FrameReader();
    let end: ExecEnd | null = null;
    leaving.current = false;
    ws.onopen = () => {
      setSession({ at: "open", since: new Date().toISOString() });
      tt.t.focus();
    };
    ws.onmessage = (e) => {
      if (!(e.data instanceof ArrayBuffer)) return;
      for (const f of reader.push(new Uint8Array(e.data))) {
        if (f.type === Frame.stdout || f.type === Frame.stderr) {
          tt.t.write(f.payload);
        } else if (f.type === Frame.exit) {
          end = JSON.parse(new TextDecoder().decode(f.payload)) as ExecEnd;
        }
      }
    };
    ws.onclose = () => {
      if (socket.current === ws) socket.current = null;
      setSession({ at: "ended", end, byYou: leaving.current });
    };
  };

  // Once the terminal is there, Connect has focus: Enter opens the shell.
  useEffect(() => {
    if (ready) document.getElementById(connectId)?.focus();
  }, [ready, connectId]);

  const reason = service.active_release
    ? denied
    : "deploy first: a shell opens in the running container";
  const open = session.at === "open" || session.at === "connecting";
  const release = ticket?.release ?? service.active_release;
  return (
    <div className="flex flex-col gap-2.5">
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2 text-mut">
        {ticket && (
          <span>
            Container{" "}
            <span className="font-mono text-sec">{ticket.container}</span>
          </span>
        )}
        {release !== undefined && (
          <span>
            release <span className="font-mono text-sec">r{release}</span>
          </span>
        )}
        <span className="flex-1" />
        {open ? (
          <Button
            danger
            onClick={() => {
              leaving.current = true;
              attempt.current++;
              if (socket.current) socket.current.close();
              else setSession({ at: "ended", end: null, byYou: true });
            }}
          >
            Disconnect
          </Button>
        ) : (
          <Act
            primary
            id={connectId}
            cli={`dock exec -s ${ref.service} -e ${ref.environment}`}
            disabled={!!reason || !ready}
            reason={reason}
            onClick={connect}
          >
            {session.at === "ended" ? "Reconnect" : "Connect"}
          </Act>
        )}
      </div>
      <div
        ref={box}
        className="h-[calc(100vh-var(--shell-top)-300px)] min-h-[320px] overflow-hidden rounded-lg border border-line bg-page p-2"
      />
      <div className="flex flex-wrap gap-x-4 text-mut">
        <SessionLine session={session} />
        <ErrorText error={getTicket.error} />
        {loadFailed && (
          <span role="alert" className="text-fail">
            The terminal didn't load.{" "}
            <Button small onClick={() => window.location.reload()}>
              Reload
            </Button>
          </span>
        )}
      </div>
    </div>
  );
}

/** The line under the terminal: what the session is doing or how it ended. */
function SessionLine({ session }: { session: Session }) {
  switch (session.at) {
    case "idle":
      return (
        <span>
          Opens a shell in what serves traffic now;{" "}
          <span className="font-mono">dock run</span> starts a new container
          instead. Audited as service.exec.
        </span>
      );
    case "connecting":
      return <span>Connecting</span>;
    case "open":
      return (
        <span>
          Connected at {clock(session.since)}, as you, audited. Ends when you
          leave this tab or the container restarts.
        </span>
      );
    case "ended": {
      const e = session.end;
      if (e?.exit_code !== undefined)
        return <span>Session ended, exit code {e.exit_code}.</span>;
      if (e?.error) return <span className="text-fail">{e.error}</span>;
      return (
        <span>{session.byYou ? "Disconnected." : "The connection ended."}</span>
      );
    }
  }
}
