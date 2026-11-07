import { useSuspenseQuery } from "@tanstack/react-query";
import { lazy, Suspense, useState } from "react";
import { passkeysAvailable } from "../../api/webauthn";
import { useSession } from "../../data/queries";
import { ago, day, plural } from "../../ui/format";
import { Button, Confirm, ErrorText, Glyph, SectionHead } from "../../ui/kit";
import {
  type Factor,
  type TOTPSetup,
  twoFactorQuery,
  useAddPasskey,
  useNewRecoveryCodes,
  useRemoveFactor,
  useStartTOTP,
} from "./data";

// Loaded with what opens them: the QR code library comes with the setup.
const SetupTOTP = lazy(() => import("./SetupTOTP"));
const RecoveryCodesDialog = lazy(() => import("./RecoveryCodesDialog"));

/**
 * The caller's two-factor sign-in (ADR 0057, `dock me two-factor`): whether it
 * is on, their passkeys and authenticator app, adding them, and recovery
 * codes.
 * Changes ask to confirm it's you when the session is older than 15
 * minutes (the API client's step-up). A single sign-on session only
 * shows them: an org's provider vouches for the person only in that org,
 * not for their account (ADR 0058).
 */
export function TwoFactorSection() {
  const tf = useSuspenseQuery(twoFactorQuery()).data;
  const me = useSession();
  const start = useStartTOTP();
  const [setup, setSetup] = useState<TOTPSetup>();
  // Recovery codes just made, shown once.
  const [codes, setCodes] = useState<string[]>();
  const on = tf.factors.length > 0;
  const passkeys = tf.factors.filter((f) => f.kind === "passkey");
  const apps = tf.factors.filter((f) => f.kind === "totp");
  // Managed from a password sign-in only.
  const readOnly = !!me?.sso;

  return (
    <section aria-labelledby="two-factor" className="flex flex-col gap-3">
      <SectionHead id="two-factor">Two-factor sign-in</SectionHead>
      <dl className="props">
        <dt>Status</dt>
        <dd>
          {on ? (
            <span className="text-ok">
              <Glyph char="●" /> on
            </span>
          ) : (
            <span className="text-mut">
              <Glyph char="○" /> off
            </span>
          )}{" "}
          <span className="text-mut">
            {on
              ? "signing in takes a second factor"
              : readOnly
                ? "no second factor in Dock"
                : "your password alone signs you in"}
            {me?.two_factor &&
              `; this session used ${factorWord(me.two_factor)}`}
          </span>
        </dd>
        <dt>Passkeys</dt>
        <dd className="flex flex-col gap-2">
          {passkeys.map((f) => (
            <FactorRow key={f.id} factor={f} readOnly={readOnly} />
          ))}
          {passkeys.length === 0 && readOnly && (
            <span className="text-mut">none</span>
          )}
          {!readOnly && <AddPasskey onRecoveryCodes={setCodes} />}
        </dd>
        <dt>Authenticator app</dt>
        <dd className="flex flex-col gap-2">
          {apps.map((f) => (
            <FactorRow key={f.id} factor={f} readOnly={readOnly} />
          ))}
          {apps.length === 0 && readOnly && (
            <span className="text-mut">none</span>
          )}
          {apps.length === 0 && !setup && !readOnly && (
            <span className="flex flex-wrap items-center gap-3">
              <Button
                small
                primary
                disabled={start.isPending}
                onClick={() => start.mutate(undefined, { onSuccess: setSetup })}
              >
                Set up authenticator app
              </Button>
              <ErrorText error={start.error} />
            </span>
          )}
          {setup && (
            <Suspense fallback={null}>
              <SetupTOTP
                setup={setup}
                onCancel={() => setSetup(undefined)}
                onDone={(recovery) => {
                  setSetup(undefined);
                  if (recovery.length) setCodes(recovery);
                }}
              />
            </Suspense>
          )}
        </dd>
        {on && (
          <>
            <dt>Recovery codes</dt>
            <dd>
              <RecoveryCodes
                left={tf.recovery_codes}
                onNew={setCodes}
                readOnly={readOnly}
              />
            </dd>
          </>
        )}
      </dl>
      {readOnly && (
        <p className="text-xs text-mut">
          You signed in with the single sign-on of{" "}
          <span className="font-mono">{me?.sso}</span>, which vouches for you
          only there. Sign in with your password to change your second factors.
        </p>
      )}
      <p className="text-xs text-mut">
        Lost your second factors and your recovery codes? Someone with shell
        access to the server runs{" "}
        <span className="font-mono text-fg">
          sudo dock admin reset-2fa {me?.email ?? "<email>"}
        </span>
        .
      </p>
      {codes && (
        <Suspense fallback={null}>
          <RecoveryCodesDialog
            codes={codes}
            onClose={() => setCodes(undefined)}
          />
        </Suspense>
      )}
    </section>
  );
}

