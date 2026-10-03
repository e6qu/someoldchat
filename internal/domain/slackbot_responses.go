package domain

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Slackbot's custom responses are a workspace's own: when a member's message
// says one of a response's trigger phrases, Slackbot answers in the same place
// with one of the response's replies. Slackbot also answers a member who
// writes to it directly, with a matching custom response or, failing one, with
// help.

// SlackbotResponseID names one custom response.
type SlackbotResponseID string

// SlackbotResponse is one custom response: the phrases that trigger it and the
// replies Slackbot chooses among.
type SlackbotResponse struct {
	WorkspaceID WorkspaceID
	ID          SlackbotResponseID
	Triggers    []string
	Replies     []string
	CreatedBy   UserID
	CreatedAt   time.Time
}

// The bounds of a custom response.
const (
	SlackbotResponseTriggerLimit = 50
	SlackbotResponseReplyLimit   = 50
	slackbotTriggerLength        = 200
	slackbotReplyLength          = 4000
)

// ErrInvalidSlackbotResponse is a custom response outside its bounds.
var ErrInvalidSlackbotResponse = errors.New("invalid Slackbot response")

// SlackbotHelpText is Slackbot's answer to a direct message no custom response
// matches.
const SlackbotHelpText = "I'm Slackbot. I deliver your reminders and answer with the custom responses your workspace sets up. " +
	"To set a reminder, type `/remind me to <what> <when>`, for example `/remind me to stretch in 20 minutes`. " +
	"To find something, use search at the top of the window."

// NormalizeSlackbotResponse trims and checks a custom response. Triggers are
// given as Slack's form takes them, each a phrase; a phrase may itself hold
// several separated by commas. Duplicates are dropped, case aside.
func NormalizeSlackbotResponse(triggers, replies []string) ([]string, []string, error) {
	var phrases []string
	seen := map[string]struct{}{}
	for _, value := range triggers {
		for _, phrase := range strings.Split(value, ",") {
			phrase = strings.Join(strings.Fields(phrase), " ")
			if phrase == "" {
				continue
			}
			if utf8.RuneCountInString(phrase) > slackbotTriggerLength {
				return nil, nil, ErrInvalidSlackbotResponse
			}
			key := FoldSearchText(phrase)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			phrases = append(phrases, phrase)
		}
	}
	var answers []string
	for _, reply := range replies {
		reply = strings.TrimSpace(reply)
		if reply == "" {
			continue
		}
		if utf8.RuneCountInString(reply) > slackbotReplyLength {
			return nil, nil, ErrInvalidSlackbotResponse
		}
		answers = append(answers, reply)
	}
	if len(phrases) == 0 || len(answers) == 0 || len(phrases) > SlackbotResponseTriggerLimit || len(answers) > SlackbotResponseReplyLimit {
		return nil, nil, ErrInvalidSlackbotResponse
	}
	return phrases, answers, nil
}

// Matches reports a message that says one of the response's phrases as words
// of its own, without regard to case: "lunch" matches "Lunch?" but not
// "lunchtime".
func (r SlackbotResponse) Matches(text string) bool {
	words := slackbotWords(text)
	for _, trigger := range r.Triggers {
		phrase := slackbotWords(trigger)
		if len(phrase) == 0 {
			continue
		}
		for start := 0; start+len(phrase) <= len(words); start++ {
			same := true
			for index := range phrase {
				if words[start+index] != phrase[index] {
					same = false
					break
				}
			}
			if same {
				return true
			}
		}
	}
	return false
}

// Reply is the reply Slackbot gives for a message, chosen by the message so a
// replayed delivery gives the same one.
func (r SlackbotResponse) Reply(seed string) string {
	if len(r.Replies) == 0 {
		return ""
	}
	sum := 0
	for _, value := range seed {
		sum = sum*31 + int(value)
		sum &= 0x7fffffff
	}
	return r.Replies[sum%len(r.Replies)]
}

// slackbotWords is folded text split into words: letters, digits and marks.
func slackbotWords(text string) []string {
	return strings.FieldsFunc(FoldSearchText(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r)
	})
}
