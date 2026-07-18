package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/platform/macos"
	"github.com/jtsang4/larky/internal/request"
)

type StopHandler struct {
	Config        config.Config
	Detector      macos.Detector
	Requests      *request.Service
	Executable    string
	EnsureSidecar func() error
	PollInterval  time.Duration
	AwayInterval  time.Duration
}

func (h StopHandler) Handle(ctx context.Context, platform contract.Platform, reader io.Reader) (contract.HookDecision, error) {
	if !platform.Valid() {
		return contract.HookDecision{}, fmt.Errorf("unsupported platform %q", platform)
	}
	decoder := json.NewDecoder(reader)
	var input contract.HookInput
	if err := decoder.Decode(&input); err != nil {
		return contract.HookDecision{}, fmt.Errorf("decode Stop hook input: %w", err)
	}
	if input.SessionID == "" {
		return contract.HookDecision{SystemMessage: "larky ignored Stop hook without session_id"}, nil
	}
	away, err := h.Detector.Detect()
	if err != nil {
		return contract.HookDecision{SystemMessage: "larky could not determine Mac away state; notification was skipped: " + err.Error()}, nil
	}
	if !away.Away {
		if input.StopHookActive && platform == contract.PlatformCodex {
			h.cancelLatest(input, platform)
		}
		return contract.HookDecision{}, nil
	}
	if input.StopHookActive {
		if platform == contract.PlatformClaude {
			return contract.HookDecision{}, nil
		}
		latest, err := h.Requests.LatestForSession(platform, input.SessionID, input.TurnID)
		if err != nil {
			return contract.HookDecision{SystemMessage: "larky could not inspect the pending Codex request: " + err.Error()}, nil
		}
		if latest == nil {
			latest, err = h.Requests.AwaitingForSession(platform, input.SessionID)
			if err != nil {
				return contract.HookDecision{SystemMessage: "larky refused an ambiguous Codex request mapping: " + err.Error()}, nil
			}
		}
		if latest == nil {
			latest, err = h.Requests.LatestForSession(platform, input.SessionID, "")
			if err != nil {
				return contract.HookDecision{SystemMessage: "larky could not inspect the Codex session history: " + err.Error()}, nil
			}
		}
		if latest == nil {
			return contract.HookDecision{}, nil
		}
		switch latest.State {
		case contract.StatePendingDelivery:
			return h.blockForDelivery(latest)
		case contract.StatePendingReply, contract.StateClaimed:
			return h.waitForCodexReply(ctx, input, latest)
		case contract.StateResumed:
			// The previous Lark reply already resumed this exact hook chain. A
			// new final result may now need its own notification.
			return h.createAndBlock(ctx, platform, input, away, latest.ID)
		default:
			return contract.HookDecision{}, nil
		}
	}
	return h.createAndBlock(ctx, platform, input, away, "")
}

func (h StopHandler) createAndBlock(ctx context.Context, platform contract.Platform, input contract.HookInput, away macos.State, previousRequestID string) (contract.HookDecision, error) {
	message := decodeLastMessage(input.LastAssistantMessage)
	req, created, err := h.Requests.Create(request.CreateInput{
		Platform: platform, SessionID: input.SessionID, TurnID: input.TurnID,
		PreviousRequestID: previousRequestID,
		CWD:               input.CWD, Message: message, AwayDetected: away.Away,
		DisplayAsleep: away.DisplayAsleep, ScreenLocked: away.ScreenLocked, AwayMethod: away.Method,
	})
	if err != nil {
		return contract.HookDecision{SystemMessage: "larky skipped notification: " + err.Error()}, nil
	}
	if !created {
		switch req.State {
		case contract.StatePendingDelivery:
			return h.blockForDelivery(req)
		case contract.StatePendingReply, contract.StateClaimed:
			if platform == contract.PlatformCodex {
				return h.waitForCodexReply(ctx, input, req)
			}
		}
		return contract.HookDecision{}, nil
	}
	return h.blockForDelivery(req)
}

func (h StopHandler) blockForDelivery(req *contract.InteractionRequest) (contract.HookDecision, error) {
	if h.EnsureSidecar != nil {
		if err := h.EnsureSidecar(); err != nil {
			return contract.HookDecision{SystemMessage: "larky could not start its event sidecar; notification was skipped: " + err.Error()}, nil
		}
	}
	return contract.HookDecision{Decision: "block", Reason: continuationPrompt(req)}, nil
}

