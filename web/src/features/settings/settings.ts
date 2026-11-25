import type { SettingsPatch } from "../../data/types";
import { mb, parseMb } from "../../ui/format";

/** A service setting, named as in the API (`memory_mb`, `cpus`, ...). */
export type SettingKey = Exclude<keyof SettingsPatch, "apply">;

/** The settings edited as text: all but the on/off ones. */
export type TextSettingKey = {
  [K in SettingKey]: NonNullable<SettingsPatch[K]> extends boolean ? never : K;
}[SettingKey];

/** The on/off settings, edited as a checkbox. */
export type FlagSettingKey = Exclude<SettingKey, TextSettingKey>;

export const settingLabel: Record<SettingKey, string> = {
  image: "image",
  port: "port",
  start_command: "start command",
  health_check_path: "health check path",
  health_check_timeout_seconds: "health check timeout",
  health_check_interval_seconds: "health check interval",
  health_check_probe_timeout_seconds: "health probe timeout",
  health_check_retries: "health check retries",
  health_check_start_period_seconds: "health check start period",
  cpus: "cpu limit",
  memory_mb: "memory limit",
  dockerfile_path: "dockerfile",
  build_command: "build command",
  pre_deploy_command: "pre-deploy command",
  pre_deploy_timeout_seconds: "pre-deploy timeout",
  pre_deploy_snapshot: "pre-deploy snapshot",
};

export const isSettingKey = (field: string): field is SettingKey =>
  field in settingLabel;

/** A setting edited as text, e.g. from a link that opens its editor. */
export const isTextSettingKey = (field: string): field is TextSettingKey =>
  isSettingKey(field) && field !== "pre_deploy_snapshot";

const numeric = new Set<TextSettingKey>([
  "port",
  "health_check_timeout_seconds",
  "health_check_interval_seconds",
  "health_check_probe_timeout_seconds",
  "health_check_retries",
  "health_check_start_period_seconds",
  "cpus",
  "memory_mb",
  "pre_deploy_timeout_seconds",
]);

/**
 * Shows a setting. Zero values mean the platform default, so say which.
 * Staged changes carry on/off settings as "true" and "false".
 */
export function formatSetting(
  key: SettingKey,
  value: string | number | boolean,
) {
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
    case "health_check_interval_seconds":
      return n < 0 ? "off" : n ? `${n}s` : "30s (default)";
    case "health_check_probe_timeout_seconds":
      return n ? `${n}s` : "5s (default)";
    case "health_check_retries":
      return n ? String(n) : "3 (default)";
    case "health_check_start_period_seconds":
      return n ? `${n}s` : "same as timeout";
    case "cpus":
      return n ? String(n) : "no limit";
    case "memory_mb":
      return n ? mb(n) : "no limit";
    case "dockerfile_path":
      return String(value) || "Dockerfile";
    case "build_command":
      return String(value) || "none";
    case "pre_deploy_command":
      return String(value) || "none";
    case "pre_deploy_timeout_seconds":
      return n ? `${n}s` : "600s (default)";
    case "pre_deploy_snapshot":
      return String(value) === "true" ? "on" : "off";
  }
}

/** The text an editor starts from: empty for defaults, "off" for -1. */
export function editText(key: TextSettingKey, value: string | number) {
  if (!numeric.has(key)) return String(value);
  const n = Number(value);
  if (!n) return "";
  if (n < 0) return "off";
  return key === "memory_mb" ? mb(n) : String(n);
}

/** What an empty editor shows: the default, and for the interval how to turn the check off. */
export const editPlaceholder = (key: TextSettingKey) =>
  key === "health_check_interval_seconds"
    ? "30s, or off"
    : formatSetting(key, "");

/**
 * Validates input for one setting and builds the patch to stage. Empty
 * input means the default (zero); "off" turns the ongoing health check
 * off (interval -1).
 */
export function parseSetting(
  key: TextSettingKey,
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
    case "health_check_interval_seconds": {
      if (input.toLowerCase() === "off") {
        return { health_check_interval_seconds: -1 };
      }
      const seconds = int(3600);
      return seconds === null
        ? new Error("Interval in whole seconds up to 3600, or off")
        : { health_check_interval_seconds: seconds };
    }
    case "health_check_probe_timeout_seconds": {
      const seconds = int(300);
      return seconds === null
        ? new Error("Probe timeout in whole seconds up to 300")
        : { health_check_probe_timeout_seconds: seconds };
    }
    case "health_check_retries": {
      const retries = int(100);
      return retries === null
        ? new Error("Retries must be 0 to 100")
        : { health_check_retries: retries };
    }
    case "health_check_start_period_seconds": {
      const seconds = int(3600);
      return seconds === null
        ? new Error("Start period in whole seconds up to 3600")
        : { health_check_start_period_seconds: seconds };
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
    case "pre_deploy_command":
      return { pre_deploy_command: input };
    case "pre_deploy_timeout_seconds": {
      const seconds = int(3600);
      return seconds === null
        ? new Error("Timeout in whole seconds, at most 3600")
        : { pre_deploy_timeout_seconds: seconds };
    }
  }
}
