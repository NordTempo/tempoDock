import {
  CompactSelection,
  DataEditorCore,
  type DrawHeaderCallback,
  type GridCell,
  GridCellKind,
  type GridColumn,
  type GridSelection,
  getMiddleCenterBias,
  ImageWindowLoaderImpl,
  type Item,
  markerCellRenderer,
  type Theme,
  textCellRenderer,
} from "@glideapps/glide-data-grid";
import "@glideapps/glide-data-grid/dist/index.css";
import {
  type ComponentProps,
  useCallback,
  useEffect,
  useMemo,
  useState,
} from "react";
import { bsonText, isBson } from "./bson";
import { type CellKind, cellKind, cellText, isNumericType } from "./cells";
import type { DataCell, DataResultColumn, DataRow } from "./types";

// The data grid: Glide Data Grid (MIT, canvas) with only the text cell
// renderer, so its markdown, image and number editors stay out of the
// bundle. Used for table pages and SQL results; it lives in the lazy Data
// chunk only.

// Style G (./g.css): the canvas needs literal colors, so translucent
// tints are pre-blended onto the panel's #000.
const sans =
  '-apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif';
const mono = 'ui-monospace, "SF Mono", Menlo, monospace';

const theme: Partial<Theme> = {
  accentColor: "#346bf1",
  accentFg: "#fff",
  accentLight: "rgba(52, 107, 241, 0.12)",
  textDark: "#f5f5f5",
  textMedium: "#a3a3a3",
  textLight: "#8a8a8a",
  textBubble: "#f5f5f5",
  bgIconHeader: "#8a8a8a",
  fgIconHeader: "#000",
  textHeader: "#8a8a8a",
  textHeaderSelected: "#f5f5f5",
  bgCell: "#000",
  bgCellMedium: "#0a0a0a",
  bgHeader: "#000",
  bgHeaderHasFocus: "#0f0f0f",
  bgHeaderHovered: "#0a0a0a",
  bgBubble: "#191a1d",
  bgBubbleSelected: "#232428",
  bgSearchResult: "#2b1f0a",
  borderColor: "#000",
  horizontalBorderColor: "#121212",
  headerBottomBorderColor: "#1b1b1b",
  drilldownBorder: "#1b1b1b",
  linkColor: "#7da2ff",
  cellHorizontalPadding: 8,
  cellVerticalPadding: 4,
  headerFontStyle: "500 12px",
  baseFontStyle: "13px",
  markerFontStyle: "11px",
  editorFontSize: "13px",
  fontFamily: sans,
  lineHeight: 1.4,
  roundingRadius: 6,
};

// Ids, numbers, times and raw values in mono and quieter, as G1 shows them;
// only NULL and masked values are dim.
const monoCell: Partial<Theme> = { fontFamily: mono, baseFontStyle: "12px" };
const quietMono: Partial<Theme> = { ...monoCell, textDark: "#a3a3a3" };
const kindTheme: Partial<Record<CellKind, Partial<Theme>>> = {
  null: { ...monoCell, textDark: "#525252" },
  masked: { ...monoCell, textDark: "#525252" },
  truncated: { textDark: "#a3a3a3" },
  bytea: quietMono,
  json: monoCell,
};
const timeTypes = new Set([
  "timestamp",
  "timestamptz",
  "date",
  "time",
  "timetz",
  "interval",
  "uuid",
]);
const linkTheme: Partial<Theme> = { textDark: "#7da2ff" };
const stagedTheme: Partial<Theme> = { bgCell: "#2b1f0a", textDark: "#fde68a" };
const deletedTheme: Partial<Theme> = { bgCell: "#221414", textDark: "#8a8a8a" };
const selectedTheme: Partial<Theme> = { bgCell: "#0f1626" };

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

/** A column's header: "customer_id →", "created_at UTC ↓". */
const headerTitle = (c: GridCol) =>
  `${c.name}${c.type_name === "timestamptz" ? " UTC" : ""}${c.link ? " →" : ""}${c.sort ? ` ${c.sort}` : ""}`;

/** Width from the header and the first rows: characters × a constant. */
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
  return Math.round(Math.min(Math.max(chars * 7.4 + 24, 64), 320));
}

const noSelection: GridSelection = {
  columns: CompactSelection.empty(),
  rows: CompactSelection.empty(),
};

export function Grid({
  columns,
  rows,
  selected,
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
  /** The row whose details are open, highlighted. */
  selected?: number | null;
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
        const title = headerTitle(c);
        return {
          id: c.name,
          title,
          width: columnWidth(title, c.type_name, rows, i),
        };
      }),
    [columns, rows],
  );
  // Glide left-aligns every header; numeric ones go right, over their values.
  const drawHeader = useCallback<DrawHeaderCallback>(
    ({ ctx, column, columnIndex, rect, theme, isSelected }, drawContent) => {
      const type = columns[columnIndex]?.type_name;
      if (!type || !isNumericType(type)) return drawContent();
      ctx.fillStyle =
        (isSelected ? theme.textHeaderSelected : theme.textHeader) ?? "";
      ctx.textAlign = "right";
      ctx.fillText(
        column.title,
        rect.x + rect.width - (theme.cellHorizontalPadding ?? 8),
        rect.y + rect.height / 2 + getMiddleCenterBias(ctx, ctx.font),
      );
      ctx.textAlign = "left";
    },
    [columns],
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
      edit !== undefined
        ? edit === null
          ? "NULL"
          : isBson(column.type_name)
            ? bsonText(edit)
            : edit
        : cellText(cell, column.type_name);
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
        edit !== undefined
          ? stagedTheme
          : link
            ? linkTheme
            : (kindTheme[kind] ??
              (kind === "number"
                ? monoCell
                : timeTypes.has(column.type_name)
                  ? quietMono
                  : undefined)),
    };
  };

  // Never taller than the rows, so short results don't trail empty lines.
  const maxHeight = 32 + rows.length * 34 + 14;

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
        rowHeight={34}
        headerHeight={32}
        drawHeader={drawHeader}
        rowMarkers="none"
        rowSelect="single"
        rowSelectionMode="auto"
        gridSelection={selection}
        onGridSelectionChange={(next) => {
          setSelection(next);
          onSelectRow?.(next.current?.cell[1] ?? next.rows.first() ?? null);
        }}
        getRowThemeOverride={(row) =>
          deleted?.(row)
            ? deletedTheme
            : row === selected
              ? selectedTheme
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
