package larkevent

import (
	"testing"
	"time"

	"github.com/jtsang4/larky/internal/contract"
)

func TestNormalizeCardAction(t *testing.T) {
	raw := []byte(`{"type":"card.action.trigger","event_id":"evt-1","operator_id":"ou-user","message_id":"om-card","chat_id":"oc-chat","action_tag":"button","action_value":"{\"v\":1,\"request_id\":\"L7K2AA\",\"action\":\"answer\",\"choice_id\":\"a\"}"}`)
	event, err := Normalize("card.action.trigger", raw, time.Unix(1, 0))
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != contract.IncomingCardAction || event.RequestHint != "L7K2AA" || event.Action != "answer" || event.ChoiceID != "a" {
		t.Fatalf("unexpected event: %#v", event)
	}
}

func TestNormalizeFormAndMessage(t *testing.T) {
	form := []byte(`{"event_id":"evt-form","operator_id":"ou-user","message_id":"om-card","chat_id":"oc-chat","action_tag":"button","action_name":"submit_context","form_value":"{\"context\":\"run the race tests\"}"}`)
	event, err := Normalize("card.action.trigger", form, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.Action != "submit_context" || event.Text != "run the race tests" {
		t.Fatalf("unexpected form event: %#v", event)
	}

	message := []byte(`{"event_id":"evt-msg","sender_id":"ou-user","message_id":"om-reply","chat_id":"oc-chat","reply_to":"om-card","content":"L7K2AA please continue"}`)
	event, err = Normalize("im.message.receive_v1", message, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != contract.IncomingMessage || event.RequestHint != "L7K2AA" || event.ReplyTo != "om-card" {
		t.Fatalf("unexpected message event: %#v", event)
	}
}
