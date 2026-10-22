import {
  CompactSelection,
  DataEditorCore,
  type GridCell,
  GridCellKind,
  type GridColumn,
  type GridSelection,
  ImageWindowLoaderImpl,
  type Item,
  markerCellRenderer,
  type Theme,
  textCellRenderer,
} from "@glideapps/glide-data-grid";
import "@glideapps/glide-data-grid/dist/index.css";
import { type ComponentProps, useEffect, useMemo, useState } from "react";
import { type CellKind, cellKind, cellText } from "./cells";
import type { DataCell, DataResultColumn, DataRow } from "./types";

// The data grid: Glide Data Grid (MIT, canvas) with only the text cell
// renderer, so its markdown, image and number editors stay out of the
// bundle. Used for table pages and SQL results; it lives in the lazy Data
// chunk only.

const mono = '"JetBrains Mono", "Inter", ui-monospace, monospace';

// The v2 palette (src/index.css @theme), as Glide wants it.
const theme: Partial<Theme> = {
  accentColor: "#60a5fa",
  accentFg: "#000",
  accentLight: "rgba(96, 165, 250, 0.14)",
  textDark: "#fff",
  textMedium: "#8c8c8c",
  textLight: "#6e6e6e",
  textBubble: "#fff",
  bgIconHeader: "#8c8c8c",
  fgIconHeader: "#000",
  textHeader: "#8c8c8c",
  textHeaderSelected: "#000",
  bgCell: "#000",
  bgCellMedium: "#0a0a0a",
  bgHeader: "#000",
  bgHeaderHasFocus: "#161616",
  bgHeaderHovered: "#0a0a0a",
  bgBubble: "#161616",
  bgBubbleSelected: "#262626",
  bgSearchResult: "#161000",
  borderColor: "#161616",
  horizontalBorderColor: "#161616",
  headerBottomBorderColor: "#262626",
  drilldownBorder: "#262626",
  linkColor: "#60a5fa",
  cellHorizontalPadding: 10,
  cellVerticalPadding: 3,
  headerFontStyle: "500 12px",
  baseFontStyle: "12px",
  markerFontStyle: "11px",
  editorFontSize: "12px",
  fontFamily: mono,
  lineHeight: 1.4,
  roundingRadius: 0,
};

// Per kind of cell: quiet for NULL and masked values, blue for links.
const kindTheme: Partial<Record<CellKind, Partial<Theme>>> = {
  null: { textDark: "#6e6e6e" },
  masked: { textDark: "#6e6e6e" },
  truncated: { textDark: "#bdbdbd" },
  bytea: { textDark: "#8c8c8c" },
};
const linkTheme: Partial<Theme> = { textDark: "#60a5fa" };
const stagedTheme: Partial<Theme> = { bgCell: "#161000", textDark: "#fbbf24" };
const deletedTheme: Partial<Theme> = { bgCell: "#1a0a0a", textDark: "#6e6e6e" };

/** A grid column: a result column plus what the table knows about it. */
export type GridCol = DataResultColumn & {
  /** Part of a foreign key: clicking a value follows it. */
  link?: boolean;
  /** "↑" or "↓" when the view is sorted by it. */
  sort?: "↑" | "↓";
  /** Editable in edit mode. */
  editable?: boolean;
};

/** Glide's overlay editor renders into #portal; make sure there is one. */
function usePortal(enabled: boolean) {
  useEffect(() => {
    if (!enabled || document.getElementById("portal")) return;
    const el = document.createElement("div");
    el.id = "portal";
    el.style.cssText = "position:fixed;left:0;top:0;z-index:50";
    document.body.append(el);
  }, [enabled]);
}

// Glide types each renderer by its cell kind but takes them as one array of
// the union, which TypeScript can't relate; its own AllCellRenderers is
// declared the same widened way.
const renderers = [textCellRenderer, markerCellRenderer] as NonNullable<
  ComponentProps<typeof DataEditorCore>["renderers"]
>;

// Required by the core editor; no image cells here, so it stays idle.
const imageWindowLoader = new ImageWindowLoaderImpl();

/** Width from the header and the first rows: monospace, so characters × a constant. */
function columnWidth(
  title: string,
  typeName: string,
  rows: readonly DataRow[],
  i: number,
) {
  let chars = title.length + 1;
  for (const row of rows.slice(0, 50)) {
    chars = Math.max(chars, cellText(row[i] ?? null, typeName).length);
  }
  return Math.round(Math.min(Math.max(chars * 7.6 + 24, 64), 320));
}

const noSelection: GridSelection = {
  columns: CompactSelection.empty(),
  rows: CompactSelection.empty(),
};

