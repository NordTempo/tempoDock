import type { ComponentProps } from "react";

/** Digits only, at most six: what an authenticator app shows. */
export const codeDigits = (typed: string) =>
  typed.replace(/\D/g, "").slice(0, 6);

/**
 * A labelled 6-digit authenticator app code: a numeric keypad on phones,
 * the browser's one-time-code autofill, spaces and dashes from a paste
 * dropped. onComplete fires once the sixth digit is in, so the form
 * submits itself.
 */
export function CodeInput({
  label,
  labelClassName = "",
  value,
  onChange,
  onComplete,
  className = "",
  ...props
}: Omit<ComponentProps<"input">, "value" | "onChange"> & {
  label: string;
  labelClassName?: string;
  value: string;
  onChange: (code: string) => void;
  onComplete?: (code: string) => void;
}) {
  return (
    <label className={`flex flex-col gap-1.5 ${labelClassName}`}>
      <span>{label}</span>
      <input
        {...props}
        inputMode="numeric"
        autoComplete="one-time-code"
        pattern="[0-9]{6}"
        spellCheck={false}
        placeholder="000000"
        value={value}
        onChange={(e) => {
          const code = codeDigits(e.target.value);
          onChange(code);
          if (code.length === 6 && code !== value) onComplete?.(code);
        }}
        className={`field font-mono tracking-[0.3em] ${className}`}
      />
    </label>
  );
}
