package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// droidBackend implements Backend by spawning `droid exec --output-format
// stream-json` and translating Droid's JSONL event stream into Multica task
// messages. That is what powers the issue-run transcript dialog.
type droidBackend struct {
	cfg Config
}

type droidUsageBlock struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
	CacheCreationTokens  int64 `json:"cache_creation_input_tokens"`
	ThinkingTokens       int64 `json:"thinking_tokens"`
}

type droidStreamEvent struct {
	Type       string          `json:"type"`
	Subtype    string          `json:"subtype,omitempty"`
	Role       string          `json:"role,omitempty"`
	ID         string          `json:"id,omitempty"`
	MessageID  string          `json:"messageId,omitempty"`
	ToolID     string          `json:"toolId,omitempty"`
	ToolName   string          `json:"toolName,omitempty"`
	Text       string          `json:"text,omitempty"`
	Value      json.RawMessage `json:"value,omitempty"`
	IsError    bool            `json:"isError,omitempty"`
	FinalText  string          `json:"finalText,omitempty"`
	SessionID  string          `json:"session_id,omitempty"`
	Parameters json.RawMessage `json:"parameters,omitempty"`
	Usage      droidUsageBlock `json:"usage,omitempty"`
	DurationMs int64           `json:"durationMs,omitempty"`
}

const droidDefaultModelID = "droid-default"

var droidBlockedArgs = map[string]blockedArgMode{
	"-o":              blockedWithValue,
	"--output-format": blockedWithValue,
	"--input-format":  blockedWithValue,
	"--cwd":           blockedWithValue,
	"-s":              blockedWithValue,
	"--session-id":    blockedWithValue,
	"--fork":          blockedWithValue,
	"-f":              blockedWithValue,
	"--file":          blockedWithValue,
}

