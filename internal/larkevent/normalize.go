package larkevent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jtsang4/larky/internal/contract"
)

var requestCodePattern = regexp.MustCompile(`(?i)(?:^|[^A-Z0-9])([A-Z2-9]{6})(?:$|[^A-Z0-9])`)

func Normalize(eventKey string, raw []byte, now time.Time) (contract.IncomingEvent, error) {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		return contract.IncomingEvent{}, fmt.Errorf("parse event JSON: %w", err)
	}
	event := contract.IncomingEvent{
		EventID:    stringValue(value, "event_id"),
		MessageID:  stringValue(value, "message_id"),
		ChatID:     stringValue(value, "chat_id"),
		ReplyTo:    stringValue(value, "reply_to"),
		RootID:     stringValue(value, "root_id"),
		ReceivedAt: now.UTC(),
		Source:     "lark-live",
		Raw:        append([]byte(nil), raw...),
	}
	if event.EventID == "" {
		sum := sha256.Sum256(raw)
		event.EventID = "derived-" + hex.EncodeToString(sum[:12])
	}
	switch eventKey {
	case "card.action.trigger":
		event.Kind = contract.IncomingCardAction
		event.SenderID = stringValue(value, "operator_id")
		normalizeCard(value, &event)
	case "im.message.receive_v1":
		event.Kind = contract.IncomingMessage
		event.SenderID = stringValue(value, "sender_id")
		event.Text = stringValue(value, "content", "text")
		event.RequestHint = extractRequestCode(event.Text)
	default:
		return contract.IncomingEvent{}, fmt.Errorf("unsupported event key %q", eventKey)
	}
	if event.ChatID == "" || event.SenderID == "" {
		return contract.IncomingEvent{}, fmt.Errorf("event %s is missing chat_id or sender identity", event.EventID)
	}
	return event, nil
}

func normalizeCard(value map[string]any, event *contract.IncomingEvent) {
	event.CallbackToken = stringValue(value, "token")
	event.CardContent = stringValue(value, "card_content")
	actionName := stringValue(value, "action_name")
	actionValue := stringValue(value, "action_value")
	payload := parseObject(actionValue)
	event.Action = firstNonEmpty(stringFromMap(payload, "action"), actionName)
	event.RequestHint = stringFromMap(payload, "request_id")
	event.ChoiceID = firstNonEmpty(
		stringFromMap(payload, "choice_id"),
		stringValue(value, "option"),
		stringValue(value, "options"),
	)
	form := parseObject(stringValue(value, "form_value"))
	event.Text = firstNonEmpty(
		stringFromMap(form, "context"),
		stringFromMap(form, "context_value"),
		stringFromMap(form, "answer"),
		stringFromMap(form, "answer_value"),
		stringFromMap(form, "input"),
		stringFromMap(form, "input_value"),
		stringValue(value, "input_value"),
	)
	if event.Action == "" && len(form) > 0 {
		event.Action = "submit_context"
	}
}

func extractRequestCode(text string) string {
	match := requestCodePattern.FindStringSubmatch(strings.ToUpper(text))
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func parseObject(value string) map[string]any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	var result map[string]any
	if json.Unmarshal([]byte(value), &result) != nil {
		return nil
	}
	return result
}

func stringFromMap(value map[string]any, key string) string {
	if value == nil {
		return ""
	}
	return scalarString(value[key])
}

func stringValue(value map[string]any, keys ...string) string {
	for _, key := range keys {
		if direct, ok := value[key]; ok {
			if result := scalarString(direct); result != "" {
				return result
			}
		}
	}
	for _, key := range keys {
		if found, ok := findNested(value, key); ok {
			if result := scalarString(found); result != "" {
				return result
			}
		}
	}
	return ""
}

func findNested(value any, wanted string) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if key == wanted {
				return typed[key], true
			}
		}
		for _, key := range keys {
			if found, ok := findNested(typed[key], wanted); ok {
				return found, true
			}
		}
	case []any:
		for _, item := range typed {
			if found, ok := findNested(item, wanted); ok {
				return found, true
			}
		}
	}
	return nil, false
}

func scalarString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return fmt.Sprintf("%.0f", typed)
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
