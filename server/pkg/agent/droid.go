package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// droidBackend implements Backend by spawning `droid exec --output-format json`
// and parsing the one-shot result envelope. Droid does not need a long-lived
// JSON-RPC transport for the Multica daemon path: the CLI reads the prompt from
// stdin and returns a single JSON result with session and usage metadata.
type droidBackend struct {
	cfg Config
}

type droidResultEnvelope struct {
	Type     string          `json:"type"`
	Subtype  string          `json:"subtype"`
	IsError  bool            `json:"is_error"`
	Duration int64           `json:"duration_ms"`
	Result   string          `json:"result"`
	Session  string          `json:"session_id"`
	Usage    droidUsageBlock `json:"usage"`
}

type droidUsageBlock struct {
	InputTokens          int64 `json:"input_tokens"`
	OutputTokens         int64 `json:"output_tokens"`
	CacheReadInputTokens int64 `json:"cache_read_input_tokens"`
	CacheCreationTokens  int64 `json:"cache_creation_input_tokens"`
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

	msgCh := make(chan Message, 4)
	resCh := make(chan Result, 1)
	trySend(msgCh, Message{Type: MessageStatus, Status: "running"})

	go func() {
		defer cancel()
		defer close(msgCh)
		defer close(resCh)

		startTime := time.Now()
		out, readErr := io.ReadAll(stdout)
		exitErr := cmd.Wait()
		duration := time.Since(startTime)

		final := Result{
			Status:     "completed",
			DurationMs: duration.Milliseconds(),
		}

		switch {
		case runCtx.Err() == context.DeadlineExceeded:
			final.Status = "timeout"
			final.Error = fmt.Sprintf("droid timed out after %s", timeout)
		case runCtx.Err() == context.Canceled:
			final.Status = "aborted"
			final.Error = "execution cancelled"
		case readErr != nil:
			final.Status = "failed"
			final.Error = fmt.Sprintf("read droid stdout: %v", readErr)
		case exitErr != nil:
			final.Status = "failed"
			final.Error = fmt.Sprintf("droid exited with error: %v", exitErr)
		}

		env, parseErr := parseDroidResult(out)
		if parseErr != nil && final.Status == "completed" {
			final.Status = "failed"
			final.Error = parseErr.Error()
		}
		if parseErr == nil {
			final.Output = env.Result
			final.SessionID = env.Session
			final.DurationMs = chooseDroidDuration(env.Duration, final.DurationMs)
			if env.IsError {
				final.Status = "failed"
				if strings.TrimSpace(env.Result) != "" {
					final.Error = env.Result
				} else {
					final.Error = "droid returned is_error=true"
				}
			}
			if usage := droidUsageToMap(opts.Model, env.Usage); len(usage) > 0 {
				final.Usage = usage
			}
			if env.Result != "" {
				trySend(msgCh, Message{Type: MessageText, Content: env.Result, SessionID: env.Session})
			}
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
	args := []string{"exec", "--output-format", "json"}
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
	if !droidArgsSetAutonomy(filteredExtra) && !droidArgsSetAutonomy(filteredCustom) {
		args = append(args, "--auto", "high")
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

func parseDroidResult(raw []byte) (droidResultEnvelope, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return droidResultEnvelope{}, fmt.Errorf("droid produced empty stdout")
	}
	var env droidResultEnvelope
	if err := json.Unmarshal(trimmed, &env); err != nil {
		tail := string(trimmed)
		if len(tail) > 2048 {
			tail = tail[len(tail)-2048:]
		}
		return droidResultEnvelope{}, fmt.Errorf("parse droid JSON result: %w; stdout tail: %s", err, tail)
	}
	if env.Type != "" && env.Type != "result" {
		return droidResultEnvelope{}, fmt.Errorf("unexpected droid result type %q", env.Type)
	}
	return env, nil
}

func chooseDroidDuration(cliDuration, measured int64) int64 {
	if cliDuration > 0 {
		return cliDuration
	}
	return measured
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
