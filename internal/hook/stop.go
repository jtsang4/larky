package hook

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

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
}

func (h StopHandler) Handle(platform contract.Platform, reader io.Reader) (contract.HookDecision, error) {
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
	if input.StopHookActive {
		return contract.HookDecision{}, nil
	}
	away, err := h.Detector.Detect()
	if err != nil {
		return contract.HookDecision{SystemMessage: "larky could not determine Mac away state; notification was skipped: " + err.Error()}, nil
	}
	if !away.Away {
		return contract.HookDecision{}, nil
	}
	message := decodeLastMessage(input.LastAssistantMessage)
	req, created, err := h.Requests.Create(request.CreateInput{
		Platform: platform, SessionID: input.SessionID, TurnID: input.TurnID,
		CWD: input.CWD, Message: message, AwayDetected: away.Away,
		DisplayAsleep: away.DisplayAsleep, ScreenLocked: away.ScreenLocked, AwayMethod: away.Method,
	})
	if err != nil {
		return contract.HookDecision{SystemMessage: "larky skipped notification: " + err.Error()}, nil
	}
	if !created && req.State != contract.StatePendingDelivery {
		return contract.HookDecision{}, nil
	}
	if h.EnsureSidecar != nil {
		if err := h.EnsureSidecar(); err != nil {
			return contract.HookDecision{SystemMessage: "larky could not start its event sidecar; notification was skipped: " + err.Error()}, nil
		}
	}
	return contract.HookDecision{Decision: "block", Reason: h.continuationPrompt(req)}, nil
}

func (h StopHandler) continuationPrompt(req *contract.InteractionRequest) string {
	binary := h.Executable
	if binary == "" {
		binary = "larky"
	}
	deliveryChatID := req.ChatID
	target := "chat " + req.ChatID
	deliveryIdentity := strings.ToLower(strings.TrimSpace(h.Config.EventIdentity))
	if deliveryIdentity == "" {
		deliveryIdentity = "bot"
	}
	if deliveryChatID == "" {
		deliveryChatID = "<CHAT_ID_FROM_RESULT>"
		target = "a direct message to user " + req.TargetUserID
	}
	deliveryCommand := fmt.Sprintf("%s delivery record --request-id %s --message-id <MESSAGE_ID> --chat-id %s --identity <IDENTITY_FROM_RESULT>", shellQuote(binary), shellQuote(req.ID), shellQuote(deliveryChatID))
	failureCommand := fmt.Sprintf("%s delivery fail --request-id %s", shellQuote(binary), shellQuote(req.ID))
	receiptInstruction := "Replace <MESSAGE_ID> and <IDENTITY_FROM_RESULT> with the message_id and actual identity returned by lark-im."
	if req.ChatID == "" {
		receiptInstruction = "Replace <MESSAGE_ID>, <CHAT_ID_FROM_RESULT>, and <IDENTITY_FROM_RESULT> with the message_id, chat_id, and actual identity returned by lark-im."
	}
	return fmt.Sprintf(`Larky detected that the Mac display is asleep or locked. Before stopping, notify the user now.

Use the globally installed lark-im skill to send a Card 2.0 interactive message to %s. Do not reimplement the Lark API and do not ask the local terminal user for confirmation.

Identity contract:
- Send the message as %s, the same identity used by Larky's event consumers. In lark-im/lark-cli terms, select the %s identity explicitly (for example, --as %s when the skill exposes that flag).
- Verify that the lark-im result reports identity %s. If it reports another identity, do not record that delivery: resend with %s. The buttons on a card sent by the wrong identity cannot reach Larky's consumer.

Notification contract:
- request_id: %s; status: %s; project: %s; platform: %s; expires_at: %s.
- Summarize the just-finished task and its real verification evidence. Redact secrets, full session IDs, and sensitive paths.
- Header: status + project + platform. Body: concise result, evidence or blocker, and exactly one next question when input is needed. Footer: request code %s and expiry, plus “you can also reply to this card”.
- This first delivery must be Card 2.0. If card sending fails twice, send a plain-text fallback containing request code %s and mark the delivery as degraded.
- For standalone buttons, use callback value {"v":1,"request_id":"%s","action":"<action>"}. For done, include continue and close. For waiting_user, blocked, or failed, include an input form named context with a submit button named submit_context, plus retry/continue when appropriate and cancel. If the answer has 2–3 clear choices, buttons may use action answer and choice_id.
- Never expose or approve dangerous tool permissions through a card action.

After the skill returns the outbound delivery, %s Then run exactly this command with the real values:
%s

For the plain-text fallback, append --degraded to that command. If both card and fallback fail, run:
%s

Do not stop until one of those receipt commands succeeds.`,
		target, deliveryIdentity, deliveryIdentity, deliveryIdentity, deliveryIdentity, deliveryIdentity,
		req.ID, req.Status, req.Project, req.Platform, req.ExpiresAt.Format("2006-01-02T15:04:05Z07:00"), req.ShortCode, req.ShortCode, req.ID, receiptInstruction, deliveryCommand, failureCommand)
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

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
