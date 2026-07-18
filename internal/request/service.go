package request

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jtsang4/larky/internal/config"
	"github.com/jtsang4/larky/internal/contract"
	"github.com/jtsang4/larky/internal/state"
)

const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

type Service struct {
	store *state.Store
	cfg   config.Config
	now   func() time.Time
}

type CreateInput struct {
	Platform          contract.Platform
	SessionID         string
	TurnID            string
	PreviousRequestID string
	CWD               string
	Message           string
	AwayDetected      bool
	DisplayAsleep     bool
	ScreenLocked      bool
	AwayMethod        string
}

func NewService(store *state.Store, cfg config.Config) *Service {
	return &Service{store: store, cfg: cfg, now: time.Now}
}

func NewServiceWithClock(store *state.Store, cfg config.Config, now func() time.Time) *Service {
	return &Service{store: store, cfg: cfg, now: now}
}

func (s *Service) Create(input CreateInput) (*contract.InteractionRequest, bool, error) {
	if !input.Platform.Valid() || input.SessionID == "" {
		return nil, false, errors.New("platform and session_id are required")
	}
	if s.cfg.ChatID == "" && s.cfg.TargetUserID == "" {
		return nil, false, errors.New("larky target chat or target user is not configured")
	}
	if len(s.cfg.AllowedSenderIDs) == 0 {
		return nil, false, errors.New("larky allowed sender is not configured")
	}
	message := sanitizeSummary(input.Message)
	idempotency := idempotencyKey(input, message)
	var result *contract.InteractionRequest
	created := false
	err := s.store.Update(func(db *state.Database) error {
		if id, ok := db.Idempotency[idempotency]; ok {
			if existing := db.Requests[id]; existing != nil {
				copy := *existing
				result = &copy
				return nil
			}
		}
		id, err := uniqueID(db)
		if err != nil {
			return err
		}
		now := s.now().UTC()
		project := filepath.Base(input.CWD)
		if project == "." || project == string(filepath.Separator) {
			project = "coding task"
		}
		request := &contract.InteractionRequest{
			ID:                id,
			ShortCode:         id,
			IdempotencyKey:    idempotency,
			Platform:          input.Platform,
			SessionID:         input.SessionID,
			TurnID:            input.TurnID,
			PreviousRequestID: strings.ToUpper(input.PreviousRequestID),
			CWD:               input.CWD,
			Project:           project,
			Summary:           message,
			Status:            Classify(message),
			State:             contract.StatePendingDelivery,
			ChatID:            s.cfg.ChatID,
			TargetUserID:      s.cfg.TargetUserID,
			AllowedSenderIDs:  append([]string(nil), s.cfg.AllowedSenderIDs...),
			AwayDetected:      input.AwayDetected,
			DisplayAsleep:     input.DisplayAsleep,
			ScreenLocked:      input.ScreenLocked,
			AwayMethod:        input.AwayMethod,
			CreatedAt:         now,
			UpdatedAt:         now,
			ExpiresAt:         now.Add(s.cfg.RequestTTL),
		}
		db.Requests[id] = request
		db.Idempotency[idempotency] = id
		copy := *request
		result = &copy
		created = true
		return nil
	})
	return result, created, err
}

func (s *Service) RecordDelivery(requestID, messageID, chatID, senderIdentity string, degraded bool) error {
	if requestID == "" || messageID == "" || chatID == "" || senderIdentity == "" {
		return errors.New("request_id, message_id, chat_id, and sender identity are required")
	}
	senderIdentity = strings.ToLower(strings.TrimSpace(senderIdentity))
	expectedIdentity := strings.ToLower(strings.TrimSpace(s.cfg.EventIdentity))
	if expectedIdentity == "" {
		expectedIdentity = "bot"
	}
	if senderIdentity != expectedIdentity {
		return fmt.Errorf("delivery identity %q does not match event consumer identity %q; resend the message using the matching identity", senderIdentity, expectedIdentity)
	}
	return s.store.Update(func(db *state.Database) error {
		req := db.Requests[strings.ToUpper(requestID)]
		if req == nil {
			return fmt.Errorf("request %q not found", requestID)
		}
		if req.ChatID != "" && req.ChatID != chatID {
			return errors.New("delivery chat does not match request chat")
		}
		if existing, ok := db.Deliveries[messageID]; ok && existing.RequestID != req.ID {
			return errors.New("message is already assigned to another request")
		}
		now := s.now().UTC()
		delivery := contract.Delivery{RequestID: req.ID, MessageID: messageID, ChatID: chatID, SenderIdentity: senderIdentity, Degraded: degraded, CreatedAt: now}
		db.Deliveries[messageID] = delivery
		req.MessageID = messageID
		req.ChatID = chatID
		req.DegradedDelivery = degraded
		req.State = contract.StatePendingReply
		req.UpdatedAt = now
		return nil
	})
}

