import type { SettingsPatch } from "../../data/types";
import { mb, parseMb } from "../../ui/format";

/** A service setting, named as in the API (`memory_mb`, `cpus`, ...). */
export type SettingKey = Exclude<keyof SettingsPatch, "apply">;

export const settingLabel: Record<SettingKey, string> = {
  image: "image",
  port: "port",
  start_command: "start command",
  health_check_path: "health check path",
  health_check_timeout_seconds: "health check timeout",
  cpus: "cpu limit",
  memory_mb: "memory limit",
  dockerfile_path: "dockerfile",
  build_command: "build command",
};

export const isSettingKey = (field: string): field is SettingKey =>
  field in settingLabel;

const numeric = new Set<SettingKey>([
  "port",
  "health_check_timeout_seconds",
  "cpus",
  "memory_mb",
]);

/** Shows a setting. Zero values mean the platform default, so say which. */
export function formatSetting(key: SettingKey, value: string | number) {
  const n = Number(value);
  switch (key) {
    case "image":
      return String(value) || "none, deployed from source";
    case "port":
      return n ? String(n) : "8080 (default)";
    case "start_command":
      return String(value) || "image default";
    case "health_check_path":
      return String(value) || "none, TCP connect";
    case "health_check_timeout_seconds":
      return n ? `${n}s` : "default";
    case "cpus":
      return n ? String(n) : "no limit";
    case "memory_mb":
      return n ? mb(n) : "no limit";
    case "dockerfile_path":
      return String(value) || "Dockerfile";
    case "build_command":
      return String(value) || "none";
  }
}

/** The text an editor starts from: empty for defaults. */
export function editText(key: SettingKey, value: string | number) {
  if (!numeric.has(key)) return String(value);
  const n = Number(value);
  if (!n) return "";
  return key === "memory_mb" ? mb(n) : String(n);
}

/**
 * Validates input for one setting and builds the patch to stage. Empty
 * input means the default (zero).
 */
export function parseSetting(
  key: SettingKey,
  raw: string,
): SettingsPatch | Error {
  const input = raw.trim();
  const int = (max: number) => {
    const n = Number(input.replace(/s$/, "") || 0);
    return Number.isInteger(n) && n >= 0 && n <= max ? n : null;
  };
  switch (key) {
    case "port": {
      const port = int(65535);
      return port === null ? new Error("Port must be 0 to 65535") : { port };
    }
    case "health_check_timeout_seconds": {
      const seconds = int(3600);
      return seconds === null
        ? new Error("Timeout in whole seconds")
        : { health_check_timeout_seconds: seconds };
    }
    case "cpus": {
      const cpus = Number(input || 0);
      return cpus >= 0 && cpus <= 64
        ? { cpus }
        : new Error("CPU limit must be between 0 and 64");
    }
    case "memory_mb": {
      const memory = input ? parseMb(input) : 0;
      return memory === null || (memory > 0 && memory < 16)
        ? new Error("Memory limit like 512M or 1G, at least 16M")
        : { memory_mb: memory };
    }
    case "health_check_path":
      return input === "" || input.startsWith("/")
        ? { health_check_path: input }
        : new Error("Path must start with /");
    case "image":
      return { image: input };
    case "start_command":
      return { start_command: input };
    case "dockerfile_path":
      return { dockerfile_path: input };
    case "build_command":
      return { build_command: input };
  }
}

/** Whether a setting takes effect on the next build rather than a release. */
export const buildOnly = (key: SettingKey) =>
  key === "dockerfile_path" || key === "build_command";
