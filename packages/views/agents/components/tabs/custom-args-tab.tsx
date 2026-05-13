"use client";

import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2, Plus, Save, Trash2 } from "lucide-react";
import type { Agent, RuntimeDevice } from "@multica/core/types";
import { runtimeModelsOptions } from "@multica/core/runtimes";
import { createSafeId } from "@multica/core/utils";
import { Button } from "@multica/ui/components/ui/button";
import { Input } from "@multica/ui/components/ui/input";
import {
  NativeSelect,
  NativeSelectOption,
} from "@multica/ui/components/ui/native-select";
import { toast } from "sonner";
import { useT } from "../../../i18n";
import {
  buildDroidEffectiveArgsPreview,
  getDroidReasoningEffort,
  getDroidReasoningSpec,
  setDroidReasoningEffort,
} from "../../lib/droid-effective-args";

interface ArgEntry {
  id: string;
  value: string;
}

function argsToEntries(args: string[]): ArgEntry[] {
  return args.map((value) => ({ id: createSafeId(), value }));
}

// Each row may contain a single arg ("--model") or several space-separated
// tokens ("--model claude-sonnet-4"). We split on whitespace so users can
// paste multi-token flags into one row without having to break them apart
// manually. The placeholder + helper text explain this so users aren't
// surprised when "--flag value" lands as two args at the back-end.
function entriesToArgs(entries: ArgEntry[]): string[] {
  return entries.flatMap((e) => e.value.trim().split(/\s+/)).filter(Boolean);
}

export function CustomArgsTab({
  agent,
  runtimeDevice,
  onSave,
  onDirtyChange,
}: {
  agent: Agent;
  runtimeDevice?: RuntimeDevice;
  onSave: (updates: Partial<Agent>) => Promise<void>;
  onDirtyChange?: (dirty: boolean) => void;
}) {
  const { t } = useT("agents");
  const [entries, setEntries] = useState<ArgEntry[]>(
    argsToEntries(agent.custom_args ?? []),
  );
  const [saving, setSaving] = useState(false);

  const currentArgs = entriesToArgs(entries);
  const originalArgs = agent.custom_args ?? [];
  const dirty = JSON.stringify(currentArgs) !== JSON.stringify(originalArgs);
  const modelsQuery = useQuery(
    runtimeModelsOptions(
      runtimeDevice?.status === "online" ? runtimeDevice.id : null,
    ),
  );
  const discoveredModel = modelsQuery.data?.models.find(
    (model) => model.id === agent.model,
  );
  const reasoningSpec = getDroidReasoningSpec(
    runtimeDevice?.provider,
    agent.model,
    discoveredModel?.reasoning,
  );
  const reasoningEffort =
    getDroidReasoningEffort(currentArgs) ?? reasoningSpec?.defaultLevel ?? "";
  const effectiveArgs = buildDroidEffectiveArgsPreview(
    runtimeDevice?.provider,
    agent.model,
    currentArgs,
    reasoningSpec,
  );

  useEffect(() => {
    onDirtyChange?.(dirty);
  }, [dirty, onDirtyChange]);

  const addEntry = () => {
    setEntries([...entries, { id: createSafeId(), value: "" }]);
  };

  const removeEntry = (index: number) => {
    setEntries(entries.filter((_, i) => i !== index));
  };

  const updateEntry = (index: number, value: string) => {
    setEntries(
      entries.map((entry, i) => (i === index ? { ...entry, value } : entry)),
    );
  };

  const updateReasoningEffort = (value: string) => {
    setEntries(argsToEntries(setDroidReasoningEffort(currentArgs, value)));
  };

  const handleSave = async () => {
    setSaving(true);
    try {
      await onSave({ custom_args: currentArgs });
      toast.success(t(($) => $.tab_body.custom_args.saved_toast));
    } catch {
      toast.error(t(($) => $.tab_body.custom_args.save_failed_toast));
    } finally {
      setSaving(false);
    }
  };

  const launchHeader = runtimeDevice?.launch_header;

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-3">
        <div className="space-y-1">
          <p className="text-xs text-muted-foreground">
            {t(($) => $.tab_body.custom_args.intro)}
          </p>
          {launchHeader && (
            <p className="text-xs text-muted-foreground">
              {t(($) => $.tab_body.custom_args.launch_mode_prefix)}
              <code className="rounded bg-muted px-1 py-0.5 font-mono text-[11px]">
                {launchHeader} {t(($) => $.tab_body.custom_args.launch_mode_args_placeholder)}
              </code>
            </p>
          )}
          {launchHeader && effectiveArgs.length > 0 && (
            <p className="max-w-full break-all text-xs text-muted-foreground">
              {t(($) => $.tab_body.custom_args.effective_args_prefix)}
              <code className="rounded bg-muted px-1 py-0.5 font-mono text-[11px]">
                {launchHeader} {effectiveArgs.join(" ")}
              </code>
            </p>
          )}
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={addEntry}
          className="shrink-0"
        >
          <Plus className="h-3 w-3" />
          {t(($) => $.tab_body.common.add)}
        </Button>
      </div>

      {reasoningSpec && (
        <div className="flex items-center justify-between gap-3 rounded-lg border border-border bg-muted/20 px-3 py-2.5">
          <div className="min-w-0 space-y-0.5">
            <div className="text-xs font-medium">
              {t(($) => $.tab_body.custom_args.reasoning_effort_label)}
            </div>
            <div className="text-xs text-muted-foreground">
              {t(($) => $.tab_body.custom_args.reasoning_effort_hint, {
                model: reasoningSpec.label,
              })}
            </div>
          </div>
          <NativeSelect
            size="sm"
            value={reasoningEffort}
            onChange={(e) => updateReasoningEffort(e.target.value)}
            aria-label={t(($) => $.tab_body.custom_args.reasoning_effort_label)}
            className="shrink-0"
          >
            {reasoningSpec.levels.map((level) => (
              <NativeSelectOption key={level} value={level}>
                {level}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </div>
      )}

      {entries.length > 0 && (
        <div className="space-y-2">
          {entries.map((entry, index) => (
            <div key={entry.id} className="flex items-center gap-2">
              <Input
                value={entry.value}
                onChange={(e) => updateEntry(index, e.target.value)}
                placeholder={t(($) => $.tab_body.custom_args.input_placeholder)}
                className="flex-1 font-mono text-xs"
              />
              <Button
                variant="ghost"
                size="icon-sm"
                onClick={() => removeEntry(index)}
                className="text-muted-foreground hover:text-destructive"
                aria-label={t(($) => $.tab_body.custom_args.remove_aria)}
              >
                <Trash2 className="h-3.5 w-3.5" />
              </Button>
            </div>
          ))}
        </div>
      )}

      <div className="flex items-center justify-end gap-3">
        {dirty && (
          <span className="text-xs text-muted-foreground">{t(($) => $.tab_body.common.unsaved_changes)}</span>
        )}
        <Button onClick={handleSave} disabled={!dirty || saving} size="sm">
          {saving ? (
            <Loader2 className="h-3.5 w-3.5 animate-spin" />
          ) : (
            <Save className="h-3.5 w-3.5" />
          )}
          {t(($) => $.tab_body.common.save)}
        </Button>
      </div>
    </div>
  );
}
