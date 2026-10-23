// Open /tests/overflow.html under `pnpm dev`; no backend or test dependencies.
import { act, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import type { AuditEvent } from "../data/types";
import { AuditTable } from "../features/audit/AuditTable";
import { Act as DataAct } from "../features/data/kit";
import { Act, Page } from "./kit";
import "../index.css";

/** Throws a browser-visible failure, including in automation's console. */
function check(ok: boolean, message: string) {
  if (!ok) throw new Error(message);
}

if (new URLSearchParams(location.search).has("scrollbar")) {
  const style = document.createElement("style");
  style.textContent =
    "html { overflow-y: scroll; scrollbar-gutter: stable; } ::-webkit-scrollbar { width: 16px; }";
  document.head.append(style);
}

const host = document.getElementById("root");
if (!host) throw new Error("Missing test root");
const root = createRoot(host);
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
const render = (content: ReactNode) => act(() => root.render(content));
const interact = (action: () => void) => act(action);

const noOverflow = () =>
  check(
    document.documentElement.scrollWidth <=
      document.documentElement.clientWidth,
    "The document must not scroll horizontally",
  );

window.addEventListener("error", (event) => {
  document.title = `FAIL: ${event.message}`;
});

await document.fonts.ready;
const cli = `dock deploy --image registry.example.com/${"long-image".repeat(60)}`;
for (const Action of [Act, DataAct]) {
  for (const right of [false, true]) {
    await render(
      <div className={`g flex p-2 ${right ? "justify-end" : "justify-start"}`}>
        <Action cli={cli}>Deploy</Action>
      </div>,
    );
    const button = document.querySelector("button");
    if (!button) throw new Error("Missing action button");
    check(!document.querySelector(".cli"), "Idle hints must not exist");
    noOverflow();
    // React derives mouseenter from mouseover, just as a pointer would.
    await interact(() =>
      button.dispatchEvent(new MouseEvent("mouseover", { bubbles: true })),
    );
    const hint = document.querySelector(".cli");
    if (!hint) throw new Error("Hover must reveal the CLI hint");
    const box = hint.getBoundingClientRect();
    check(
      box.left >= 0 && box.right <= document.documentElement.clientWidth,
      "Hint must fit both edges",
    );
    check(hint.textContent === cli, "Long commands must remain readable");
    noOverflow();
    await interact(() =>
      button.dispatchEvent(new MouseEvent("mouseout", { bubbles: true })),
    );
    check(!document.querySelector(".cli"), "Pointer leave must close the hint");
    await interact(() => button.focus());
    check(
      !!document.querySelector(".cli"),
      "Keyboard focus must reveal the hint",
    );
    noOverflow();
    await interact(() =>
      window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" })),
    );
    check(!document.querySelector(".cli"), "Escape must close the hint");
    await interact(() => button.blur());
    await interact(() => button.focus());
    await interact(() => window.dispatchEvent(new Event("scroll")));
    check(!document.querySelector(".cli"), "Scroll must close the hint");
    await interact(() => button.blur());
    await interact(() => button.focus());
    await interact(() => window.dispatchEvent(new Event("resize")));
    check(!document.querySelector(".cli"), "Resize must close the hint");
    await interact(() => button.blur());
  }
}

const event: AuditEvent = {
  id: 1,
  action: "release.deploy",
  actor: "cli:very-long-machine-name.example.com".repeat(4),
  actor_kind: "token",
  created_at: "2026-10-23T12:00:00Z",
  target: "project/production/service".repeat(8),
  summary: "Deploy a service",
};
for (const compact of [false, true]) {
  await render(
    <Page>
      <AuditTable events={[event]} compact={compact} />
    </Page>,
  );
  noOverflow();
  const scroll = document.querySelector<HTMLElement>(".overflow-x-auto");
  if (!scroll)
    throw new Error("Audit tables must have a local scroll container");
  check(
    scroll.scrollWidth > scroll.clientWidth,
    "Wide rows must stay available",
  );
  scroll.scrollLeft = 100;
  check(scroll.scrollLeft > 0, "Wide audit rows must remain scrollable");
}
await render(<p>Passed: action hints and audit tables at {innerWidth}px.</p>);
document.title = "PASS: horizontal overflow";
console.log(document.title, innerWidth);
