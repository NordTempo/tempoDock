// Express app traced by tracing.js: GET /, a slow /slow and a failing /fail.
const express = require("express");
const app = express();
app.get("/", (_req, res) => res.send("node-otel\n"));
app.get("/healthz", (_req, res) => res.send("ok\n"));
app.get("/slow", async (_req, res) => {
  await fetch("http://127.0.0.1:" + port + "/");
  setTimeout(() => res.send("slow\n"), 300);
});
app.get("/fail", (_req, res) => res.status(500).send("boom\n"));
const port = process.env.PORT || 3000;
const server = app.listen(port, () => console.log(`listening on ${port}`));
process.on("SIGTERM", () => server.close(() => process.exit(0)));