func (s *Service) MarkDeliveryFailed(requestID string) error {
	return s.store.Update(func(db *state.Database) error {
		req := db.Requests[strings.ToUpper(requestID)]
		if req == nil {
			return fmt.Errorf("request %q not found", requestID)
		}
		req.State = contract.StateDeliveryFailed
		req.UpdatedAt = s.now().UTC()
		return nil
	})
}

func (s *Service) LatestForSession(platform contract.Platform, sessionID, turnID string) (*contract.InteractionRequest, error) {
	if !platform.Valid() || sessionID == "" {
		return nil, errors.New("valid platform and session_id are required")
	}
	var result *contract.InteractionRequest
	err := s.store.View(func(db *state.Database) error {
		for _, req := range db.Requests {
			if req.Platform != platform || req.SessionID != sessionID {
				continue
			}
			if turnID != "" && req.TurnID != turnID {
				continue
			}
			if result == nil || req.CreatedAt.After(result.CreatedAt) || (req.CreatedAt.Equal(result.CreatedAt) && req.UpdatedAt.After(result.UpdatedAt)) {
				result = cloneRequest(req)
			}
		}
		return nil
	})
	return result, err
}

// AwaitingForSession returns the only unfinished request for an exact host
// session. It refuses to guess if corrupted or legacy state contains more than
// one candidate.
func (s *Service) AwaitingForSession(platform contract.Platform, sessionID string) (*contract.InteractionRequest, error) {
	if !platform.Valid() || sessionID == "" {
		return nil, errors.New("valid platform and session_id are required")
	}
	var result *contract.InteractionRequest
	err := s.store.View(func(db *state.Database) error {
		for _, req := range db.Requests {
			if req.Platform != platform || req.SessionID != sessionID {
				continue
			}
			switch req.State {
			case contract.StatePendingDelivery, contract.StatePendingReply, contract.StateClaimed:
			default:
				continue
			}
			if result != nil {
				return errors.New("multiple unfinished requests exist for this exact session")
			}
			result = cloneRequest(req)
		}
		return nil
	})
	return result, err
}

func (s *Service) GetForSession(requestID string, platform contract.Platform, sessionID string) (*contract.InteractionRequest, error) {
	if requestID == "" || !platform.Valid() || sessionID == "" {
		return nil, errors.New("request_id, valid platform, and session_id are required")
	}
	var result *contract.InteractionRequest
	err := s.store.View(func(db *state.Database) error {
		req := db.Requests[strings.ToUpper(requestID)]
		if req == nil || req.Platform != platform || req.SessionID != sessionID {
			return nil
		}
		result = cloneRequest(req)
		return nil
	})
	return result, err
}

// TakeReplyForHandoff atomically removes the reply for one exact request and
// records that it was handed back to the originating host session.
func (s *Service) TakeReplyForHandoff(requestID string, platform contract.Platform, sessionID string, mode contract.HandoffMode) (*contract.RoutedReply, error) {
	return s.takeReply(requestID, "", platform, sessionID, mode)
}

// TakeNextReplyForHandoff is used by SessionStart recovery. It never searches
// outside the exact platform/session inbox.
func (s *Service) TakeNextReplyForHandoff(platform contract.Platform, sessionID string, mode contract.HandoffMode) (*contract.RoutedReply, error) {
	return s.takeReply("", "", platform, sessionID, mode)
}

// AcknowledgeReplyHandoff records a reply only after a long-lived host
// subscriber has accepted it.
func (s *Service) AcknowledgeReplyHandoff(reply contract.RoutedReply, mode contract.HandoffMode) error {
	result, err := s.takeReply(reply.RequestID, reply.EventID, reply.Platform, reply.SessionID, mode)
	if err != nil {
		return err
	}
	if result == nil {
		return fmt.Errorf("reply %q is no longer queued for request %q", reply.EventID, reply.RequestID)
	}
	return nil
}

func (s *Service) takeReply(requestID, eventID string, platform contract.Platform, sessionID string, mode contract.HandoffMode) (*contract.RoutedReply, error) {
	if !platform.Valid() || sessionID == "" || mode == "" {
		return nil, errors.New("valid platform, session_id, and handoff mode are required")
	}
	requestID = strings.ToUpper(requestID)
	var result *contract.RoutedReply
	err := s.store.Update(func(db *state.Database) error {
		key := state.InboxKey(platform, sessionID)
		items := db.Inbox[key]
		for index, item := range items {
			reply := item.Reply
			if requestID != "" && strings.ToUpper(reply.RequestID) != requestID {
				continue
			}
			if eventID != "" && reply.EventID != eventID {
				continue
			}
			if reply.Platform != platform || reply.SessionID != sessionID {
				return errors.New("queued reply does not match its exact session inbox")
			}
			req := db.Requests[strings.ToUpper(reply.RequestID)]
			if req == nil || req.Platform != platform || req.SessionID != sessionID {
				return errors.New("queued reply has no matching exact-session request")
			}
			if req.State != contract.StateClaimed || req.ClaimedEventID != reply.EventID {
				return fmt.Errorf("request %q is not claimable for event %q", req.ID, reply.EventID)
			}
			now := s.now().UTC()
			req.State = contract.StateResumed
			req.HandoffEventID = reply.EventID
			req.HandoffMode = mode
			req.HandoffAt = now
			req.UpdatedAt = now
			db.Inbox[key] = append(items[:index], items[index+1:]...)
			if len(db.Inbox[key]) == 0 {
				delete(db.Inbox, key)
			}
			copy := reply
			result = &copy
			return nil
		}
		return nil
	})
	return result, err
}