func (h StopHandler) waitForCodexReply(ctx context.Context, input contract.HookInput, req *contract.InteractionRequest) (contract.HookDecision, error) {
	if h.EnsureSidecar != nil {
		if err := h.EnsureSidecar(); err != nil {
			return contract.HookDecision{SystemMessage: "larky could not keep its event sidecar ready; the Codex task stopped waiting: " + err.Error()}, nil
		}
	}
	pollInterval := h.PollInterval
	if pollInterval <= 0 {
		pollInterval = 250 * time.Millisecond
	}
	awayInterval := h.AwayInterval
	if awayInterval <= 0 {
		awayInterval = time.Second
	}
	poll := time.NewTicker(pollInterval)
	away := time.NewTicker(awayInterval)
	defer poll.Stop()
	defer away.Stop()

	for {
		decision, done, err := h.pollCodexReply(input, req)
		if err != nil {
			return contract.HookDecision{SystemMessage: "larky could not read the routed Codex reply: " + err.Error()}, nil
		}
		if done {
			return decision, nil
		}
		select {
		case <-ctx.Done():
			return contract.HookDecision{}, nil
		case <-poll.C:
		case <-away.C:
			state, err := h.Detector.Detect()
			if err == nil && !state.Away {
				_ = h.Requests.CancelAwaitingReply(req.ID, contract.PlatformCodex, input.SessionID)
				return contract.HookDecision{}, nil
			}
		}
	}
}

func (h StopHandler) pollCodexReply(input contract.HookInput, req *contract.InteractionRequest) (contract.HookDecision, bool, error) {
	reply, err := h.Requests.TakeReplyForHandoff(req.ID, contract.PlatformCodex, input.SessionID, contract.HandoffCodexStopHook)
	if err != nil {
		return contract.HookDecision{}, true, err
	}
	if reply != nil {
		return contract.HookDecision{Decision: "block", Reason: routedReplyPrompt(*reply)}, true, nil
	}
	current, err := h.Requests.GetForSession(req.ID, contract.PlatformCodex, input.SessionID)
	if err != nil {
		return contract.HookDecision{}, true, err
	}
	if current == nil {
		return contract.HookDecision{}, true, nil
	}
	if !current.ExpiresAt.After(time.Now().UTC()) {
		if err := h.Requests.ExpireAwaitingReply(req.ID, contract.PlatformCodex, input.SessionID); err != nil {
			return contract.HookDecision{}, true, err
		}
		return contract.HookDecision{}, true, nil
	}
	switch current.State {
	case contract.StatePendingReply, contract.StateClaimed:
		return contract.HookDecision{}, false, nil
	default:
		return contract.HookDecision{}, true, nil
	}
}

func (h StopHandler) cancelLatest(input contract.HookInput, platform contract.Platform) {
	req, err := h.Requests.LatestForSession(platform, input.SessionID, input.TurnID)
	if err == nil && req == nil {
		req, err = h.Requests.AwaitingForSession(platform, input.SessionID)
	}
	if err == nil && req != nil {
		_ = h.Requests.CancelAwaitingReply(req.ID, platform, input.SessionID)
	}
}

func continuationPrompt(req *contract.InteractionRequest) string {
	return fmt.Sprintf("[Larky · 飞书传输 · %s]\n运行 `larky delivery show --request-id %s`，并按已安装的 larky skill 完成本轮对话回传。", req.ID, req.ID)
}

func sessionStartWakePrompt(reply contract.RoutedReply) string {
	return routedReplyPrompt(reply)
}

func routedReplyPrompt(reply contract.RoutedReply) string {
	header := fmt.Sprintf("[Larky · 飞书回复 · %s]", reply.RequestID)
	if reply.Text != "" {
		return header + "\n" + truncatePromptValue(reply.Text, 6000)
	}
	action := map[string]string{
		"continue": "继续当前任务", "retry": "重试当前任务", "close": "关闭当前请求", "cancel": "取消当前请求",
	}[reply.Action]
	if action == "" {
		action = reply.Action
	}
	if reply.ChoiceID != "" {
		action += "：" + truncatePromptValue(reply.ChoiceID, 200)
	}
	return header + "\n用户选择了：" + action + "。"
}

func truncatePromptValue(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func decodeLastMessage(raw json.RawMessage) string {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var message string
	if json.Unmarshal(raw, &message) == nil {
		return message
	}
	return strings.TrimSpace(string(raw))
}
