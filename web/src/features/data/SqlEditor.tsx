import { autocompletion, completionKeymap } from "@codemirror/autocomplete";
import {
  defaultKeymap,
  history,
  historyKeymap,
  indentWithTab,
} from "@codemirror/commands";
import { PostgreSQL, type SQLNamespace, sql } from "@codemirror/lang-sql";
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
import type { DataTable } from "./types";

// The SQL editor: CodeMirror 6 with the Postgres dialect, completion from
// the schema, and Mod-Enter to run. Lives in the lazy SQL chunk only.

const mono = '"JetBrains Mono", "Inter", ui-monospace, monospace';

const theme = EditorView.theme(
  {
    "&": {
      backgroundColor: "#000",
      color: "#fff",
      fontSize: "13px",
      height: "100%",
    },
    "&.cm-focused": { outline: "none" },
    ".cm-scroller": { fontFamily: mono, lineHeight: "1.6" },
    ".cm-content": { caretColor: "#fff", padding: "8px 0" },
    ".cm-gutters": {
      backgroundColor: "#000",
      color: "#4a4a4a",
      border: "none",
    },
    ".cm-cursor": { borderLeftColor: "#fff" },
    "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection":
      {
        backgroundColor: "#1f3a5f",
      },
    ".cm-placeholder": { color: "#6e6e6e" },
    ".cm-tooltip": {
      backgroundColor: "#0a0a0a",
      border: "1px solid #3a3a3a",
      color: "#fff",
    },
    ".cm-tooltip-autocomplete > ul": { fontFamily: mono, fontSize: "12px" },
    ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
      backgroundColor: "#1a1a1a",
      color: "#fff",
    },
    ".cm-completionDetail": { color: "#8c8c8c" },
  },
  { dark: true },
);

// Quiet highlighting: keywords blue, literals light gray, comments faint.
const highlight = HighlightStyle.define([
  { tag: tags.keyword, color: "#60a5fa" },
  { tag: [tags.string, tags.number, tags.bool, tags.null], color: "#bdbdbd" },
  { tag: tags.comment, color: "#6e6e6e", fontStyle: "italic" },
  { tag: [tags.operator, tags.punctuation], color: "#8c8c8c" },
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

const language = (tables: readonly DataTable[]) =>
  sql({
    dialect: PostgreSQL,
    schema: namespace(tables),
    defaultSchema: "public",
  });

export function SqlEditor({
  value,
  onChange,
  onRun,
  tables,
  errorAt,
}: {
  value: string;
  onChange: (sql: string) => void;
  /** Mod-Enter. */
  onRun: () => void;
  tables: readonly DataTable[];
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
          lang.current.of(language(tables)),
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
      effects: lang.current.reconfigure(language(tables)),
    });
  }, [tables]);

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
