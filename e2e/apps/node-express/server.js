// Tiny Express app built by Railpack (no Dockerfile). GET / returns the
// version in VERSION, so a redeploy with a new VERSION file shows from outside.
const express = require("express");
const fs = require("fs");

const version = fs.readFileSync(__dirname + "/VERSION", "utf8").trim();
const app = express();
app.get("/", (_req, res) => res.send(`node-express ${version}\n`));
app.get("/healthz", (_req, res) => res.send("ok\n"));
const port = process.env.PORT || 3000;
const server = app.listen(port, () => console.log(`listening on ${port}`));
process.on("SIGTERM", () => server.close(() => process.exit(0)));
