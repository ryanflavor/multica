import { describe, expect, it } from "vitest";
import {
  buildDroidEffectiveArgsPreview,
  getDroidReasoningEffort,
  getDroidReasoningSpec,
  setDroidReasoningEffort,
} from "./droid-effective-args";

describe("Droid effective args", () => {
  it("defaults Droid GPT-5.5 BYOK to high autonomy and low reasoning", () => {
    expect(getDroidReasoningSpec("droid", "custom:GPT-5.5-1")?.levels).toEqual([
      "off",
      "low",
      "medium",
      "high",
      "xhigh",
    ]);
    expect(
      buildDroidEffectiveArgsPreview("droid", "custom:GPT-5.5-1", []),
    ).toEqual(["--auto", "high", "--reasoning-effort", "low"]);
  });

  it("uses model-specific reasoning levels for other Droid BYOK models", () => {
    const spec = getDroidReasoningSpec("droid", "custom:Claude-Opus-4.7-0");
    expect(spec?.levels).toEqual(["off", "low", "medium", "high", "xhigh", "max"]);
    expect(
      buildDroidEffectiveArgsPreview("droid", "custom:Claude-Opus-4.7-0", []),
    ).toEqual(["--auto", "high", "--reasoning-effort", "high"]);
  });

  it("recognizes Droid hyphenated Claude model ids", () => {
    const spec = getDroidReasoningSpec("droid", "claude-opus-4-7");
    expect(spec?.levels).toEqual(["off", "low", "medium", "high", "xhigh", "max"]);
  });

  it("prefers discovered Droid BYOK reasoning metadata over string inference", () => {
    expect(
      buildDroidEffectiveArgsPreview(
        "droid",
        "custom:opaque-model-id",
        [],
        {
          flag: "--reasoning-effort",
          levels: ["off", "low", "medium", "high", "xhigh", "max"],
          defaultLevel: "max",
          label: "Droid BYOK",
        },
      ),
    ).toEqual(["--auto", "high", "--reasoning-effort", "max"]);
  });

  it("preserves explicit autonomy and reasoning choices", () => {
    expect(
      buildDroidEffectiveArgsPreview("droid", "custom:GPT-5.5-1", [
        "--auto",
        "medium",
        "--reasoning-effort",
        "xhigh",
      ]),
    ).toEqual(["--auto", "medium", "--reasoning-effort", "xhigh"]);
  });

  it("replaces existing reasoning flags", () => {
    const args = setDroidReasoningEffort(
      ["--auto", "high", "-r", "high", "--foo", "bar"],
      "low",
    );
    expect(args).toEqual(["--auto", "high", "--foo", "bar", "--reasoning-effort", "low"]);
    expect(getDroidReasoningEffort(args)).toBe("low");
  });
});
