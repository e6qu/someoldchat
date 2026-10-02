package service

import (
	"encoding/json"
	"strings"

	"github.com/sameoldchat/sameoldchat/internal/blockkit"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// maxMessageBlocks is Slack's limit for a message; a view may hold 100.
const maxMessageBlocks = 50

// validateMessageBlocks applies the Block Kit invariants blocks.validate
// reports to blocks an app writes into a message, so every message write path
// answers invalid_blocks for what Slack would refuse instead of storing blocks
// no client can render. Empty blocks are valid: the message has none.
func validateMessageBlocks(normalized string) error {
	if strings.TrimSpace(normalized) == "" {
		return nil
	}
	problems, err := blockkit.ValidateBlocks(json.RawMessage(normalized), "", maxMessageBlocks)
	if err != nil || len(problems) != 0 {
		return domain.ErrInvalidBlocks
	}
	return nil
}
