package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// Slack assigns the block_id and action_id an app omits, and an interaction
// is addressed by exactly that pair, so every stored block and every
// interactive element must carry one — without ever overwriting or colliding
// with an identifier the app chose.
func TestNormalizeBlocksAssignsMissingBlockAndActionIdentifiers(t *testing.T) {
	raw := `[
		{"type":"section","text":{"type":"mrkdwn","text":"pick"},"accessory":{"type":"button","text":{"type":"plain_text","text":"Go"}}},
		{"type":"section","text":{"type":"mrkdwn","text":"photo"},"accessory":{"type":"image","image_url":"https://example.com/a.png","alt_text":"a"}},
		{"type":"actions","block_id":"mine","elements":[
			{"type":"button","action_id":"approve","text":{"type":"plain_text","text":"Yes"}},
			{"type":"button","text":{"type":"plain_text","text":"No"}},
			{"type":"static_select","options":[]}
		]},
		{"type":"context","elements":[{"type":"mrkdwn","text":"note"},{"type":"image","image_url":"https://example.com/b.png","alt_text":"b"}]},
		{"type":"input","label":{"type":"plain_text","text":"L"},"element":{"type":"plain_text_input"}},
		{"type":"divider","big":12345678901234567890}
	]`
	normalized, err := NormalizeBlocks([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	decoder := json.NewDecoder(strings.NewReader(normalized))
	decoder.UseNumber()
	if err := decoder.Decode(&blocks); err != nil {
		t.Fatal(err)
	}
	seenBlocks := map[string]bool{}
	for index, block := range blocks {
		id, _ := block["block_id"].(string)
		if id == "" || seenBlocks[id] {
			t.Fatalf("block %d block_id=%q, want a distinct identifier: %s", index, id, normalized)
		}
		seenBlocks[id] = true
	}
	if blocks[2]["block_id"] != "mine" {
		t.Fatalf("app-supplied block_id was replaced: %v", blocks[2]["block_id"])
	}
	if accessory := blocks[0]["accessory"].(map[string]any); accessory["action_id"] == nil || accessory["action_id"] == "" {
		t.Fatalf("button accessory has no action_id: %s", normalized)
	}
	if accessory := blocks[1]["accessory"].(map[string]any); accessory["action_id"] != nil {
		t.Fatalf("an image accessory is not an action: %s", normalized)
	}
	elements := blocks[2]["elements"].([]any)
	seenActions := map[any]bool{}
	for index, raw := range elements {
		actionID := raw.(map[string]any)["action_id"]
		if actionID == nil || actionID == "" || seenActions[actionID] {
			t.Fatalf("actions element %d action_id=%v, want a distinct identifier: %s", index, actionID, normalized)
		}
		seenActions[actionID] = true
	}
	if elements[0].(map[string]any)["action_id"] != "approve" {
		t.Fatalf("app-supplied action_id was replaced: %s", normalized)
	}
	for _, raw := range blocks[3]["elements"].([]any) {
		if raw.(map[string]any)["action_id"] != nil {
			t.Fatalf("context text and images are not actions: %s", normalized)
		}
	}
	if element := blocks[4]["element"].(map[string]any); element["action_id"] == nil {
		t.Fatalf("input element has no action_id: %s", normalized)
	}
	if blocks[5]["big"].(json.Number).String() != "12345678901234567890" {
		t.Fatalf("a large number lost precision: %s", normalized)
	}
	again, err := NormalizeBlocks([]byte(normalized))
	if err != nil || again != normalized {
		t.Fatalf("normalisation is not idempotent:\n%s\n%s err=%v", normalized, again, err)
	}
	fresh, err := NormalizeBlocks([]byte(raw))
	if err != nil || fresh != normalized {
		t.Fatalf("generated identifiers are not deterministic:\n%s\n%s", normalized, fresh)
	}
}

// A generated identifier must not take one the app chose for a later block.
func TestGeneratedBlockIdentifiersAvoidAppSuppliedOnes(t *testing.T) {
	first, err := NormalizeBlocks([]byte(`[{"type":"divider"}]`))
	if err != nil {
		t.Fatal(err)
	}
	var generated []map[string]any
	if err := json.Unmarshal([]byte(first), &generated); err != nil {
		t.Fatal(err)
	}
	id := generated[0]["block_id"].(string)
	collision, err := NormalizeBlocks([]byte(`[{"type":"divider"},{"type":"divider","block_id":"` + id + `"}]`))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]any
	if err := json.Unmarshal([]byte(collision), &blocks); err != nil {
		t.Fatal(err)
	}
	if blocks[0]["block_id"] == blocks[1]["block_id"] || blocks[1]["block_id"] != id {
		t.Fatalf("generated block_id collided with an app-supplied one: %s", collision)
	}
}
