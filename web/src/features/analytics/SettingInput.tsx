import type { Field } from "./settings";

/** One site setting's input, by its kind: lines, a number or on/off. */
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
          className="font-mono"
          aria-label={id ? undefined : field.label}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
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
        <input
          id={id}
          type="number"
          min={1}
          max={field.kind === "days" ? 3650 : 100}
          aria-label={id ? undefined : field.label}
          className="w-28"
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
}
