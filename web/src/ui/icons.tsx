// A few line icons (24px grid, drawn with the current colour), so the
// shell needs no icon library.
const paths = {
  chevronDown: "m7 10 5 5 5-5",
  chevronRight: "m10 7 5 5-5 5",
  chevronLeft: "m14 7-5 5 5 5",
  search: "M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14ZM20 20l-3.5-3.5",
  plus: "M12 5v14M5 12h14",
  activity: "M3 12h4l3-8 4 16 3-8h4",
  // Bars rising: Dock Analytics.
  bars: "M4 20V14M10 20V9M16 20V12M22 20V4",
  users:
    "M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM2 21v-1a6 6 0 0 1 12 0v1M16 3.5a4 4 0 0 1 0 7.5M22 21v-1a6 6 0 0 0-4-5.6",
  inbox: "M4 13h4l2 3h4l2-3h4M4 13l2.5-8h11L20 13v6H4Z",
  bell: "M6 8a6 6 0 1 1 12 0c0 7 3 9 3 9H3s3-2 3-9M10 21h4",
  server: "M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01",
  database:
    "M4 5c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3ZM4 5v14c0 1.7 3.6 3 8 3s8-1.3 8-3V5M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3",
  key: "M14 10a4 4 0 1 0-1.2 2.8L21 21M17 17l2-2",
  shield: "M12 3 4 6v6c0 5 3.5 8 8 9 4.5-1 8-4 8-9V6Z",
  list: "M8 6h13M8 12h13M8 18h13M3 6h.01M3 12h.01M3 18h.01",
  graph:
    "M6 3a3 3 0 1 0 0 6 3 3 0 0 0 0-6ZM18 15a3 3 0 1 0 0 6 3 3 0 0 0 0-6ZM6 9v3a3 3 0 0 0 3 3h6",
  console: "M4 6h16M4 12h16M4 18h10",
  logout: "M15 4h4v16h-4M10 8l-4 4 4 4M6 12h11",
  grid: "M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z",
  box: "M3 7l9-4 9 4v10l-9 4-9-4ZM3 7l9 4 9-4M12 11v10",
  history: "M3 12a9 9 0 1 0 3-6.7L3 8M3 3v5h5M12 7v5l3 2",
  upload: "M12 20V6M6 12l6-6 6 6M4 4h16",
  globe:
    "M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18ZM3 12h18M12 3c2.4 2.5 3.6 5.5 3.6 9s-1.2 6.5-3.6 9M12 3c-2.4 2.5-3.6 5.5-3.6 9s1.2 6.5 3.6 9",
  sliders: "M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1M15 4v4M9 10v4M17 16v4",
  cloud: "M7 18a4 4 0 0 1-.6-8A6 6 0 0 1 18 9a4.5 4.5 0 0 1-.5 9Z",
} as const;

export type IconName = keyof typeof paths;

/** An outline icon; decorative unless given a label. */
export function Icon({
  name,
  size = 14,
  className,
  label,
}: {
  name: IconName;
  size?: number;
  className?: string;
  label?: string;
}) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      className={`flex-none ${className ?? ""}`}
      role={label ? "img" : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    >
      <path d={paths[name]} />
    </svg>
  );
}
