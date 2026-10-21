// API that talks to Postgres over the environment's private network, using
// DATABASE_URL (built from ${{postgres.*}} references).
//   GET  /         row count and the newest rows
//   POST /items?n= insert a row
//   GET  /healthz  200 only if the database answers
const express = require("express");
const { Pool } = require("pg");

const pool = new Pool({ connectionString: process.env.DATABASE_URL });
// Idle clients error when Postgres restarts; without a handler Node exits.
pool.on("error", (e) => console.error("pg pool:", e.message));
const app = express();

const ready = pool.query("CREATE TABLE IF NOT EXISTS items (id serial PRIMARY KEY, name text NOT NULL)");

app.get("/", async (_req, res) => {
  try {
    await ready;
    const { rows } = await pool.query("SELECT count(*)::int AS n, coalesce(max(name), '') AS last FROM items");
    res.json({ db_host: new URL(process.env.DATABASE_URL).hostname, ...rows[0] });
  } catch (e) {
    res.status(500).json({ error: e.message });
  }
});

app.post("/items", async (req, res) => {
  try {
    await ready;
    await pool.query("INSERT INTO items (name) VALUES ($1)", [String(req.query.n || "x")]);
    res.status(201).send("ok\n");
  } catch (e) {
    res.status(500).json({ error: e.message });
  }
});

app.get("/healthz", async (_req, res) => {
  try {
    await pool.query("SELECT 1");
    res.send("ok\n");
  } catch (e) {
    res.status(503).send(e.message + "\n");
  }
});

const server = app.listen(process.env.PORT || 3000, () => console.log("listening"));
process.on("SIGTERM", () => server.close(() => pool.end().then(() => process.exit(0))));
