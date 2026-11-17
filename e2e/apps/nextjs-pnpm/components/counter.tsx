"use client";
import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { useState } from "react";

function Clock() {
  const { data } = useQuery({ queryKey: ["now"], queryFn: async () => new Date().toISOString() });
  return <span>{data}</span>;
}

export function Counter() {
  const [client] = useState(() => new QueryClient());
  const [n, setN] = useState(0);
  return (
    <QueryClientProvider client={client}>
      <button type="button" className="rounded bg-white/10 px-3 py-1" onClick={() => setN(n + 1)}>
        clicked {n}
      </button>
      <Clock />
    </QueryClientProvider>
  );
}
