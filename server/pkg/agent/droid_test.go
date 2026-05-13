package agent

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDroidExecuteParsesJSONResult(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	dir := t.TempDir()
	fakePath := filepath.Join(dir, "droid")
	argsFile := filepath.Join(dir, "args")
	stdinFile := filepath.Join(dir, "stdin")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$DROID_ARGS_FILE"
cat > "$DROID_STDIN_FILE"
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-1"}'
printf '%s\n' '{"type":"message","role":"assistant","text":"droid ok","session_id":"sess-1"}'
printf '%s\n' '{"type":"completion","finalText":"droid ok","durationMs":1234,"session_id":"sess-1","usage":{"input_tokens":11,"output_tokens":7,"cache_read_input_tokens":3,"cache_creation_input_tokens":5,"thinking_tokens":2}}'
`
	writeTestExecutable(t, fakePath, []byte(script))

	backend := &droidBackend{
		cfg: Config{
			ExecutablePath: fakePath,
			Env: map[string]string{
				"DROID_ARGS_FILE":  argsFile,
				"DROID_STDIN_FILE": stdinFile,
			},
			Logger: slog.Default(),
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "hello droid", ExecOptions{
		Cwd:             dir,
		Model:           "claude-opus-4-7",
		SystemPrompt:    "system hint",
		ResumeSessionID: "resume-1",
		Timeout:         5 * time.Second,
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var textMessages []Message
	for msg := range session.Messages {
		if msg.Type == MessageText {
			textMessages = append(textMessages, msg)
		}
	}

	result := <-session.Result
	if result.Status != "completed" {
		t.Fatalf("expected completed, got status=%q error=%q", result.Status, result.Error)
	}
	if result.Output != "droid ok" {
		t.Fatalf("unexpected output %q", result.Output)
	}
	if result.SessionID != "sess-1" {
		t.Fatalf("unexpected session id %q", result.SessionID)
	}
	if result.DurationMs != 1234 {
		t.Fatalf("expected CLI duration 1234ms, got %d", result.DurationMs)
	}
	usage := result.Usage["claude-opus-4-7"]
	if usage.InputTokens != 11 || usage.OutputTokens != 7 || usage.CacheReadTokens != 3 || usage.CacheWriteTokens != 5 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if len(textMessages) != 1 || textMessages[0].Content != "droid ok" || textMessages[0].SessionID != "sess-1" {
		t.Fatalf("unexpected text messages: %+v", textMessages)
	}

	stdinBytes, err := os.ReadFile(stdinFile)
	if err != nil {
		t.Fatalf("read stdin file: %v", err)
	}
	if string(stdinBytes) != "hello droid" {
		t.Fatalf("expected prompt on stdin, got %q", string(stdinBytes))
	}
	argsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("read args file: %v", err)
	}
	args := string(argsBytes)
	for _, want := range []string{
		"exec", "--output-format", "stream-json", "--model", "claude-opus-4-7",
		"--append-system-prompt", "system hint", "--cwd", dir,
		"--session-id", "resume-1", "--auto", "high",
	} {
		if !strings.Contains(args, want) {
			t.Fatalf("expected args to contain %q, got:\n%s", want, args)
		}
	}
}

func TestBuildDroidArgsCustomAutonomySuppressesDefault(t *testing.T) {
	t.Parallel()
	args := buildDroidArgs(ExecOptions{
		Cwd:        "/tmp/work",
		CustomArgs: []string{"--auto", "high"},
	}, slog.Default())

	got := strings.Join(args, " ")
	if strings.Count(got, "--auto") != 1 {
		t.Fatalf("default autonomy should be suppressed when custom autonomy is present: %v", args)
	}
	if !strings.Contains(got, "--auto high") {
		t.Fatalf("expected custom autonomy to pass through: %v", args)
	}
}

func TestBuildDroidArgsDefaultModelOmitsModelFlag(t *testing.T) {
	t.Parallel()
	args := buildDroidArgs(ExecOptions{
		Model: droidDefaultModelID,
		Cwd:   "/tmp/work",
	}, slog.Default())

	got := strings.Join(args, " ")
	if strings.Contains(got, "--model") || strings.Contains(got, droidDefaultModelID) {
		t.Fatalf("droid default model should use the CLI default without --model: %v", args)
	}
	if !strings.Contains(got, "--output-format stream-json") {
		t.Fatalf("expected stream JSON output format to remain: %v", args)
	}
}

func TestBuildDroidArgsCustomBYOKModelPassesModelFlag(t *testing.T) {
	t.Parallel()
	args := buildDroidArgs(ExecOptions{
		Model: "custom:GPT-5.5-1",
		Cwd:   "/tmp/work",
	}, slog.Default())

	got := strings.Join(args, " ")
	if !strings.Contains(got, "--model custom:GPT-5.5-1") {
		t.Fatalf("custom droid BYOK model must be passed through to the droid CLI: %v", args)
	}
	if !strings.Contains(got, "--auto high") || !strings.Contains(got, "--reasoning-effort low") {
		t.Fatalf("custom GPT-5.5 BYOK should default to high autonomy and low reasoning: %v", args)
	}
	if strings.Contains(got, "api") || strings.Contains(got, "key") {
		t.Fatalf("model selection args must not contain BYOK secret material: %v", args)
	}
}

func TestBuildDroidArgsCustomReasoningSuppressesGPT55Default(t *testing.T) {
	t.Parallel()
	args := buildDroidArgs(ExecOptions{
		Model:      "custom:GPT-5.5-1",
		CustomArgs: []string{"--reasoning-effort", "xhigh"},
	}, slog.Default())

	got := strings.Join(args, " ")
	if strings.Count(got, "--reasoning-effort") != 1 {
		t.Fatalf("expected one reasoning flag, got: %v", args)
	}
	if !strings.Contains(got, "--reasoning-effort xhigh") {
		t.Fatalf("expected custom reasoning to pass through: %v", args)
	}
}

func TestBuildDroidArgsDetectsGPT55FromSettingsModelID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsDir := filepath.Join(home, ".factory")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{
  "customModels": [
    {
      "id": "custom:primary-openai-model",
      "displayName": "Primary OpenAI",
      "model": "gpt-5.5"
    }
  ]
}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	args := buildDroidArgs(ExecOptions{
		Model: "custom:primary-openai-model",
	}, slog.Default())

	got := strings.Join(args, " ")
	if !strings.Contains(got, "--reasoning-effort low") {
		t.Fatalf("expected settings-backed GPT-5.5 custom model to use low reasoning: %v", args)
	}
}

func TestBuildDroidArgsUsesSettingsReasoningForOtherBYOKModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	settingsDir := filepath.Join(home, ".factory")
	if err := os.MkdirAll(settingsDir, 0o700); err != nil {
		t.Fatalf("mkdir settings dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), []byte(`{
  "customModels": [
    {
      "id": "custom:local-kimi",
      "displayName": "Kimi K2.5",
      "model": "kimi-k2.5",
      "provider": "generic-chat-completion-api",
      "reasoningEffort": "max"
    }
  ]
}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}

	args := buildDroidArgs(ExecOptions{
		Model: "custom:local-kimi",
	}, slog.Default())

	got := strings.Join(args, " ")
	if !strings.Contains(got, "--reasoning-effort max") {
		t.Fatalf("expected non-GPT BYOK custom model to inherit settings reasoning: %v", args)
	}
}

