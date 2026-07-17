package router

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/state"
)

var (
	ErrDuplicate = errors.New("duplicate event")
	ErrUnrouted  = errors.New("event could not be routed uniquely")
	ErrRejected  = errors.New("event rejected")
)

var allowedActions = map[string]struct{}{
	"continue":       {},
	"close":          {},
	"answer":         {},
	"submit_context": {},
	"retry":          {},
	"cancel":         {},
}

type Router struct {
	store *state.Store
	now   func() time.Time
}

type Result struct {
	Disposition string                       `json:"disposition"`
	Request     *contract.InteractionRequest `json:"request,omitempty"`
	Reply       *contract.RoutedReply        `json:"reply,omitempty"`
}

func New(store *state.Store) *Router {
	return &Router{store: store, now: time.Now}
}

func NewWithClock(store *state.Store, now func() time.Time) *Router {
	return &Router{store: store, now: now}
}

func (r *Router) Handle(event contract.IncomingEvent) (Result, error) {
	if event.EventID == "" {
		return Result{}, fmt.Errorf("%w: missing event_id", ErrRejected)
	}
	if event.ReceivedAt.IsZero() {
		event.ReceivedAt = r.now().UTC()
	}
	var result Result
	err := r.store.Update(func(db *state.Database) error {
		now := r.now().UTC()
		expireRequests(db, now)
		if _, ok := db.Events[event.EventID]; ok {
			return ErrDuplicate
		}
		request, routeErr := findRequest(db, event)
		if routeErr != nil {
			db.Events[event.EventID] = state.ProcessedEvent{SeenAt: now, Source: event.Source, Synthetic: event.Synthetic}
			db.Unrouted = appendBounded(db.Unrouted, event, 100)
			result.Disposition = "unrouted"
			return routeErr
		}
		if request.ExpiresAt.Before(now) || request.State == contract.StateExpired {
			request.State = contract.StateExpired
			request.UpdatedAt = now
			return fmt.Errorf("%w: request expired", ErrRejected)
		}
		if !request.State.Active() {
			return fmt.Errorf("%w: request is %s", ErrRejected, request.State)
		}
		if request.ChatID != "" && event.ChatID != "" && request.ChatID != event.ChatID {
			return fmt.Errorf("%w: chat mismatch", ErrRejected)
		}
		if !senderAllowed(request.AllowedSenderIDs, event.SenderID) {
			return fmt.Errorf("%w: sender is not allowed", ErrRejected)
		}
		action := normalizeAction(event)
		if _, ok := allowedActions[action]; !ok {
			return fmt.Errorf("%w: action %q is not allowed", ErrRejected, action)
		}
		if event.Kind == contract.IncomingCardAction && event.RequestHint != "" && !requestMatchesHint(request, event.RequestHint) {
			return fmt.Errorf("%w: callback request hint does not match delivery", ErrRejected)
		}

		db.Events[event.EventID] = state.ProcessedEvent{RequestID: request.ID, SeenAt: now, Source: event.Source, Synthetic: event.Synthetic}
		request.ClaimedEventID = event.EventID
		request.UpdatedAt = now
		result.Request = request
		if action == "close" {
			request.State = contract.StateClosed
			result.Disposition = "closed"
			return nil
		}
		if action == "cancel" {
			request.State = contract.StateCancelled
			result.Disposition = "cancelled"
			return nil
		}
		request.State = contract.StateClaimed
		result.Disposition = "routed"
		result.Reply = &contract.RoutedReply{
			RequestID:     request.ID,
			Platform:      request.Platform,
			SessionID:     request.SessionID,
			CWD:           request.CWD,
			Action:        action,
			ChoiceID:      event.ChoiceID,
			Text:          event.Text,
			CallbackToken: event.CallbackToken,
			CardContent:   event.CardContent,
			EventID:       event.EventID,
			Source:        event.Source,
			CreatedAt:     now,
		}
		key := state.InboxKey(request.Platform, request.SessionID)
		db.Inbox[key] = append(db.Inbox[key], &state.InboxItem{Reply: *result.Reply})
		if !event.Synthetic {
			db.Verification = appendBounded(db.Verification, event, 100)
		}
		return nil
	})
	return result, err
}

func findRequest(db *state.Database, event contract.IncomingEvent) (*contract.InteractionRequest, error) {
	if event.Kind == contract.IncomingCardAction && event.MessageID != "" {
		if delivery, ok := db.Deliveries[event.MessageID]; ok {
			if req := db.Requests[delivery.RequestID]; req != nil {
				return req, nil
			}
		}
	}
	for _, messageID := range []string{event.ReplyTo, event.RootID} {
		if messageID == "" {
			continue
		}
		if delivery, ok := db.Deliveries[messageID]; ok {
			if req := db.Requests[delivery.RequestID]; req != nil {
				return req, nil
			}
		}
	}
	if event.RequestHint != "" {
		for _, req := range db.Requests {
			if req.State.Active() && requestMatchesHint(req, event.RequestHint) && chatMatches(req, event.ChatID) {
				return req, nil
			}
		}
	}
	var candidate *contract.InteractionRequest
	for _, req := range db.Requests {
		if !req.State.Active() || !chatMatches(req, event.ChatID) || !senderAllowed(req.AllowedSenderIDs, event.SenderID) {
			continue
		}
		if candidate != nil {
			return nil, ErrUnrouted
		}
		candidate = req
	}
	if candidate == nil {
		return nil, ErrUnrouted
	}
	return candidate, nil
}

func expireRequests(db *state.Database, now time.Time) {
	for _, req := range db.Requests {
		if req.State.Active() && !req.ExpiresAt.After(now) {
			req.State = contract.StateExpired
			req.UpdatedAt = now
		}
	}
}

func normalizeAction(event contract.IncomingEvent) string {
	action := strings.ToLower(strings.TrimSpace(event.Action))
	if action != "" {
		return action
	}
	if event.Kind == contract.IncomingMessage {
		return "submit_context"
	}
	return ""
}

func senderAllowed(allowed []string, sender string) bool {
	if len(allowed) == 0 {
		return false
	}
	if sender == "" {
		return false
	}
	for _, id := range allowed {
		if id == sender {
			return true
		}
	}
	return false
}

func chatMatches(req *contract.InteractionRequest, chatID string) bool {
	return req.ChatID == "" || chatID == "" || req.ChatID == chatID
}

func requestMatchesHint(req *contract.InteractionRequest, hint string) bool {
	hint = strings.ToUpper(strings.TrimSpace(hint))
	return strings.ToUpper(req.ID) == hint || strings.ToUpper(req.ShortCode) == hint
}

func appendBounded[T any](items []T, item T, limit int) []T {
	items = append(items, item)
	if len(items) > limit {
		items = append([]T(nil), items[len(items)-limit:]...)
	}
	return items
}