func (s *Service) CancelAwaitingReply(requestID string, platform contract.Platform, sessionID string) error {
	if requestID == "" || !platform.Valid() || sessionID == "" {
		return errors.New("request_id, valid platform, and session_id are required")
	}
	requestID = strings.ToUpper(requestID)
	return s.store.Update(func(db *state.Database) error {
		req := db.Requests[requestID]
		if req == nil || req.Platform != platform || req.SessionID != sessionID {
			return nil
		}
		switch req.State {
		case contract.StatePendingDelivery, contract.StatePendingReply, contract.StateClaimed:
			req.State = contract.StateCancelled
			req.UpdatedAt = s.now().UTC()
		default:
			return nil
		}
		key := state.InboxKey(platform, sessionID)
		items := db.Inbox[key]
		kept := items[:0]
		for _, item := range items {
			if strings.ToUpper(item.Reply.RequestID) != requestID {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(db.Inbox, key)
		} else {
			db.Inbox[key] = kept
		}
		return nil
	})
}

func (s *Service) ExpireAwaitingReply(requestID string, platform contract.Platform, sessionID string) error {
	if requestID == "" || !platform.Valid() || sessionID == "" {
		return errors.New("request_id, valid platform, and session_id are required")
	}
	requestID = strings.ToUpper(requestID)
	return s.store.Update(func(db *state.Database) error {
		req := db.Requests[requestID]
		now := s.now().UTC()
		if req == nil || req.Platform != platform || req.SessionID != sessionID || req.ExpiresAt.After(now) {
			return nil
		}
		switch req.State {
		case contract.StatePendingDelivery, contract.StatePendingReply, contract.StateClaimed:
			req.State = contract.StateExpired
			req.UpdatedAt = now
		}
		key := state.InboxKey(platform, sessionID)
		items := db.Inbox[key]
		kept := items[:0]
		for _, item := range items {
			if strings.ToUpper(item.Reply.RequestID) != requestID {
				kept = append(kept, item)
			}
		}
		if len(kept) == 0 {
			delete(db.Inbox, key)
		} else {
			db.Inbox[key] = kept
		}
		return nil
	})
}

func cloneRequest(req *contract.InteractionRequest) *contract.InteractionRequest {
	copy := *req
	copy.AllowedSenderIDs = append([]string(nil), req.AllowedSenderIDs...)
	return &copy
}

func Classify(message string) contract.RequestStatus {
	lower := strings.ToLower(message)
	switch {
	case containsAny(lower, "failed", "failure", "error", "失败", "报错"):
		return contract.StatusFailed
	case containsAny(lower, "blocked", "blocker", "阻塞", "卡住"):
		return contract.StatusBlocked
	case containsAny(lower, "need your", "please choose", "which option", "waiting for", "需要你", "请选择", "请确认", "待确认") || strings.Contains(message, "?") || strings.Contains(message, "？"):
		return contract.StatusWaitingUser
	default:
		return contract.StatusDone
	}
}

func idempotencyKey(input CreateInput, message string) string {
	digest := sha256.Sum256([]byte(message))
	value := fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s", input.Platform, input.SessionID, input.TurnID, strings.ToUpper(input.PreviousRequestID), hex.EncodeToString(digest[:]))
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func uniqueID(db *state.Database) (string, error) {
	for attempt := 0; attempt < 20; attempt++ {
		buffer := make([]byte, 6)
		if _, err := rand.Read(buffer); err != nil {
			return "", fmt.Errorf("generate request id: %w", err)
		}
		for i := range buffer {
			buffer[i] = alphabet[int(buffer[i])%len(alphabet)]
		}
		id := string(buffer)
		if db.Requests[id] == nil {
			return id, nil
		}
	}
	return "", errors.New("could not allocate a unique request id")
}

func sanitizeSummary(message string) string {
	message = ansiPattern.ReplaceAllString(message, "")
	message = strings.TrimSpace(message)
	if message == "" {
		return "The coding agent stopped without a final summary."
	}
	const limit = 1200
	if utf8.RuneCountInString(message) <= limit {
		return message
	}
	runes := []rune(message)
	return strings.TrimSpace(string(runes[:limit])) + "…"
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
