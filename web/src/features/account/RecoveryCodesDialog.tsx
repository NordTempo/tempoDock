import { Dialog } from "../../ui/Dialog";
import { Button, CopyButton } from "../../ui/kit";

/**
 * New recovery codes, shown this once (ADR 0049): copy them or save them
 * as a text file. Each signs in once in place of the authenticator app.
 */
export default function RecoveryCodesDialog({
  codes,
  onClose,
}: {
  codes: string[];
  onClose: () => void;
}) {
  const text = `${codes.join("\n")}\n`;
  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `dock-recovery-codes-${window.location.hostname}.txt`;
    a.click();
    // After the click has started the download.
    setTimeout(() => URL.revokeObjectURL(url));
  };
  return (
    <Dialog title="Recovery codes" onClose={onClose}>
      <div className="flex flex-col gap-3.5">
        <p>
          <b className="font-semibold">Store these now.</b>{" "}
          <span className="text-mut">
            They are not shown again. Each one signs you in once if you lose
            your authenticator app.
          </span>
        </p>
        <ul
          aria-label="Recovery codes"
          className="grid grid-cols-2 gap-x-6 gap-y-1 font-mono"
        >
          {codes.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
        <div className="flex flex-wrap items-center gap-2">
          <CopyButton text={text} label="Copy" />
          <Button small onClick={download}>
            Download .txt
          </Button>
          <Button small primary className="ml-auto" onClick={onClose}>
            I've stored them
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
