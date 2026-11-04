import { useSuspenseQuery } from "@tanstack/react-query";
import { lazy, Suspense, useState } from "react";
import { useSession } from "../../data/queries";
import { ago, day, plural } from "../../ui/format";
import { Button, Confirm, ErrorText, Glyph, SectionHead } from "../../ui/kit";
import {
  type Factor,
  type TOTPSetup,
  twoFactorQuery,
  useNewRecoveryCodes,
  useRemoveFactor,
  useStartTOTP,
} from "./data";

// Loaded with what opens them: the QR code library comes with the setup.
const SetupTOTP = lazy(() => import("./SetupTOTP"));
const RecoveryCodesDialog = lazy(() => import("./RecoveryCodesDialog"));

/**
 * The caller's two-factor sign-in (ADR 0051, `dock me two-factor`): whether it
 * is on, their authenticator app, setting one up, and recovery codes.
 * Changes ask to confirm it's you when the session is older than 15
 * minutes (the API client's step-up).
 */
export function TwoFactorSection() {
  const tf = useSuspenseQuery(twoFactorQuery()).data;
  const me = useSession();
  const start = useStartTOTP();
  const [setup, setSetup] = useState<TOTPSetup>();
  // Recovery codes just made, shown once.
  const [codes, setCodes] = useState<string[]>();
  const on = tf.factors.length > 0;

  return (
    <section aria-labelledby="two-factor" className="flex flex-col gap-3">
      <SectionHead id="two-factor" aside="dock me two-factor">
        Two-factor sign-in
      </SectionHead>
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
              ? "signing in takes your password and a code"
              : "your password alone signs you in"}
            {me?.two_factor &&
              `; this session used ${factorWord(me.two_factor)}`}
          </span>
        </dd>
        <dt>Authenticator app</dt>
        <dd className="flex flex-col gap-2">
          {tf.factors.map((f) => (
            <FactorRow key={f.id} factor={f} />
          ))}
          {!on && !setup && (
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
              <RecoveryCodes left={tf.recovery_codes} onNew={setCodes} />
            </dd>
          </>
        )}
      </dl>
      <p className="text-xs text-mut">
        Lost your phone and your recovery codes? Someone with shell access to
        the server runs{" "}
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
    case "totp":
      return "your authenticator app";
    case "recovery":
      return "a recovery code";
    default:
      return kind;
  }
}

/** An authenticator app with when it was added and used, and Remove. */
function FactorRow({ factor: f }: { factor: Factor }) {
  const remove = useRemoveFactor();
  const [asking, setAsking] = useState(false);
  if (asking) {
    return (
      <Confirm
        danger
        action="Remove"
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
        Remove <b className="font-semibold">{f.name}</b>?{" "}
        <span className="text-mut">
          With no second factor left, your password alone signs you in and your
          recovery codes stop working.
        </span>
      </Confirm>
    );
  }
  return (
    <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
      <span>{f.name}</span>
      <span className="text-faint">
        added {day(f.created_at)}
        {f.last_used_at ? `, used ${ago(f.last_used_at)}` : ", never used"}
      </span>
      <Button small onClick={() => setAsking(true)}>
        Remove
      </Button>
    </span>
  );
}

/** How many recovery codes are left, and making new ones. */
function RecoveryCodes({
  left,
  onNew,
}: {
  left: number;
  onNew: (codes: string[]) => void;
}) {
  const create = useNewRecoveryCodes();
  const [asking, setAsking] = useState(false);
  if (asking) {
    return (
      <Confirm
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
        Make new recovery codes?{" "}
        <span className="text-mut">The ones you have stop working.</span>
      </Confirm>
    );
  }
  return (
    <span className="flex flex-wrap items-center gap-3">
      <span className={left <= 2 ? "text-warn" : undefined}>
        {plural(left, "unused code")}
      </span>
      <Button small onClick={() => setAsking(true)}>
        New codes
      </Button>
    </span>
  );
}
