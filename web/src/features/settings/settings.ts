import type { EditableSettings, NewChange } from "../../data/types";
import { mb, parseMb } from "../../ui/format";

export type SettingKey = keyof EditableSettings;
type NewSettingChange = Extract<NewChange, { kind: "setting" }>;

export const settingLabel: Record<SettingKey, string> = {
  port: "port",
  healthCheckPath: "health check path",
  cpuLimit: "cpu limit",
  memoryLimitMb: "memory limit",
};

export function formatSetting(key: SettingKey, value: string | number) {
  if (typeof value === "string") return value || "none";
  if (key === "memoryLimitMb") return mb(value);
  if (key === "cpuLimit") return value.toFixed(1);
  return String(value);
}

/** Validates user input for one setting and builds the change to stage. */
export function settingChange(
  serviceId: string,
  key: SettingKey,
  current: EditableSettings,
  raw: string,
): NewSettingChange | Error {
  const base = { kind: "setting", serviceId } as const;
  const input = raw.trim();
  switch (key) {
    case "port": {
      const port = Number(input);
      return Number.isInteger(port) && port > 0 && port < 65536
        ? { ...base, key, before: current.port, after: port }
        : new Error("Port must be 1 to 65535");
    }
    case "healthCheckPath":
      return input === "" || input.startsWith("/")
        ? { ...base, key, before: current.healthCheckPath, after: input }
        : new Error("Path must start with /");
    case "cpuLimit": {
      const cpu = Number(input);
      return cpu > 0 && cpu <= 64
        ? { ...base, key, before: current.cpuLimit, after: cpu }
        : new Error("CPU limit must be between 0 and 64");
    }
    case "memoryLimitMb": {
      const memory = parseMb(input);
      return memory && memory >= 16
        ? { ...base, key, before: current.memoryLimitMb, after: memory }
        : new Error("Memory limit like 512M or 1G, at least 16M");
    }
  }
}
