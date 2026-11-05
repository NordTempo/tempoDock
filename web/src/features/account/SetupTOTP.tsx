import { useMemo, useState } from "react";
import { encode } from "uqr";
import { CodeInput } from "../../ui/CodeInput";
import { Button, CopyButton, ErrorText } from "../../ui/kit";
import { type TOTPSetup, useConfirmTOTP } from "./data";

/**
 * Adding an authenticator app: its key as a QR code and as text to type
 * in, then a code from the app keeps it. onDone gets the recovery codes
 * that come with a first factor (none otherwise). Lazy-loaded with the
 * QR library.
 */
export default function SetupTOTP({
  setup,
  onDone,
  onCancel,
}: {
  setup: TOTPSetup;
  onDone: (recoveryCodes: string[]) => void;
  onCancel: () => void;
}) {
  const confirm = useConfirmTOTP();
  const [code, setCode] = useState("");
  const send = (c: string) =>
    confirm.mutate(
      { ceremony: setup.ceremony, code: c },
      {
        onSuccess: (added) => onDone(added.recovery_codes ?? []),
        onError: () => setCode(""),
      },
    );

  return (
    <div className="flex flex-wrap items-start gap-x-6 gap-y-4 py-1">
      <QRCode text={setup.uri} />
      <form
        className="flex min-w-0 flex-[1_1_260px] flex-col gap-3"
        onSubmit={(e) => {
          e.preventDefault();
          if (code.length === 6) send(code);
        }}
      >
        <p>Scan this with your authenticator app, or type in the key.</p>
        <span className="flex flex-wrap items-center gap-2.5">
          <code className="font-mono text-md tracking-wide">
            {setup.secret.match(/.{1,4}/g)?.join(" ")}
          </code>
          <CopyButton text={setup.secret} />
        </span>
        <CodeInput
          label="Code from the app"
          labelClassName="max-w-[220px]"
          autoFocus
          value={code}
          onChange={setCode}
          onComplete={send}
          disabled={confirm.isPending}
          className="py-[7px]"
        />
        <span className="flex flex-wrap items-center gap-2">
          <Button
            small
            primary
            type="submit"
            disabled={confirm.isPending || code.length < 6}
          >
            Add app
          </Button>
          <Button small onClick={onCancel}>
            Cancel
          </Button>
          <ErrorText error={confirm.error} />
        </span>
      </form>
    </div>
  );
}

/**
 * A QR code as one SVG path, black on white whatever the theme so every
 * phone camera reads it.
 */
function QRCode({ text }: { text: string }) {
  const { path, size } = useMemo(() => {
    const { data } = encode(text, { ecc: "M", border: 2 });
    let path = "";
    data.forEach((row, y) => {
      row.forEach((dark, x) => {
        if (dark) path += `M${x} ${y}h1v1h-1z`;
      });
    });
    return { path, size: data.length };
  }, [text]);
  return (
    <svg
      viewBox={`0 0 ${size} ${size}`}
      shapeRendering="crispEdges"
      role="img"
      aria-label="QR code with your authenticator app key"
      className="size-40 flex-none rounded-ctl bg-white"
    >
      <path d={path} className="fill-black" />
    </svg>
  );
}
