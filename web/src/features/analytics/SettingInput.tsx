import type { Field } from "./settings";

/** One site setting's input, by its kind: lines, a number, on/off or a choice. */
export function SettingInput({
  field,
  value,
  onChange,
  id,
}: {
  field: Field;
  value: string;
  onChange: (text: string) => void;
  id?: string;
}) {
  switch (field.kind) {
    case "list":
      return (
        <textarea
          id={id}
          rows={3}
          className="field w-full max-w-[520px] font-mono text-xs"
          aria-label={id ? undefined : field.label}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case "choice":
      return (
        <fieldset className="seg self-start" aria-label={field.label}>
          {("options" in field ? field.options : []).map(([v, words]) => (
            <button
              key={v}
              type="button"
              aria-pressed={value === v}
              onClick={() => onChange(v)}
            >
              {words}
            </button>
          ))}
        </fieldset>
      );
    case "switch":
      return (
        <fieldset className="seg self-start" aria-label={field.label}>
          {(["on", "off"] as const).map((v) => (
            <button
              key={v}
              type="button"
              aria-pressed={value === v}
              onClick={() => onChange(v)}
            >
              {v === "on" ? "On" : "Off"}
            </button>
          ))}
        </fieldset>
      );
    default:
      return (
        <span className="flex items-center gap-2">
          <input
            id={id}
            type="number"
            min={1}
            max={field.kind === "days" ? 3650 : 100}
            aria-label={id ? undefined : field.label}
            className="field w-24 font-mono"
            value={value}
            onChange={(e) => onChange(e.target.value)}
          />
          <span className="text-xs text-faint">
            {field.kind === "days" ? "days" : "%"}
          </span>
        </span>
      );
  }
}
