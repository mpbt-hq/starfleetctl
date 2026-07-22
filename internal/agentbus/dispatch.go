// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/metux/starfleetctl/internal/config"
)

// dispatchRequest is the JSON the opencode plugin sends via stdin.
type dispatchRequest struct {
	Cmd string `json:"cmd"`

	// health update fields
	State           string `json:"state,omitempty"`
	PluginLastRun   string `json:"plugin_last_run,omitempty"`
	ModelLastAction string `json:"model_last_action,omitempty"`
	PID             int    `json:"pid,omitempty"`
	Model           string `json:"model,omitempty"`
	Server          string `json:"server,omitempty"`
	ErrorTag        string `json:"error_tag,omitempty"`
	Delete          bool   `json:"delete,omitempty"`
	Reset           bool   `json:"reset,omitempty"`
	Touch           bool   `json:"touch,omitempty"`

	// inbox / ack / tell / seen
	ID      string `json:"id,omitempty"`
	Note    string `json:"note,omitempty"`
	Target  string `json:"target,omitempty"`
	Text    string `json:"text,omitempty"`
	ReplyTo string `json:"reply_to,omitempty"`
	Type    string `json:"type,omitempty"` // "ship", "user", "control"

	// error handle
	Detail string `json:"detail,omitempty"`
	Ship   string `json:"ship,omitempty"`

	// error-handle (policy decision)
	Source       string `json:"source,omitempty"`
	CurrentModel string `json:"current_model,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	HasFallback  bool   `json:"has_fallback,omitempty"`
}

// dispatchResponse is the JSON returned to the plugin.
type dispatchResponse struct {
	OK    bool     `json:"ok"`
	Error string   `json:"error,omitempty"`
	Messages []inboxMsg `json:"messages,omitempty"`
	Seen     []string   `json:"seen,omitempty"`
	Tag      string   `json:"tag,omitempty"`

	// config response
	HeartbeatMS       int    `json:"heartbeat_ms,omitempty"`
	PollMS            int    `json:"poll_ms,omitempty"`
	FallbackModel     string `json:"fallback_model,omitempty"`
	RetryPollMS       int    `json:"retry_poll_ms,omitempty"`
	RetryCooldownMS   int    `json:"retry_cooldown_ms,omitempty"`
	LogPollMS         int    `json:"log_poll_ms,omitempty"`
	LogCooldownMS     int    `json:"log_cooldown_ms,omitempty"`

	// error-handle policy response
	Action       string `json:"action,omitempty"`
	TargetModel  string `json:"target_model,omitempty"`
	Reason       string `json:"reason,omitempty"`
}

type inboxMsg struct {
	ID   string `json:"id"`
	From string `json:"from"`
	Text string `json:"text"`
	Type string `json:"type,omitempty"`
}

// DoDispatch implements `agent-bus dispatch --stdin` — the single JSON-RPC
// entry point for the opencode plugin. Reads a JSON request from stdin,
// dispatches to the appropriate Go function, and prints a JSON response.
func (b *Bus) DoDispatch(args []string) error {
	useStdin := false
	for _, a := range args {
		if a == "--stdin" {
			useStdin = true
		}
	}
	if !useStdin {
		return usageErr("agent-bus dispatch: requires --stdin")
	}

	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return fmt.Errorf("dispatch: reading stdin: %w", err)
	}

	var req dispatchRequest
	if err := json.Unmarshal(data, &req); err != nil {
		return printDispatch(dispatchResponse{OK: false, Error: "invalid JSON: " + err.Error()})
	}

	resp := b.dispatch(req)
	return printDispatch(resp)
}

func (b *Bus) dispatch(req dispatchRequest) dispatchResponse {
	switch req.Cmd {
	case "config":
		return b.dispatchConfig()
	case "init":
		return b.dispatchInit(req)
	case "health":
		return b.dispatchHealth(req)
	case "inbox":
		return b.dispatchInbox()
	case "ack":
		return b.dispatchAck(req)
	case "tell":
		return b.dispatchTell(req)
	case "command":
		return b.dispatchCommand(req)
	case "touch":
		return b.dispatchTouch()
	case "status":
		return b.dispatchStatus(req)
	case "prune":
		return b.dispatchPrune()
	case "error":
		return b.dispatchError(req)
	case "error-handle":
		return b.dispatchErrorHandle(req)
	case "clear":
		return b.dispatchClear()
	case "tick":
		return b.dispatchTick(req)
	case "exit":
		return b.dispatchExit(req)
	default:
		return dispatchResponse{OK: false, Error: "unknown cmd: " + req.Cmd}
	}
}

func (b *Bus) dispatchConfig() dispatchResponse {
	cfg, err := config.Load(b.Root)
	if err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{
		OK:              true,
		HeartbeatMS:     cfg.AgentBus.HeartbeatMS,
		PollMS:          cfg.AgentBus.PollMS,
		FallbackModel:   cfg.AgentBus.FallbackModel,
		RetryPollMS:     cfg.AgentBus.RetryPollMS,
		RetryCooldownMS: cfg.AgentBus.RetryCooldownMS,
		LogPollMS:       cfg.AgentBus.LogPollMS,
		LogCooldownMS:   cfg.AgentBus.LogCooldownMS,
	}
}

func (b *Bus) dispatchInit(req dispatchRequest) dispatchResponse {
	note := req.Note
	if note == "" {
		note = "opencode ship"
	}
	messages, err := b.DoInit(note)
	if err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true, Messages: orEmptyInbox(messages)}
}

func (b *Bus) dispatchHealth(req dispatchRequest) dispatchResponse {
	var args []string
	if req.Delete {
		args = append(args, "--delete")
	} else {
		if req.Reset {
			args = append(args, "--reset")
		}
		if req.Touch {
			args = append(args, "--touch")
		}
		if req.State != "" {
			args = append(args, "--state", req.State)
		}
		if req.PluginLastRun != "" {
			args = append(args, "--plugin-ts", req.PluginLastRun)
		}
		if req.ModelLastAction != "" {
			args = append(args, "--model-ts", req.ModelLastAction)
		}
		if req.PID > 0 {
			args = append(args, "--pid", fmt.Sprintf("%d", req.PID))
		}
		if req.Model != "" {
			args = append(args, "--model", req.Model)
		}
		if req.Server != "" {
			args = append(args, "--server", req.Server)
		}
		if req.ErrorTag != "" {
			args = append(args, "--error-tag", req.ErrorTag)
		}
	}
	if err := b.DoHealthUpdate(args); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchInbox() dispatchResponse {
	msgs := b.allMsgRecords()
	agent := b.ShipID
	var out []inboxMsg
	for _, m := range msgs {
		if m.Target != "all" && m.Target != agent {
			continue
		}
		if b.acked(m.ID, agent) {
			continue
		}
		out = append(out, inboxMsg{ID: m.ID, From: m.From, Text: m.Text, Type: m.Type})
	}
	return dispatchResponse{OK: true, Messages: orEmptyInbox(out)}
}

func (b *Bus) dispatchAck(req dispatchRequest) dispatchResponse {
	if req.ID == "" {
		return dispatchResponse{OK: false, Error: "ack: need id"}
	}
	note := req.Note
	if note == "" {
		note = "acked"
	}
	if err := b.DoAck(req.ID, note); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchTell(req dispatchRequest) dispatchResponse {
	if req.Target == "" || req.Text == "" {
		return dispatchResponse{OK: false, Error: "tell: need target and text"}
	}
	msgType := req.Type
	if msgType == "" {
		msgType = "ship"
	}
	if err := b.DoPost(req.Target, []string{req.Text}, false, "", req.ReplyTo, msgType); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchCommand(req dispatchRequest) dispatchResponse {
	if req.Target == "" || req.Text == "" {
		return dispatchResponse{OK: false, Error: "command: need target and text (verb)"}
	}
	if _, err := b.Command(req.Target, req.Text, ""); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchTouch() dispatchResponse {
	if err := b.DoTouch(); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchStatus(req dispatchRequest) dispatchResponse {
	state := req.State
	if state == "" {
		state = "idle"
	}
	note := req.Note
	if note == "" {
		note = "opencode ship"
	}
	if err := b.DoStatus(state, note, StatusPatch{}); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchPrune() dispatchResponse {
	if err := b.DoPrune(); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchError(req dispatchRequest) dispatchResponse {
	if req.Detail == "" {
		return dispatchResponse{OK: false, Error: "error: need detail"}
	}
	if IsUserAbort(req.Detail) {
		b.logEvent("plugin", fmt.Sprintf("error (user abort, suppressed): %s", req.Detail))
		return dispatchResponse{OK: true}
	}
	tag := ClassifyModelError(req.Detail)
	_ = b.DoHealthUpdate([]string{"--state", "blocked", "--error-tag", tag})
	label := ""
	if tag != "" {
		label = " [" + tag + "]"
	}
	b.logEvent("plugin", fmt.Sprintf("error%s: %s", label, req.Detail))
	shipID := b.ShipID
	if req.Ship != "" {
		shipID = req.Ship
	}
	_ = b.DoPost("Enterprise", []string{
		fmt.Sprintf("⚠️ %s session.error%s: %s", shipID, label, req.Detail),
	}, false, "", "", "control")
	return dispatchResponse{OK: true, Tag: tag}
}

// dispatchErrorHandle implements `cmd: "error-handle"` — the policy engine.
// Plugin sends error detail + context, starfleetctl returns the action to take.
// This moves all policy logic into the Go binary so it can be updated
// (binary replace or config change) without restarting the opencode client.
func (b *Bus) dispatchErrorHandle(req dispatchRequest) dispatchResponse {
	if req.Detail == "" {
		return dispatchResponse{OK: false, Error: "error-handle: need detail"}
	}

	detail := req.Detail
	shipID := b.ShipID
	if req.Ship != "" {
		shipID = req.Ship
	}

	// 1. User abort → ignore.
	if IsUserAbort(detail) {
		b.logEvent("plugin", fmt.Sprintf("error-handle (user abort, suppressed): %s", detail))
		return dispatchResponse{OK: true, Action: "ignore", Reason: "user abort"}
	}

	// 2. Classify.
	tag := ClassifyModelError(detail)

	// 3. Health update + notification (same as dispatchError).
	_ = b.DoHealthUpdate([]string{"--state", "blocked", "--error-tag", tag})
	label := ""
	if tag != "" {
		label = " [" + tag + "]"
	}
	b.logEvent("plugin", fmt.Sprintf("error-handle%s: %s (source=%s)", label, detail, req.Source))
	_ = b.DoPost("Enterprise", []string{
		fmt.Sprintf("⚠️ %s session.error%s: %s", shipID, label, detail),
	}, false, "", "", "control")

	// 4. Policy decision.
	action, reason := decideAction(tag, req.HasFallback, req.Source)
	target := ""
	if action == "switch-model" {
		cfg, err := config.Load(b.Root)
		if err == nil && cfg.AgentBus.FallbackModel != "" {
			target = cfg.AgentBus.FallbackModel
		} else {
			// No fallback configured → can't switch, fall back to retry.
			action = "retry"
			reason = "no fallback model configured"
		}
	}

	return dispatchResponse{
		OK:          true,
		Tag:         tag,
		Action:      action,
		TargetModel: target,
		Reason:      reason,
	}
}

// decideAction implements the error-handling policy.
//
//Transient errors → retry (just re-prompt, model is fine).
// Hard errors → switch-model (need different provider/model).
// Unknown → ignore (safe default).
func decideAction(tag string, hasFallback bool, source string) (action, reason string) {
	switch tag {
	case "nim-overload":
		return "retry", "transient provider overload, retry alone should suffice"
	case "streaming-response-failed":
		return "retry", "transient stream disconnect, retry alone should suffice"
	case "resource-exhausted":
		return "retry", "transient resource exhaustion, retry alone should suffice"
	case "zen-ratelimit":
		if hasFallback {
			return "switch-model", "quota/rate limit exhausted, switch to fallback model"
		}
		return "retry", "quota/rate limit but no fallback configured, retry anyway"
	default:
		// Unknown tag from log-monitor or unrecognized error.
		if source == "log-monitor" {
			return "retry", "unrecognized log error, retry as precaution"
		}
		return "ignore", "unclassified error, no action taken"
	}
}

func (b *Bus) dispatchClear() dispatchResponse {
	if err := b.DoClear(); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchTick(req dispatchRequest) dispatchResponse {
	note := req.Note
	if note == "" {
		note = "tick"
	}
	b.logEvent("tick", note)
	return dispatchResponse{OK: true}
}

func (b *Bus) dispatchExit(req dispatchRequest) dispatchResponse {
	note := req.Note
	if note == "" {
		note = "exit"
	}
	if err := b.DoExit(note); err != nil {
		return dispatchResponse{OK: false, Error: err.Error()}
	}
	return dispatchResponse{OK: true}
}

func printDispatch(resp dispatchResponse) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}

func orEmptyInbox(s []inboxMsg) []inboxMsg {
	if s == nil {
		return []inboxMsg{}
	}
	return s
}
