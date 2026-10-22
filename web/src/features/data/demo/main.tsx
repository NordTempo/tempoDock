import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { StrictMode, useMemo, useState } from "react";
import { createRoot } from "react-dom/client";
import { DataSourceContext } from "../api";
import { DataPanel, type DataPanelView } from "../DataPanel";
import {
  fixtureSource,
  renderChangesSql,
  type Scenario,
  scenarios,
} from "../fixtures";
import type { EditActions } from "../ReviewPanel";
import "../../../index.css";

// Dev-only: the database panel over fixtures, for building and screenshots.
// ?state=<scenario>&tab=tables|sql|schema&table=public.orders&env=staging

const params = new URLSearchParams(location.search);
const names = Object.keys(scenarios) as Scenario[];
const initialState = names.find((n) => n === params.get("state")) ?? "admin";
const tabs: DataPanelView["tab"][] = ["tables", "sql", "schema"];

const edit: EditActions = {
  renderSql: async (changes) => renderChangesSql(changes),
  commit: async (changes, confirm) => {
    console.info("commit", { changes, confirm });
  },
};

function Demo() {
  const [scenario, setScenario] = useState<Scenario>(initialState);
  const [view, setView] = useState<DataPanelView>({
    tab: tabs.find((t) => t === params.get("tab")) ?? "tables",
    table: params.get("table") ?? "public.orders",
  });
  const environment =
    params.get("env") ?? (scenario === "production" ? "production" : "staging");
  // A fresh source and cache per scenario, so states don't leak.
  const { source, client } = useMemo(
    () => ({ source: fixtureSource(scenario), client: new QueryClient() }),
    [scenario],
  );

  return (
    <main className="flex flex-col gap-4 p-5">
      <div className="flex flex-wrap items-baseline gap-x-3.5 gap-y-1.5">
        <h1 className="text-xl font-semibold">postgres</h1>
        <span className="text-mut">
          {environment} · data panel demo (fixtures)
        </span>
        <select
          aria-label="Scenario"
          className="field ml-auto py-1 text-xs"
          value={scenario}
          onChange={(e) =>
            setScenario(names.find((n) => n === e.target.value) ?? "admin")
          }
        >
          {names.map((n) => (
            <option key={n}>{n}</option>
          ))}
        </select>
      </div>
      <QueryClientProvider client={client}>
        <DataSourceContext value={source}>
          <DataPanel
            key={scenario}
            serviceRef={{ project: "shop", environment, service: "postgres" }}
            view={view}
            onView={setView}
            edit={edit}
            height="h-[calc(100vh-100px)] min-h-[520px]"
          />
        </DataSourceContext>
      </QueryClientProvider>
    </main>
  );
}

const root = document.getElementById("root");
if (root) {
  createRoot(root).render(
    <StrictMode>
      <Demo />
    </StrictMode>,
  );
}