func TestBuildDroidArgsFiltersProtocolCriticalArgs(t *testing.T) {
	t.Parallel()
	args := buildDroidArgs(ExecOptions{
		CustomArgs: []string{"--output-format", "text", "--input-format", "stream-jsonrpc", "--file", "x.md", "--auto=low"},
	}, slog.Default())

	got := strings.Join(args, " ")
	if strings.Contains(got, "text") || strings.Contains(got, "stream-jsonrpc") || strings.Contains(got, "x.md") {
		t.Fatalf("blocked flag values leaked through: %v", args)
	}
	if !strings.Contains(got, "--output-format stream-json") {
		t.Fatalf("expected daemon-owned output format to remain: %v", args)
	}
	if !strings.Contains(got, "--auto=low") {
		t.Fatalf("expected inline autonomy arg to pass through: %v", args)
	}
}

func TestBuildDroidArgsExtraArgsBeforeCustomArgsAndFiltersBoth(t *testing.T) {
	t.Parallel()
	args := buildDroidArgs(ExecOptions{
		ExtraArgs:  []string{"--reasoning-effort", "medium", "--output-format", "text"},
		CustomArgs: []string{"--auto", "low", "--input-format=stream-jsonrpc"},
	}, slog.Default())

	got := strings.Join(args, " ")
	if strings.Contains(got, "text") || strings.Contains(got, "stream-jsonrpc") {
		t.Fatalf("blocked daemon/user args leaked through: %v", args)
	}
	reasoningIdx := strings.Index(got, "--reasoning-effort medium")
	autoIdx := strings.Index(got, "--auto low")
	if reasoningIdx < 0 || autoIdx < 0 || reasoningIdx > autoIdx {
		t.Fatalf("expected extra args before custom args, got: %v", args)
	}
}

func TestParseDroidStreamEventRejectsNonJSON(t *testing.T) {
	t.Parallel()
	_, err := parseDroidStreamEvent([]byte("not-json"))
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "parse droid stream event") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDroidExecuteStreamsToolMessages(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fixture is POSIX-only")
	}

	dir := t.TempDir()
	fakePath := filepath.Join(dir, "droid")
	script := `#!/bin/sh
cat >/dev/null
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-tools"}'
printf '%s\n' '{"type":"tool_call","id":"call-1","toolId":"Execute","toolName":"Execute","parameters":{"command":"pwd"},"session_id":"sess-tools"}'
printf '%s\n' '{"type":"tool_result","id":"call-1","toolId":"Execute","isError":false,"value":"/tmp/project\n\n[Process exited with code 0]","session_id":"sess-tools"}'
printf '%s\n' '{"type":"message","role":"assistant","text":"done","session_id":"sess-tools"}'
printf '%s\n' '{"type":"completion","finalText":"done","durationMs":99,"session_id":"sess-tools","usage":{"input_tokens":1,"output_tokens":2}}'
`
	writeTestExecutable(t, fakePath, []byte(script))

	backend := &droidBackend{cfg: Config{ExecutablePath: fakePath, Logger: slog.Default()}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	session, err := backend.Execute(ctx, "run pwd", ExecOptions{Cwd: dir, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}

	var messages []Message
	for msg := range session.Messages {
		if msg.Type == MessageToolUse || msg.Type == MessageToolResult || msg.Type == MessageText {
			messages = append(messages, msg)
		}
	}
	result := <-session.Result
	if result.Status != "completed" || result.Output != "done" || result.SessionID != "sess-tools" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(messages) != 3 {
		t.Fatalf("expected streamed messages, got %+v", messages)
	}
	if messages[0].Type != MessageToolUse || messages[0].Tool != "Execute" || messages[0].Input["command"] != "pwd" {
		t.Fatalf("unexpected tool use: %+v", messages[0])
	}
	if messages[1].Type != MessageToolResult || !strings.Contains(messages[1].Output, "/tmp/project") {
		t.Fatalf("unexpected tool result: %+v", messages[1])
	}
	if messages[2].Type != MessageText || messages[2].Content != "done" {
		t.Fatalf("unexpected text message: %+v", messages[2])
	}
}