func (b *droidBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	execPath := b.cfg.ExecutablePath
	if execPath == "" {
		execPath = "droid"
	}
	if _, err := exec.LookPath(execPath); err != nil {
		return nil, fmt.Errorf("droid executable not found at %q: %w", execPath, err)
	}

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 20 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)

	args := buildDroidArgs(opts, b.cfg.Logger)
	cmd := exec.CommandContext(runCtx, execPath, args...)
	hideAgentWindow(cmd)
	b.cfg.Logger.Info("agent command", "exec", execPath, "args", args)
	cmd.WaitDelay = 10 * time.Second
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	cmd.Env = buildEnv(b.cfg.Env)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("droid stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("droid stdout pipe: %w", err)
	}
	stderrBuf := newStderrTail(newLogWriter(b.cfg.Logger, "[droid:stderr] "), agentStderrTailBytes)
	cmd.Stderr = stderrBuf

	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("start droid: %w", err)
	}

	if _, err := io.WriteString(stdin, prompt); err != nil {
		stdin.Close()
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("write droid prompt: %w", err)
	}
	_ = stdin.Close()

	msgCh := make(chan Message, 256)
	resCh := make(chan Result, 1)
	trySend(msgCh, Message{Type: MessageStatus, Status: "running"})

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		var output strings.Builder
		var sessionID string
		var usage map[string]TokenUsage
		var streamErr error
		final := Result{
			Status: "completed",
		}

		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 1024*1024), 10*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			evt, err := parseDroidStreamEvent([]byte(line))
			if err != nil {
				streamErr = err
				continue
			}
			if evt.SessionID != "" {
				sessionID = evt.SessionID
			}
			switch evt.Type {
			case "system":
				trySend(msgCh, Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
			case "message":
				if evt.Role == "assistant" && evt.Text != "" {
					output.WriteString(evt.Text)
					trySend(msgCh, Message{Type: MessageText, Content: evt.Text, SessionID: sessionID})
				}
			case "tool_call":
				input := map[string]any{}
				if len(evt.Parameters) > 0 {
					_ = json.Unmarshal(evt.Parameters, &input)
				}
				toolName := evt.ToolName
				if toolName == "" {
					toolName = evt.ToolID
				}
				trySend(msgCh, Message{
					Type:      MessageToolUse,
					Tool:      toolName,
					CallID:    evt.ID,
					Input:     input,
					SessionID: sessionID,
				})
			case "tool_result":
				outputText := droidRawValueToString(evt.Value)
				trySend(msgCh, Message{
					Type:      MessageToolResult,
					Tool:      evt.ToolName,
					CallID:    evt.ID,
					Output:    outputText,
					SessionID: sessionID,
				})
			case "completion":
				if evt.FinalText != "" {
					output.Reset()
					output.WriteString(evt.FinalText)
				}
				if evt.DurationMs > 0 {
					final.DurationMs = evt.DurationMs
				}
				if u := droidUsageToMap(opts.Model, evt.Usage); len(u) > 0 {
					usage = u
				}
			case "error":
				final.Status = "failed"
				if evt.Text != "" {
					final.Error = evt.Text
				} else if len(evt.Value) > 0 {
					final.Error = droidRawValueToString(evt.Value)
				} else {
					final.Error = "droid emitted error event"
				}
				trySend(msgCh, Message{Type: MessageError, Content: final.Error, SessionID: sessionID})
			}
		}
		if err := scanner.Err(); err != nil {
			streamErr = fmt.Errorf("read droid stream: %w", err)
		}
		exitErr := cmd.Wait()
		duration := time.Since(startTime)

		if final.DurationMs == 0 {
			final.DurationMs = duration.Milliseconds()
		}
		final.SessionID = sessionID
		if output.Len() > 0 {
			final.Output = output.String()
		}
		if usage != nil {
			final.Usage = usage
		}

		switch {
		case runCtx.Err() == context.DeadlineExceeded:
			final.Status = "timeout"
			final.Error = fmt.Sprintf("droid timed out after %s", timeout)
		case runCtx.Err() == context.Canceled:
			final.Status = "aborted"
			final.Error = "execution cancelled"
		case exitErr != nil:
			final.Status = "failed"
			exitMessage := fmt.Sprintf("droid exited with error: %v", exitErr)
			if final.Error == "" {
				final.Error = exitMessage
			} else {
				final.Error = fmt.Sprintf("%s (%s)", final.Error, exitMessage)
			}
		}
		if streamErr != nil && final.Status == "completed" {
			final.Status = "failed"
			final.Error = streamErr.Error()
		}
		if final.Error != "" {
			final.Error = withAgentStderr(final.Error, "droid", stderrBuf.Tail())
		}

		b.cfg.Logger.Info("droid finished", "status", final.Status, "duration", duration.Round(time.Millisecond).String())
		resCh <- final
	}()

	return &Session{Messages: msgCh, Result: resCh}, nil
}

func buildDroidArgs(opts ExecOptions, logger *slog.Logger) []string {
	args := []string{"exec", "--output-format", "stream-json"}
	if opts.Model != "" && opts.Model != droidDefaultModelID {
		args = append(args, "--model", opts.Model)
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.SystemPrompt)
	}
	if opts.Cwd != "" {
		args = append(args, "--cwd", opts.Cwd)
	}
	if opts.ResumeSessionID != "" {
		args = append(args, "--session-id", opts.ResumeSessionID)
	}

	filteredExtra := filterCustomArgs(opts.ExtraArgs, droidBlockedArgs, logger)
	filteredCustom := filterCustomArgs(opts.CustomArgs, droidBlockedArgs, logger)
	if droidModelDisablesReasoning(opts.Model) {
		filteredExtra = filterDroidReasoningArgs(filteredExtra, logger)
		filteredCustom = filterDroidReasoningArgs(filteredCustom, logger)
	}
	if !droidArgsSetAutonomy(filteredExtra) && !droidArgsSetAutonomy(filteredCustom) {
		args = append(args, "--auto", "high")
	}
	if !droidModelDisablesReasoning(opts.Model) {
		defaultReasoning := droidDefaultReasoningEffort(opts.Model)
		if defaultReasoning != "" &&
			!droidArgsSetReasoningEffort(filteredExtra) &&
			!droidArgsSetReasoningEffort(filteredCustom) {
			args = append(args, "--reasoning-effort", defaultReasoning)
		}
	}
	args = append(args, filteredExtra...)
	args = append(args, filteredCustom...)
	return args
}