export function Grid({
  columns,
  rows,
  staged,
  deleted,
  onSelectRow,
  onSort,
  onFollow,
  onEdit,
  onDeleteRow,
  onNearEnd,
}: {
  columns: GridCol[];
  rows: readonly DataRow[];
  /** The staged value of a cell, if it was edited. */
  staged?: (row: number, column: string) => string | null | undefined;
  /** Rows staged for deletion. */
  deleted?: (row: number) => boolean;
  /** A cell or row marker was selected (null: selection cleared). */
  onSelectRow?: (row: number | null) => void;
  /** A header was clicked. */
  onSort?: (column: string) => void;
  /** A value in a link column was clicked. */
  onFollow?: (row: number, column: string) => void;
  /** An editable cell was edited to `value`. */
  onEdit?: (row: number, column: string, value: string) => void;
  /** Delete or Backspace on a selected cell or row (edit mode). */
  onDeleteRow?: (row: number) => void;
  /** The last rows came into view: time to load the next page. */
  onNearEnd?: () => void;
}) {
  usePortal(!!onEdit);
  const [selection, setSelection] = useState<GridSelection>(noSelection);
  const gridColumns = useMemo<GridColumn[]>(
    () =>
      columns.map((c, i) => {
        const title = `${c.name}${c.link ? " →" : ""}${c.sort ? ` ${c.sort}` : ""}`;
        return {
          id: c.name,
          title,
          width: columnWidth(title, c.type_name, rows, i),
        };
      }),
    [columns, rows],
  );

  const getCellContent = ([col, row]: Item): GridCell => {
    const column = columns[col];
    const cell: DataCell = rows[row]?.[col] ?? null;
    if (!column) {
      return {
        kind: GridCellKind.Text,
        data: "",
        displayData: "",
        allowOverlay: false,
      };
    }
    const kind = cellKind(cell, column.type_name);
    const edit = staged?.(row, column.name);
    const text =
      edit !== undefined ? (edit ?? "NULL") : cellText(cell, column.type_name);
    const editable =
      !!onEdit &&
      !!column.editable &&
      kind !== "masked" &&
      kind !== "truncated";
    const link = column.link && kind !== "null" && !!onFollow;
    return {
      kind: GridCellKind.Text,
      data:
        edit !== undefined
          ? (edit ?? "")
          : typeof cell === "object"
            ? ""
            : String(cell ?? ""),
      displayData: text,
      allowOverlay: editable,
      readonly: !editable,
      contentAlign: kind === "number" ? "right" : "left",
      cursor: link ? "pointer" : undefined,
      themeOverride:
        edit !== undefined ? stagedTheme : link ? linkTheme : kindTheme[kind],
    };
  };

  // Never taller than the rows, so short results don't trail empty lines.
  const maxHeight = 28 + rows.length * 26 + 14;

  return (
    <div className="h-full" style={{ maxHeight }}>
      <DataEditorCore
        columns={gridColumns}
        rows={rows.length}
        getCellContent={getCellContent}
        renderers={renderers}
        imageWindowLoader={imageWindowLoader}
        theme={theme}
        width="100%"
        height="100%"
        rowHeight={26}
        headerHeight={28}
        rowMarkers={onSelectRow ? "clickable-number" : "number"}
        rowSelect="single"
        rowSelectionMode="auto"
        gridSelection={selection}
        onGridSelectionChange={(next) => {
          setSelection(next);
          onSelectRow?.(next.current?.cell[1] ?? next.rows.first() ?? null);
        }}
        getRowThemeOverride={
          deleted
            ? (row) => (deleted(row) ? deletedTheme : undefined)
            : undefined
        }
        onHeaderClicked={
          onSort
            ? (col) => {
                const name = columns[col]?.name;
                if (name) onSort(name);
              }
            : undefined
        }
        onCellClicked={([col, row]) => {
          const column = columns[col];
          if (column?.link && onFollow && rows[row]?.[col] !== null)
            onFollow(row, column.name);
        }}
        onCellEdited={
          onEdit
            ? ([col, row], value) => {
                const name = columns[col]?.name;
                if (name && value.kind === GridCellKind.Text)
                  onEdit(row, name, value.data);
              }
            : undefined
        }
        // Delete stages the row's deletion instead of clearing cells.
        onDelete={(sel) => {
          const row = sel.current?.cell[1] ?? sel.rows.first();
          if (onDeleteRow && row !== undefined) onDeleteRow(row);
          return false;
        }}
        onVisibleRegionChanged={
          onNearEnd
            ? (range) => {
                if (range.y + range.height >= rows.length - 20) onNearEnd();
              }
            : undefined
        }
      />
    </div>
  );
}
