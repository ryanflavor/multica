export interface DroidReasoningSpec {
  flag: "--reasoning-effort";
  levels: string[];
  defaultLevel: string;
  label: string;
}

const DROID_GPT55_REASONING: DroidReasoningSpec = {
  flag: "--reasoning-effort",
  levels: ["low", "medium", "high", "xhigh"],
  defaultLevel: "low",
  label: "GPT-5.5 / OpenAI BYOK",
};

export function getDroidReasoningSpec(
  provider: string | undefined,
  model: string | undefined,
  discoveredSpec?: DroidReasoningSpec,
): DroidReasoningSpec | null {
  if (provider !== "droid") return null;
  if (isValidReasoningSpec(discoveredSpec)) return discoveredSpec;
  return inferDroidReasoningSpec(model);
}

export function buildDroidEffectiveArgsPreview(
  provider: string | undefined,
  model: string | undefined,
  args: string[],
  discoveredSpec?: DroidReasoningSpec | null,
): string[] {
  if (provider !== "droid") return args;
  const out: string[] = [];
  if (!droidArgsSetAutonomy(args)) out.push("--auto", "high");
  const spec = discoveredSpec ?? getDroidReasoningSpec(provider, model);
  if (spec && !droidArgsSetReasoningEffort(args)) {
    out.push(spec.flag, spec.defaultLevel);
  }
  out.push(...args);
  return out;
}

export function getDroidReasoningEffort(args: string[]): string | null {
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i] ?? "";
    if (arg === "--reasoning-effort" || arg === "-r") {
      const next = args[i + 1];
      return next && !next.startsWith("-") ? next : null;
    }
    if (arg.startsWith("--reasoning-effort=")) {
      return arg.slice("--reasoning-effort=".length) || null;
    }
  }
  return null;
}

export function setDroidReasoningEffort(args: string[], value: string): string[] {
  const next: string[] = [];
  for (let i = 0; i < args.length; i += 1) {
    const arg = args[i] ?? "";
    if (arg === "--reasoning-effort" || arg === "-r") {
      i += 1;
      continue;
    }
    if (arg.startsWith("--reasoning-effort=")) {
      continue;
    }
    next.push(arg);
  }
  next.push("--reasoning-effort", value);
  return next;
}

function droidArgsSetAutonomy(args: string[]): boolean {
  for (const arg of args) {
    if (arg === "--auto" || arg === "--skip-permissions-unsafe") return true;
    if (arg.startsWith("--auto=")) return true;
  }
  return false;
}

function droidArgsSetReasoningEffort(args: string[]): boolean {
  return getDroidReasoningEffort(args) !== null;
}

function inferDroidReasoningSpec(model: string | undefined): DroidReasoningSpec | null {
  if (!model) return null;
  const normalized = model.toLowerCase();
  if (
    normalized === "gpt-5.5" ||
    normalized.includes("gpt-5.5") ||
    normalized.includes("gpt_5_5")
  ) {
    return DROID_GPT55_REASONING;
  }
  if (normalized.includes("gpt-5.2") && !normalized.includes("codex")) {
    return {
      flag: "--reasoning-effort",
      levels: ["off", "low", "medium", "high", "xhigh"],
      defaultLevel: "low",
      label: "OpenAI GPT",
    };
  }
  if (normalized.includes("gpt-") || normalized.includes("codex")) {
    return {
      flag: "--reasoning-effort",
      levels: ["low", "medium", "high", "xhigh"],
      defaultLevel: normalized.includes("gpt-5.4-mini") ? "high" : "medium",
      label: "OpenAI GPT",
    };
  }
  if (normalized.includes("claude-opus-4.7") || normalized.includes("claude-opus-4-7")) {
    return {
      flag: "--reasoning-effort",
      levels: ["off", "low", "medium", "high", "xhigh", "max"],
      defaultLevel: "high",
      label: "Claude",
    };
  }
  if (normalized.includes("claude-")) {
    return {
      flag: "--reasoning-effort",
      levels: ["off", "low", "medium", "high", "max"],
      defaultLevel: "high",
      label: "Claude",
    };
  }
  if (normalized.includes("gemini-3-flash")) {
    return {
      flag: "--reasoning-effort",
      levels: ["minimal", "low", "medium", "high"],
      defaultLevel: "high",
      label: "Gemini",
    };
  }
  if (normalized.includes("gemini-")) {
    return {
      flag: "--reasoning-effort",
      levels: ["low", "medium", "high"],
      defaultLevel: "high",
      label: "Gemini",
    };
  }
  if (normalized.startsWith("custom:")) {
    return {
      flag: "--reasoning-effort",
      levels: ["off", "low", "medium", "high", "xhigh", "max"],
      defaultLevel: "high",
      label: "Droid BYOK",
    };
  }
  return null;
}

function isValidReasoningSpec(
  spec: DroidReasoningSpec | undefined,
): spec is DroidReasoningSpec {
  return Boolean(
    spec &&
      spec.flag === "--reasoning-effort" &&
      spec.levels.length > 0 &&
      spec.defaultLevel &&
      spec.levels.includes(spec.defaultLevel),
  );
}
