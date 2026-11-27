// Dev-only: the Data panel over a MongoDB collection, from a stub source
// (tests/mongo.html, `pnpm dev`; no backend). It shows the Documents tab
// as Saile picked it (mock A, ADR 0110): the grid of top-level fields and
// the open document's tree. ?doc=1 opens the first document.
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { createRoot } from "react-dom/client";
import "../../index.css";
import "./g.css";
import { type DataSource, DataSourceContext } from "./api";
import { DataPanel, type DataPanelView } from "./DataPanel";
import type { DataInfo, DataPage, DataSchema } from "./types";

const t = (s: string) => JSON.stringify(s);
const doc = (
  id: string,
  status: string,
  total: string,
  name: string,
  items: number,
  coupon?: string,
) => [
  `{"$oid":"${id}"}`,
  t(status),
  `{"email":{"$masked":true},"name":${t(name)},"tier":"gold"}`,
  `{"$numberDecimal":"${total}"}`,
  JSON.stringify(
    Array.from({ length: items }, (_, i) => ({
      sku: `SKU-${i}`,
      qty: { $numberInt: "1" },
    })),
  ),
  coupon ? t(coupon) : null,
  `{"$date":"2026-11-27T09:${String(14 - items).padStart(2, "0")}:02.000Z"}`,
];

const page: DataPage = {
  columns: [
    { name: "_id", type_name: "bson:objectId" },
    { name: "status", type_name: "bson:string" },
    { name: "customer", type_name: "bson:object" },
    { name: "total", type_name: "bson:decimal" },
    { name: "items", type_name: "bson:array" },
    { name: "coupon", type_name: "bson:string" },
    { name: "createdAt", type_name: "bson:date" },
  ],
  rows: [
    doc("6745f0c2e1b7a3d4c5f2a91e", "open", "249.00", "Ada Lovelace", 3),
    doc(
      "6745ef81e1b7a3d4c5f277c0",
      "open",
      "118.50",
      "Grace Hopper",
      1,
      "BLACKFRI",
    ),
    doc("6745ee10e1b7a3d4c5f20b2d", "open", "1204.00", "Alan Turing", 12),
    doc("6745eb3ae1b7a3d4c5f2f4e1", "open", "310.00", "Katherine Johnson", 2),
    doc(
      "6745e902e1b7a3d4c5f219aa",
      "open",
      "142.20",
      "Edsger Dijkstra",
      4,
      "WELCOME",
    ),
  ].map((r) => JSON.parse(JSON.stringify(r))),
  estimated_rows: 12400,
};

const schema = {
  read_at: new Date().toISOString(),
  tables: ["events", "orders", "sessions", "users"].map((name) => ({
    schema: "app",
    name,
    kind: "table",
    estimated_rows: name === "orders" ? 12400 : 1200,
    size_bytes: 0,
    editable: true,
    primary_key: ["_id"],
    foreign_keys: [],
    indexes: [],
    constraints: [],
    columns: (page.columns ?? []).map((c) => ({
      name: c.name,
      type: c.type_name.slice(5),
      type_name: c.type_name,
      nullable: true,
      generated: false,
      sensitive: c.name === "customer",
    })),
  })),
} as unknown as DataSchema;

const info = {
  engine: "mongodb",
  status: "ready",
  detected_by: "setting",
  server_version: "MongoDB 8.0.38",
  used_by: [],
  access: {
    level: "write",
    sensitive: "visible",
    confirm_commit: false,
    why: "",
  },
} as unknown as DataInfo;

const base: Partial<DataSource> = {
  key: ["fixture"],
  cli: "-s shop-db",
  roles: {
    reader: "dock_reader",
    writer: "dock_writer",
    masked: "dock_reader_masked",
  },
  words: {
    product: "Dock",
    owner: "the root user",
    target: "Environment",
    unreachable: "",
    notRunning: "",
    queryStatsOff: "",
    stagesQueryStats: false,
  },
  confirmName: "production",
  info: async () => info,
  schema: async () => schema,
  rows: async () => page,
  references: async () => [],
  history: async () => [],
  savedQueries: async () => [],
  changesets: async () => [],
};
const source = new Proxy(base, {
  get: (o, k) =>
    k in o
      ? o[k as keyof DataSource]
      : () => Promise.reject(new Error(`${String(k)} isn't in the fixture`)),
}) as DataSource;

function App() {
  const [view, setView] = useState<DataPanelView>({
    tab: "tables",
    table: "app.orders",
  });
  return (
    <QueryClientProvider client={new QueryClient()}>
      <DataSourceContext value={source}>
        <div className="g p-4">
          <DataPanel view={view} onView={setView} className="h-[700px]" />
        </div>
      </DataSourceContext>
    </QueryClientProvider>
  );
}

const root = document.getElementById("root");
if (root) createRoot(root).render(<App />);
