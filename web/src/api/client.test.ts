// Open /tests/errors.html under Vite. No backend: the client's error mapping.
import { ApiError, body } from "./client";

function check(ok: boolean, message: string) {
  if (!ok) throw new Error(message);
}

const failure = (request: Parameters<typeof body>[0]) =>
  body(request).then(
    () => null,
    (e: unknown) => e,
  );

// fetch's own TypeError never reaches people as "Failed to fetch".
const offline = await failure(Promise.reject(new TypeError("Failed to fetch")));
check(
  offline instanceof ApiError &&
    offline.status === 0 &&
    !offline.message.includes("Failed to fetch"),
  "a network failure is an ApiError with status 0 and a plain message",
);

// A proxy's error page has no problem document.
const gateway = await failure(
  Promise.resolve({
    error: "<html>bad gateway</html>",
    response: new Response(null, { status: 502, statusText: "Bad Gateway" }),
  }),
);
check(
  gateway instanceof ApiError &&
    gateway.status === 502 &&
    gateway.message.includes("didn't answer"),
  "a proxy's 502 says Dock's server didn't answer",
);

// The daemon's own problem documents pass through unchanged.
const problem = await failure(
  Promise.resolve({
    error: { status: 409, code: "conflict", detail: "r5 is deploying" },
    response: new Response(null, { status: 409 }),
  }),
);
check(
  problem instanceof ApiError && problem.message === "r5 is deploying",
  "a problem document's detail is the message",
);

document.title = "PASS: errors";
const results = document.getElementById("results");
if (results)
  results.textContent = "PASS: network, proxy and problem-document errors";
console.log(document.title);
