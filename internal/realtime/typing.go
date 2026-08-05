package realtime

import (
	"context"
	"encoding/json"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// Typing delivery, on both live transports.
//
// Everything else these streams carry comes out of the journal: the loop reads
// records after a cursor and translates them. A typing signal has no record,
// deliberately — see domain.TypingSignal — so it is read as state on its own
// timer and turned into a frame here.
//
// That difference has one consequence worth stating, because it is easy to
// reintroduce by accident: a typing frame carries no event id. The SSE client
// stores the last id it saw and resumes from it, so stamping these frames with
// a sequence they do not have would rewind or corrupt that cursor, and the
// reader would be sent the journal again from the wrong place.

// TypingSource is the half of the service a live stream needs to report who is
// composing. It is separate from RTMMessageService because reading typing state
// is not posting, and a stream that could do one should not be handed the other.
type TypingSource interface {
	SetTyping(context.Context, domain.WorkspaceID, domain.UserID, domain.ConversationID) error
	TypingSignals(context.Context, domain.WorkspaceID, domain.UserID) ([]domain.TypingSignal, error)
}

// typingPollInterval is how often a live stream re-reads who is composing. It
// is deliberately slower than the 250 ms journal poll: a typing indicator that
// appears a fraction of a second late reads as instant, and this is one store
// query per open stream, so the cadence is a real deployment cost rather than a
// free knob. It also has to stay well inside domain.TypingSignalTTL, or a
// steady typist would blink out between polls.
const typingPollInterval = time.Second

// typingAnnouncer turns the typing state a stream observes into the frames it
// has not sent yet.
//
// Slack's user_typing is repeated while composing continues rather than paired
// with a "stopped" frame, and this reproduces that from state: a member's
// signal is announced whenever its expiry has moved on from the one last
// announced, which is exactly once per renewal by the composing client. A
// member who stops typing produces no frame at all — their signal simply
// expires, and the client's own timer clears the indicator. That is why there
// is no "stopped" event to lose.
type typingAnnouncer struct {
	announced map[typingKey]time.Time
}

type typingKey struct {
	conversation domain.ConversationID
	user         domain.UserID
}

func newTypingAnnouncer() *typingAnnouncer {
	return &typingAnnouncer{announced: make(map[typingKey]time.Time)}
}

// due reports which of the observed signals have not been announced at their
// current expiry, and forgets the ones that are no longer present so a member
// who returns after a pause is announced again rather than being suppressed by
// a memory of the signal they used to have.
func (a *typingAnnouncer) due(signals []domain.TypingSignal) []domain.TypingSignal {
	fresh := make([]domain.TypingSignal, 0, len(signals))
	seen := make(map[typingKey]struct{}, len(signals))
	for _, signal := range signals {
		key := typingKey{conversation: signal.Conversation, user: signal.UserID}
		seen[key] = struct{}{}
		if last, ok := a.announced[key]; ok && !signal.ExpiresAt.After(last) {
			continue
		}
		a.announced[key] = signal.ExpiresAt
		fresh = append(fresh, signal)
	}
	for key := range a.announced {
		if _, present := seen[key]; !present {
			delete(a.announced, key)
		}
	}
	return fresh
}

// typingFrame is Slack's RTM user_typing frame. The field names are Slack's,
// not this product's, because an official RTM client dispatches on "type" and
// reads "channel" and "user"; a frame shaped like our domain would arrive and
// fire no listener.
func typingFrame(signal domain.TypingSignal) ([]byte, error) {
	return json.Marshal(map[string]string{
		"type":    "user_typing",
		"channel": string(signal.Conversation),
		"user":    string(signal.UserID),
	})
}

// typingCommand is the client half: an RTM client announces composition by
// sending {"type":"typing","channel":"C…"} with no id, and Slack sends no
// acknowledgement back. It is the one RTM command that is fire-and-forget,
// which is consistent with the signal itself — there is nothing to confirm
// about a fact that expires in six seconds.
func typingCommand(raw string) (domain.ConversationID, bool) {
	var command struct {
		Type    string `json:"type"`
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal([]byte(raw), &command); err != nil || command.Type != "typing" {
		return "", false
	}
	if command.Channel == "" {
		return "", false
	}
	return domain.ConversationID(command.Channel), true
}