func droidArgsSetAutonomy(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--auto", "--skip-permissions-unsafe":
			return true
		}
		if strings.HasPrefix(arg, "--auto=") {
			return true
		}
	}
	return false
}

func droidArgsSetReasoningEffort(args []string) bool {
	for _, arg := range args {
		switch arg {
		case "--reasoning-effort", "-r":
			return true
		}
		if strings.HasPrefix(arg, "--reasoning-effort=") {
			return true
		}
	}
	return false
}

func filterDroidReasoningArgs(args []string, logger *slog.Logger) []string {
	if len(args) == 0 {
		return args
	}
	filtered := make([]string, 0, len(args))
	skip := false
	for _, arg := range args {
		if skip {
			skip = false
			continue
		}
		switch {
		case arg == "--reasoning-effort" || arg == "-r":
			if logger != nil {
				logger.Warn("droid: removed unsupported reasoning flag for model", "flag", arg)
			}
			skip = true
			continue
		case strings.HasPrefix(arg, "--reasoning-effort="):
			if logger != nil {
				logger.Warn("droid: removed unsupported reasoning flag for model", "flag", "--reasoning-effort")
			}
			continue
		}
		filtered = append(filtered, arg)
	}
	return filtered
}

func droidDefaultReasoningEffort(model string) string {
	if spec := droidReasoningSpecForDroidModel(model); spec != nil {
		return spec.DefaultLevel
	}
	return ""
}

func droidReasoningSpecForDroidModel(model string) *ReasoningSpec {
	normalized := strings.ToLower(strings.TrimSpace(model))
	if normalized == "" || normalized == droidDefaultModelID {
		return nil
	}
	if strings.HasPrefix(normalized, "custom:") {
		if custom, ok := droidCustomModelSettings(model); ok {
			return droidReasoningSpecForCustomModel(custom)
		}
		return droidReasoningSpecForModel(model, "generic-chat-completion-api", "")
	}
	return droidReasoningSpecForModel(model, inferDroidProvider(model, ""), "")
}

func droidCustomModelSettings(modelID string) (droidCustomModel, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return droidCustomModel{}, false
	}
	raw, err := os.ReadFile(filepath.Join(home, ".factory", "settings.json"))
	if err != nil {
		return droidCustomModel{}, false
	}
	var settings droidSettingsFile
	if err := json.Unmarshal(raw, &settings); err != nil {
		return droidCustomModel{}, false
	}
	for _, custom := range settings.CustomModels {
		if strings.EqualFold(strings.TrimSpace(custom.ID), strings.TrimSpace(modelID)) {
			return custom, true
		}
	}
	return droidCustomModel{}, false
}

func parseDroidStreamEvent(raw []byte) (droidStreamEvent, error) {
	var evt droidStreamEvent
	if err := json.Unmarshal(bytes.TrimSpace(raw), &evt); err != nil {
		return droidStreamEvent{}, fmt.Errorf("parse droid stream event: %w", err)
	}
	if evt.Type == "" {
		return droidStreamEvent{}, fmt.Errorf("droid stream event missing type")
	}
	return evt, nil
}

func droidRawValueToString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		if data, err := json.MarshalIndent(v, "", "  "); err == nil {
			return string(data)
		}
	}
	return string(raw)
}

func droidUsageToMap(model string, usage droidUsageBlock) map[string]TokenUsage {
	if usage.InputTokens == 0 && usage.OutputTokens == 0 && usage.CacheReadInputTokens == 0 && usage.CacheCreationTokens == 0 {
		return nil
	}
	if model == "" {
		model = droidDefaultModelID
	}
	return map[string]TokenUsage{
		model: {
			InputTokens:      usage.InputTokens,
			OutputTokens:     usage.OutputTokens,
			CacheReadTokens:  usage.CacheReadInputTokens,
			CacheWriteTokens: usage.CacheCreationTokens,
		},
	}
}
