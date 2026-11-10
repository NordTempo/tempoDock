import { autocompletion, completionKeymap } from "@codemirror/autocomplete";
import {
  defaultKeymap,
  history,
  historyKeymap,
  indentWithTab,
} from "@codemirror/commands";
import {
  MySQL,
  PostgreSQL,
  type SQLNamespace,
  sql,
} from "@codemirror/lang-sql";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { Compartment, EditorSelection, EditorState } from "@codemirror/state";
import {
  drawSelection,
  EditorView,
  keymap,
  lineNumbers,
  placeholder,
} from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { useEffect, useLayoutEffect, useRef } from "react";
import type { DataEngine } from "./engines";
import type { DataTable } from "./types";

// The SQL editor: CodeMirror 6 with the database's dialect, completion
// from the schema, and Mod-Enter to run. Lives in the lazy SQL chunk only.

// Style G (G3): the panel's mono stack and colours.
const mono = "ui-monospace, 'SF Mono', Menlo, monospace";

const theme = EditorView.theme(
  {
    "&": {
      backgroundColor: "#000",
      color: "#d4d4d4",
      fontSize: "13px",
      height: "100%",
    },
    "&.cm-focused": { outline: "none" },
    ".cm-scroller": { fontFamily: mono, lineHeight: "22px" },
    ".cm-content": { caretColor: "#f5f5f5", padding: "12px 0" },
    ".cm-gutters": {
      backgroundColor: "#000",
      color: "#525252",
      border: "none",
      paddingLeft: "12px",
    },
    ".cm-cursor": { borderLeftColor: "#f5f5f5" },
    "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection":
      {
        backgroundColor: "rgba(52, 107, 241, 0.3)",
      },
    ".cm-placeholder": { color: "#525252" },
    ".cm-tooltip": {
      backgroundColor: "#191a1d",
      border: "1px solid rgba(255, 255, 255, 0.08)",
      borderRadius: "8px",
      color: "#f5f5f5",
      overflow: "hidden",
    },
    ".cm-tooltip-autocomplete > ul": { fontFamily: mono, fontSize: "12px" },
    ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
      backgroundColor: "rgba(52, 107, 241, 0.16)",
      color: "#f5f5f5",
    },
    ".cm-completionDetail": { color: "#8a8a8a" },
  },
  { dark: true },
);

// Quiet highlighting: keywords blue, strings amber, comments faint.
const highlight = HighlightStyle.define([
  { tag: tags.keyword, color: "#9cb8ff" },
  { tag: tags.string, color: "#e5c07b" },
  { tag: [tags.number, tags.bool, tags.null], color: "#d4d4d4" },
  { tag: tags.comment, color: "#525252", fontStyle: "italic" },
  { tag: [tags.operator, tags.punctuation], color: "#8a8a8a" },
  { tag: tags.typeName, color: "#d4d4d4" },
]);

/** Completion namespace: schema → table → columns; public is the default. */
function namespace(tables: readonly DataTable[]): SQLNamespace {
  const out: Record<string, Record<string, string[]>> = {};
  for (const t of tables) {
    out[t.schema] = {
      ...out[t.schema],
      [t.name]: (t.columns ?? []).map((c) => c.name),
    };
  }
  return out;
}

// MySQL's "schemas" are its databases: unqualified names complete from
// the first one's tables (usually the only one).
// Redis commands get no SQL language: plain text.
const language = (tables: readonly DataTable[], dialect: DataEngine) =>
  dialect === "redis"
    ? []
    : sql({
        dialect: dialect === "mysql" ? MySQL : PostgreSQL,
        schema: namespace(tables),
        defaultSchema: dialect === "mysql" ? tables[0]?.schema : "public",
      });

export function SqlEditor({
  value,
  onChange,
  onRun,
  tables,
  dialect,
  errorAt,
}: {
  value: string;
  onChange: (sql: string) => void;
  /** Mod-Enter. */
  onRun: () => void;
  tables: readonly DataTable[];
  dialect: DataEngine;
  /** 1-based character the last error points at; selects that word. */
  errorAt?: number;
}) {
  const host = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const lang = useRef(new Compartment());
  const latest = useRef({ onChange, onRun });
  useLayoutEffect(() => {
    latest.current = { onChange, onRun };
  });

  useEffect(() => {
    if (!host.current) return;
    const editor = new EditorView({
      parent: host.current,
      state: EditorState.create({
        doc: value,
        extensions: [
          lineNumbers(),
          history(),
          drawSelection({ cursorBlinkRate: 0 }),
          keymap.of([
            {
              key: "Mod-Enter",
              run: () => {
                latest.current.onRun();
                return true;
              },
            },
            ...completionKeymap,
            ...defaultKeymap,
            ...historyKeymap,
            indentWithTab,
          ]),
          lang.current.of(language(tables, dialect)),
          autocompletion(),
          syntaxHighlighting(highlight),
          theme,
          placeholder("select * from orders where status = 'paid'"),
          EditorView.lineWrapping,
          EditorView.contentAttributes.of({ "aria-label": "SQL" }),
          EditorView.updateListener.of((u) => {
            if (u.docChanged) latest.current.onChange(u.state.doc.toString());
          }),
        ],
      }),
    });
    view.current = editor;
    editor.focus();
    return () => editor.destroy();
  }, []);

  // A query loaded from history or saved queries replaces the text.
  useEffect(() => {
    const editor = view.current;
    if (!editor || editor.state.doc.toString() === value) return;
    editor.dispatch({
      changes: { from: 0, to: editor.state.doc.length, insert: value },
    });
  }, [value]);

  useEffect(() => {
    view.current?.dispatch({
      effects: lang.current.reconfigure(language(tables, dialect)),
    });
  }, [tables, dialect]);

  useEffect(() => {
    const editor = view.current;
    if (!editor || !errorAt) return;
    const from = Math.min(errorAt - 1, editor.state.doc.length);
    const word = editor.state.wordAt(from);
    editor.dispatch({
      selection: word
        ? EditorSelection.range(word.from, word.to)
        : EditorSelection.cursor(from),
      scrollIntoView: true,
    });
  }, [errorAt]);

  return <div ref={host} className="h-full min-h-0 overflow-auto" />;
}
