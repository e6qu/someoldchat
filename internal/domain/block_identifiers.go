package domain

import (
	"bytes"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"strconv"
	"strings"
)

// Slack fills in the identifiers an app leaves out of Block Kit: every
// top-level block gets a block_id and every interactive element an action_id,
// and the stored message and every payload that echoes it carry them. An
// interaction is addressed by that pair, so a block or element without one
// cannot be clicked: the first-party client skipped such elements, and the
// interaction check compared "" against the first block that also lacked one.
//
// Identifiers are derived from the block's position and content rather than
// drawn at random, so normalising the same input twice — a write that the
// service and then the store both normalise, a streamed message re-normalised
// per chunk — yields the same identifiers and the normalisation stays
// idempotent. An identifier an app supplied is never changed, and a generated
// one never collides with one the app supplied.

// actionElementFields names where a block holds its interactive elements.
var actionElementFields = []string{"elements", "accessory", "element"}

// blocksWithActionElements are the blocks whose "elements" are interactive.
// Other blocks — context, rich_text — use "elements" for text and images.
var blocksWithActionElements = map[string]bool{"actions": true, "context_actions": true}

// nonInteractiveElements occupy an element slot without being actions: an
// image accessory, for example, has no action_id in Slack.
var nonInteractiveElements = map[string]bool{"image": true, "plain_text": true, "mrkdwn": true}

// AssignBlockIdentifiers gives each block in blocks a block_id when it has
// none and each interactive element an action_id when it has none. It reports
// whether it changed anything. Values that are not objects are left alone;
// validation of the block shapes belongs to the callers that already do it.
func AssignBlockIdentifiers(blocks []any) bool {
	changed := false
	takenBlocks := make(map[string]struct{}, len(blocks))
	for _, raw := range blocks {
		if block, ok := raw.(map[string]any); ok {
			if id := identifierValue(block["block_id"]); id != "" {
				takenBlocks[id] = struct{}{}
			}
		}
	}
	for index, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		blockID := identifierValue(block["block_id"])
		if blockID == "" {
			blockID = derivedIdentifier(takenBlocks, "block", strconv.Itoa(index), block)
			block["block_id"] = blockID
			changed = true
		}
		if assignActionIdentifiers(block, blockID) {
			changed = true
		}
	}
	return changed
}

func assignActionIdentifiers(block map[string]any, blockID string) bool {
	kind, _ := block["type"].(string)
	var elements []map[string]any
	for _, field := range actionElementFields {
		switch value := block[field].(type) {
		case []any:
			if field != "elements" || !blocksWithActionElements[kind] {
				continue
			}
			for _, raw := range value {
				if element, ok := raw.(map[string]any); ok {
					elements = append(elements, element)
				}
			}
		case map[string]any:
			if field == "element" && kind != "input" {
				continue
			}
			elements = append(elements, value)
		}
	}
	taken := make(map[string]struct{}, len(elements))
	for _, element := range elements {
		if id := identifierValue(element["action_id"]); id != "" {
			taken[id] = struct{}{}
		}
	}
	changed := false
	for index, element := range elements {
		elementType, _ := element["type"].(string)
		if elementType == "" || nonInteractiveElements[elementType] || identifierValue(element["action_id"]) != "" {
			continue
		}
		element["action_id"] = derivedIdentifier(taken, "action", blockID+"\x00"+strconv.Itoa(index), element)
		changed = true
	}
	return changed
}

func identifierValue(value any) string {
	text, _ := value.(string)
	return strings.TrimSpace(text)
}

var identifierEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// derivedIdentifier returns a short identifier, shaped like the ones Slack
// generates, from a digest of the kind, position, and content, lengthened
// until it is not already taken, and records it as taken.
func derivedIdentifier(taken map[string]struct{}, kind, position string, value map[string]any) string {
	content, _ := json.Marshal(value)
	digest := sha256.Sum256([]byte(kind + "\x00" + position + "\x00" + string(content)))
	encoded := identifierEncoding.EncodeToString(digest[:])
	for length := 5; length <= len(encoded); length++ {
		candidate := encoded[:length]
		if _, exists := taken[candidate]; !exists {
			taken[candidate] = struct{}{}
			return candidate
		}
	}
	for suffix := 1; ; suffix++ {
		candidate := encoded + strconv.Itoa(suffix)
		if _, exists := taken[candidate]; !exists {
			taken[candidate] = struct{}{}
			return candidate
		}
	}
}

// assignMessageBlockIdentifiers applies AssignBlockIdentifiers to a
// normalised blocks array. The input is returned byte for byte when nothing
// was missing, so a message whose identifiers are complete keeps the exact
// encoding it was written with; numbers are carried as written either way.
func assignMessageBlockIdentifiers(normalized string) (string, error) {
	decoder := json.NewDecoder(strings.NewReader(normalized))
	decoder.UseNumber()
	var blocks []any
	if err := decoder.Decode(&blocks); err != nil {
		return "", err
	}
	if !AssignBlockIdentifiers(blocks) {
		return normalized, nil
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(blocks); err != nil {
		return "", err
	}
	return strings.TrimSuffix(encoded.String(), "\n"), nil
}
