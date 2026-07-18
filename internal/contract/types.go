package contract

import (
	"encoding/json"
	"time"
)

type Platform string

const (
	PlatformClaude Platform = "claude"
	PlatformCodex  Platform = "codex"
)

func (p Platform) Valid() bool {
	return p == PlatformClaude || p == PlatformCodex
}

type RequestStatus string

const (
	StatusDone        RequestStatus = "done"
	StatusWaitingUser RequestStatus = "waiting_user"
	StatusBlocked     RequestStatus = "blocked"
	StatusFailed      RequestStatus = "failed"
)

type RequestState string

const (
	StatePendingDelivery RequestState = "pending_delivery"
	StatePendingReply    RequestState = "pending_reply"
	StateClaimed         RequestState = "claimed"
	StateResumed         RequestState = "resumed"
	StateClosed          RequestState = "closed"
	StateCancelled       RequestState = "cancelled"
	StateExpired         RequestState = "expired"
	StateDeliveryFailed  RequestState = "delivery_failed"
)

func (s RequestState) Active() bool {
	return s == StatePendingDelivery || s == StatePendingReply
}

type HandoffMode string

const (
	HandoffClaudeMonitor     HandoffMode = "claude_monitor"
	HandoffCodexStopHook     HandoffMode = "codex_stop_hook"
	HandoffCodexSessionStart HandoffMode = "codex_session_start"
)

type InteractionRequest struct {
	ID                  string        `json:"id"`
	ShortCode           string        `json:"short_code"`
	IdempotencyKey      string        `json:"idempotency_key"`
	Platform            Platform      `json:"platform"`
	SessionID           string        `json:"session_id"`
	TurnID              string        `json:"turn_id,omitempty"`
	PreviousRequestID   string        `json:"previous_request_id,omitempty"`
	CWD                 string        `json:"cwd,omitempty"`
	Project             string        `json:"project,omitempty"`
	Summary             string        `json:"summary"`
	TurnOutput          string        `json:"turn_output,omitempty"`
	TurnOutputTruncated bool          `json:"turn_output_truncated,omitempty"`
	Status              RequestStatus `json:"status"`
	State               RequestState  `json:"state"`
	ChatID              string        `json:"chat_id,omitempty"`
	TargetUserID        string        `json:"target_user_id,omitempty"`
	AllowedSenderIDs    []string      `json:"allowed_sender_ids,omitempty"`
	MessageID           string        `json:"message_id,omitempty"`
	MessageIDs          []string      `json:"message_ids,omitempty"`
	DegradedDelivery    bool          `json:"degraded_delivery,omitempty"`
	AwayDetected        bool          `json:"away_detected"`
	DisplayAsleep       bool          `json:"display_asleep,omitempty"`
	ScreenLocked        bool          `json:"screen_locked,omitempty"`
	AwayMethod          string        `json:"away_method,omitempty"`
	ClaimedEventID      string        `json:"claimed_event_id,omitempty"`
	HandoffEventID      string        `json:"handoff_event_id,omitempty"`
	HandoffMode         HandoffMode   `json:"handoff_mode,omitempty"`
	HandoffAt           time.Time     `json:"handoff_at,omitempty"`
	CreatedAt           time.Time     `json:"created_at"`
	UpdatedAt           time.Time     `json:"updated_at"`
	ExpiresAt           time.Time     `json:"expires_at"`
}

type Delivery struct {
	RequestID      string    `json:"request_id"`
	MessageID      string    `json:"message_id"`
	ChatID         string    `json:"chat_id"`
	SenderIdentity string    `json:"sender_identity"`
	Degraded       bool      `json:"degraded,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

type IncomingKind string

const (
	IncomingCardAction IncomingKind = "card_action"
	IncomingMessage    IncomingKind = "message"
)

type IncomingEvent struct {
	EventID       string          `json:"event_id"`
	Kind          IncomingKind    `json:"kind"`
	MessageID     string          `json:"message_id,omitempty"`
	ChatID        string          `json:"chat_id,omitempty"`
	SenderID      string          `json:"sender_id,omitempty"`
	ReplyTo       string          `json:"reply_to,omitempty"`
	RootID        string          `json:"root_id,omitempty"`
	RequestHint   string          `json:"request_hint,omitempty"`
	Action        string          `json:"action,omitempty"`
	ChoiceID      string          `json:"choice_id,omitempty"`
	Text          string          `json:"text,omitempty"`
	CallbackToken string          `json:"callback_token,omitempty"`
	CardContent   string          `json:"card_content,omitempty"`
	ReceivedAt    time.Time       `json:"received_at"`
	Source        string          `json:"source,omitempty"`
	Synthetic     bool            `json:"synthetic,omitempty"`
	Raw           json.RawMessage `json:"raw,omitempty"`
}

type RoutedReply struct {
	RequestID     string    `json:"request_id"`
	Platform      Platform  `json:"platform"`
	SessionID     string    `json:"session_id"`
	CWD           string    `json:"cwd,omitempty"`
	Action        string    `json:"action"`
	ChoiceID      string    `json:"choice_id,omitempty"`
	Text          string    `json:"text,omitempty"`
	CallbackToken string    `json:"callback_token,omitempty"`
	CardContent   string    `json:"card_content,omitempty"`
	EventID       string    `json:"event_id"`
	Source        string    `json:"source,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type DeliveryPlan struct {
	Type                  string        `json:"type"`
	RequestID             string        `json:"request_id"`
	Status                RequestStatus `json:"status"`
	Project               string        `json:"project"`
	Platform              Platform      `json:"platform"`
	ExpiresAt             time.Time     `json:"expires_at"`
	TargetChatID          string        `json:"target_chat_id,omitempty"`
	TargetUserID          string        `json:"target_user_id,omitempty"`
	RequiredIdentity      string        `json:"required_identity"`
	CardVersion           string        `json:"card_version"`
	TurnOutput            string        `json:"turn_output,omitempty"`
	TurnOutputPartCount   int           `json:"turn_output_part_count"`
	PartCommandTemplate   string        `json:"part_command_template,omitempty"`
	TurnOutputTruncated   bool          `json:"turn_output_truncated,omitempty"`
	RequireContextForm    bool          `json:"require_context_form"`
	Actions               []string      `json:"actions"`
	RecordCommandTemplate string        `json:"record_command_template"`
	DegradedCommand       string        `json:"degraded_record_command_template"`
	FailureCommand        string        `json:"failure_command"`
}

type DeliveryPart struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id"`
	Index     int    `json:"index"`
	Count     int    `json:"count"`
	Content   string `json:"content"`
}

type HookInput struct {
	SessionID            string          `json:"session_id"`
	TurnID               string          `json:"turn_id,omitempty"`
	TranscriptPath       string          `json:"transcript_path,omitempty"`
	CWD                  string          `json:"cwd,omitempty"`
	HookEventName        string          `json:"hook_event_name,omitempty"`
	Source               string          `json:"source,omitempty"`
	StopHookActive       bool            `json:"stop_hook_active"`
	LastAssistantMessage json.RawMessage `json:"last_assistant_message,omitempty"`
}

type HookDecision struct {
	Decision      string `json:"decision,omitempty"`
	Reason        string `json:"reason,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`
}

type HookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

type SessionStartDecision struct {
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
	SystemMessage      string              `json:"systemMessage,omitempty"`
}
