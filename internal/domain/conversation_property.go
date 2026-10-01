package domain

import (
	"bytes"
	"encoding/json"
	"strings"
)

// ConversationPropertyBulkLimit is the most channels one
// admin.conversations.bulkSetProperties request may name.
const ConversationPropertyBulkLimit = 100

// ConversationPropertyExcludeFromSlackAI is the property name Slack's
// admin.conversations.bulkSetProperties reference gives as its example, and
// the one channel property this deployment holds durably (the same flag
// admin.conversations.bulkSetExcludeFromSlackAi writes).
const ConversationPropertyExcludeFromSlackAI = "exclude_from_slack_ai"

// ConversationProperty is one channel property to set. It is a value of a
// known property, never a free-form key: a request that names anything else
// is refused when it is parsed, so the service and the store cannot be asked
// to write a property nothing reads.
type ConversationProperty struct {
	// ExcludeFromSlackAI keeps the channel out of the workspace's generative
	// features.
	ExcludeFromSlackAI bool
}

// ParseConversationProperty reads the property argument, a JSON object of
// exactly one key and its value. The checks follow the codes the method's
// reference declares: an argument that is not such an object is
// ErrInvalidConversationProperty, more than one key is
// ErrTooManyConversationProperties, and a key that may not be updated is
// ErrConversationPropertyNotAllowed.
func ParseConversationProperty(raw string) (ConversationProperty, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil || decoder.More() {
		return ConversationProperty{}, ErrInvalidConversationProperty
	}
	switch {
	case len(object) == 0:
		return ConversationProperty{}, ErrInvalidConversationProperty
	case len(object) > 1:
		return ConversationProperty{}, ErrTooManyConversationProperties
	}
	for key, value := range object {
		if key != ConversationPropertyExcludeFromSlackAI {
			return ConversationProperty{}, ErrConversationPropertyNotAllowed
		}
		switch string(bytes.TrimSpace(value)) {
		case "true":
			return ConversationProperty{ExcludeFromSlackAI: true}, nil
		case "false":
			return ConversationProperty{ExcludeFromSlackAI: false}, nil
		}
	}
	return ConversationProperty{}, ErrInvalidConversationProperty
}
