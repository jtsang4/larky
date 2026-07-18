package hook

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/request"
)

type SessionStartHandler struct {
	Requests *request.Service
}

func (h SessionStartHandler) Handle(platform contract.Platform, reader io.Reader) (contract.SessionStartDecision, error) {
	if platform != contract.PlatformCodex {
		return contract.SessionStartDecision{}, fmt.Errorf("SessionStart recovery only supports Codex")
	}
	var input contract.HookInput
	if err := json.NewDecoder(reader).Decode(&input); err != nil {
		return contract.SessionStartDecision{}, fmt.Errorf("decode SessionStart hook input: %w", err)
	}
	if input.SessionID == "" {
		return contract.SessionStartDecision{SystemMessage: "larky ignored SessionStart hook without session_id"}, nil
	}
	reply, err := h.Requests.TakeNextReplyForHandoff(platform, input.SessionID, contract.HandoffCodexSessionStart)
	if err != nil {
		return contract.SessionStartDecision{SystemMessage: "larky could not recover a queued reply: " + err.Error()}, nil
	}
	if reply == nil {
		return contract.SessionStartDecision{}, nil
	}
	return contract.SessionStartDecision{HookSpecificOutput: &contract.HookSpecificOutput{
		HookEventName: "SessionStart", AdditionalContext: sessionStartWakePrompt(*reply),
	}}, nil
}
