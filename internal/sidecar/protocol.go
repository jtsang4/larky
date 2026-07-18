package sidecar

import (
	"encoding/json"

	"github.com/jtsang4/larky/internal/contract"
)

type command struct {
	Op        string            `json:"op"`
	Platform  contract.Platform `json:"platform,omitempty"`
	SessionID string            `json:"session_id,omitempty"`
	EventKey  string            `json:"event_key,omitempty"`
	Event     json.RawMessage   `json:"event,omitempty"`
	Synthetic bool              `json:"synthetic,omitempty"`
}

type response struct {
	OK     bool                  `json:"ok"`
	Error  string                `json:"error,omitempty"`
	Status *Status               `json:"status,omitempty"`
	Result any                   `json:"result,omitempty"`
	Reply  *contract.RoutedReply `json:"reply,omitempty"`
}

type Status struct {
	PID              int             `json:"pid"`
	StartedAt        string          `json:"started_at"`
	ExecutableDigest string          `json:"executable_digest"`
	Subscribers      int             `json:"subscribers"`
	EventsEnabled    bool            `json:"events_enabled"`
	EventsReady      bool            `json:"events_ready"`
	EventConsumers   map[string]bool `json:"event_consumers"`
	PendingByKind    map[string]int  `json:"pending_by_kind"`
}
