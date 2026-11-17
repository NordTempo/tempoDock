// CSV for the exports people open in a spreadsheet: the data panel's
// query results and Activity. Both go through here, so what they hold
// (database values, names people typed) can't run as a formula there.

/** A number as a spreadsheet reads one: -12.5, +4712345678, 1e-9. */
const plainNumber = /^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i;

/**
 * A cell's text as a spreadsheet shows it rather than runs it. Excel,
 * LibreOffice and Sheets take a cell that starts with = + - or @ (or
 * with a tab or carriage return) as a formula; those get a leading
 * apostrophe, which they read as "this is text". Plain numbers start
 * with a sign too and stay as they are.
 */
export function csvText(text: string) {
  return /^[=+\-@\t\r]/.test(text) && !plainNumber.test(text)
    ? `'${text}`
    : text;
}

/** A CSV field: csvText, quoted when it has to be. */
export function csvField(text: string) {
  const safe = csvText(text);
  return /[",\r\n]|^\s/.test(safe) ? `"${safe.replaceAll('"', '""')}"` : safe;
}

/** Rows of cells as a CSV file's text, the header row first. */
export const csv = (rows: string[][]) =>
  rows.map((row) => row.map(csvField).join(",")).join("\r\n");