/** How Me.two_factor reads in a sentence. */
function factorWord(kind: string) {
  switch (kind) {
    case "passkey":
      return "a passkey";
    case "totp":
      return "your authenticator app";
    case "recovery":
      return "a recovery code";
    case "sso":
      return "single sign-on";
    default:
      return kind;
  }
}

/**
 * Adding a passkey: a name for where it lives, then the browser creates
 * it. A first factor brings recovery codes, shown once.
 */
function AddPasskey({
  onRecoveryCodes,
}: {
  onRecoveryCodes: (codes: string[]) => void;
}) {
  const add = useAddPasskey();
  const [name, setName] = useState("");
  if (!passkeysAvailable()) {
    return (
      <span className="text-mut">
        Passkeys need Dock's public address (https) or localhost.
      </span>
    );
  }
  return (
    <form
      className="flex flex-wrap items-center gap-x-3 gap-y-1"
      onSubmit={(e) => {
        e.preventDefault();
        add.mutate(name.trim(), {
          onSuccess: (added) => {
            setName("");
            if (added.recovery_codes?.length) {
              onRecoveryCodes(added.recovery_codes);
            }
          },
        });
      }}
    >
      <input
        aria-label="Passkey name"
        placeholder="Name, e.g. MacBook"
        maxLength={64}
        value={name}
        onChange={(e) => setName(e.target.value)}
        disabled={add.isPending}
        className="field w-[200px] py-[5px]"
      />
      <Button small primary type="submit" disabled={add.isPending}>
        Add a passkey
      </Button>
      <ErrorText error={add.error} />
    </form>
  );
}

/** A passkey or authenticator app with when it was added and used, and Remove. */
function FactorRow({
  factor: f,
  readOnly,
}: {
  factor: Factor;
  readOnly: boolean;
}) {
  const remove = useRemoveFactor();
  const [asking, setAsking] = useState(false);
  return (
    <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <span>{f.name}</span>
      <span className="text-faint">
        added {day(f.created_at)}
        {f.last_used_at ? `, used ${ago(f.last_used_at)}` : ", never used"}
      </span>
      {!readOnly && (
        <Button danger small onClick={() => setAsking(true)}>
          Remove
        </Button>
      )}
      {asking && (
        <Confirm
          danger
          action={`Remove ${f.name}`}
          pending={remove.isPending}
          error={remove.error}
          onConfirm={() =>
            remove.mutate(f.id, { onSuccess: () => setAsking(false) })
          }
          onCancel={() => {
            remove.reset();
            setAsking(false);
          }}
        >
          With no second factor left, your password alone signs you in and your
          recovery codes stop working.
        </Confirm>
      )}
    </span>
  );
}

/** How many recovery codes are left, and making new ones. */
function RecoveryCodes({
  left,
  onNew,
  readOnly,
}: {
  left: number;
  onNew: (codes: string[]) => void;
  readOnly: boolean;
}) {
  const create = useNewRecoveryCodes();
  const [asking, setAsking] = useState(false);
  return (
    <span className="flex flex-wrap items-center gap-3">
      <span className={left <= 2 ? "text-warn" : undefined}>
        {plural(left, "unused code")}
      </span>
      {!readOnly && (
        <Button small onClick={() => setAsking(true)}>
          New codes
        </Button>
      )}
      {asking && (
        <Confirm
          title="Make new recovery codes?"
          action="Make new codes"
          pending={create.isPending}
          error={create.error}
          onConfirm={() =>
            create.mutate(undefined, {
              onSuccess: (codes) => {
                setAsking(false);
                onNew(codes);
              },
            })
          }
          onCancel={() => {
            create.reset();
            setAsking(false);
          }}
        >
          The codes you have now stop working.
        </Confirm>
      )}
    </span>
  );
}
