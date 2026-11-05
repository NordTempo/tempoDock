import type { components } from "./schema";

// Passkeys in the browser (ADR 0057). The daemon sends WebAuthn options
// as JSON with binary fields in base64url, and takes the credential back
// the same way. Browsers with the JSON helpers (parse...FromJSON, toJSON)
// convert with them; others get the same conversion by hand. Loaded with
// what asks for a passkey, not with the app.

type Challenge = components["schemas"]["PasskeyChallenge"];
type Answer = components["schemas"]["PasskeyResponse"];

/** Whether this page can use passkeys: a browser with WebAuthn, on https or localhost. */
export const passkeysAvailable = () =>
  window.isSecureContext && "PublicKeyCredential" in window;

/**
 * Creates a passkey for options from POST /me/passkeys/options and
 * resolves with the answer for POST /me/passkeys.
 */
export async function createPasskey(challenge: Challenge): Promise<Answer> {
  // The daemon's contract: PublicKeyCredentialCreationOptions as JSON.
  const json = challenge.options as PublicKeyCredentialCreationOptionsJSON;
  const publicKey =
    typeof PublicKeyCredential.parseCreationOptionsFromJSON === "function"
      ? PublicKeyCredential.parseCreationOptionsFromJSON(json)
      : {
          ...json,
          attestation: json.attestation as AttestationConveyancePreference,
          challenge: fromBase64url(json.challenge),
          user: { ...json.user, id: fromBase64url(json.user.id) },
          excludeCredentials: json.excludeCredentials?.map(descriptor),
          extensions: undefined,
        };
  return answer(
    challenge,
    await ask(() => navigator.credentials.create({ publicKey })),
  );
}

/**
 * Signs a challenge (sign-in, second factor or step-up) with a passkey
 * and resolves with the answer for the call that finishes it.
 */
export async function getPasskey(challenge: Challenge): Promise<Answer> {
  // The daemon's contract: PublicKeyCredentialRequestOptions as JSON.
  const json = challenge.options as PublicKeyCredentialRequestOptionsJSON;
  const publicKey =
    typeof PublicKeyCredential.parseRequestOptionsFromJSON === "function"
      ? PublicKeyCredential.parseRequestOptionsFromJSON(json)
      : {
          ...json,
          challenge: fromBase64url(json.challenge),
          allowCredentials: json.allowCredentials?.map(descriptor),
          userVerification:
            json.userVerification as UserVerificationRequirement,
          extensions: undefined,
        };
  return answer(
    challenge,
    await ask(() => navigator.credentials.get({ publicKey })),
  );
}

/**
 * Runs a navigator.credentials call, turning the browser's errors into
 * ones that say what to do.
 */
async function ask(call: () => Promise<Credential | null>) {
  try {
    const credential = await call();
    if (credential instanceof PublicKeyCredential) return credential;
  } catch (e) {
    if (e instanceof DOMException && e.name === "InvalidStateError") {
      throw new Error("This passkey is on your account already.");
    }
    if (e instanceof DOMException && e.name === "SecurityError") {
      throw new Error(
        "Passkeys work on Dock's public address or on localhost; open Dock there.",
      );
    }
    if (!(e instanceof DOMException && e.name === "NotAllowedError")) throw e;
  }
  throw new Error("No passkey was used; try again or use another factor.");
}

/** The credential as JSON (PublicKeyCredential.toJSON) with its ceremony. */
function answer(challenge: Challenge, c: PublicKeyCredential): Answer {
  return { ceremony: challenge.ceremony, credential: toJSON(c) };
}

function toJSON(c: PublicKeyCredential) {
  if (typeof c.toJSON === "function") return c.toJSON();
  const r = c.response;
  const response: Record<string, unknown> = {
    clientDataJSON: toBase64url(r.clientDataJSON),
  };
  if (r instanceof AuthenticatorAttestationResponse) {
    response.attestationObject = toBase64url(r.attestationObject);
    response.transports = r.getTransports();
  }
  if (r instanceof AuthenticatorAssertionResponse) {
    response.authenticatorData = toBase64url(r.authenticatorData);
    response.signature = toBase64url(r.signature);
    if (r.userHandle) response.userHandle = toBase64url(r.userHandle);
  }
  return {
    id: c.id,
    rawId: toBase64url(c.rawId),
    type: c.type,
    authenticatorAttachment: c.authenticatorAttachment ?? undefined,
    response,
    clientExtensionResults: c.getClientExtensionResults(),
  };
}

const descriptor = (d: PublicKeyCredentialDescriptorJSON) => ({
  type: "public-key" as const,
  id: fromBase64url(d.id),
  transports: d.transports as AuthenticatorTransport[] | undefined,
});

function fromBase64url(s: string) {
  const bin = atob(s.replace(/-/g, "+").replace(/_/g, "/"));
  return Uint8Array.from(bin, (ch) => ch.charCodeAt(0));
}

function toBase64url(buf: ArrayBuffer) {
  let bin = "";
  for (const b of new Uint8Array(buf)) bin += String.fromCharCode(b);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}
