package slack

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// jsonArgumentDescription is how the pinned snapshot marks an argument that
// carries a JSON document inside its `type: string`: "A JSON-based array…",
// "This must be a JSON-encoded string", "URL-encoded JSON map", "Key-value
// object of outputs", "stringified JSON format". The snapshot types every
// argument as a scalar, so the description is the only machine-readable signal.
var jsonArgumentDescription = regexp.MustCompile(`(?i)\bJSON\b|key-value`)

// sdkJSONArguments are the arguments an official SDK sends as a JSON array or
// object inside a JSON request body, with the shape it sends. They were read
// from python-slack-sdk 3.43's WebClient: every method that calls
// api_call(..., json=kwargs) and declares a Sequence/List/Dict parameter it
// does not comma-join. (@slack/web-api and the Java SDK form-encode, stringifying
// every non-scalar, so a structured argument reaches this server as a string
// from them.) A new SDK argument of this kind belongs here.
var sdkJSONArguments = map[string]string{
	"attachments":        `[{"text":"a"}]`,
	"blocks":             `[{"type":"divider"}]`,
	"cells":              `[{"column_id":"Col1"}]`,
	"changes":            `[{"operation":"insert_at_end"}]`,
	"channel_ids":        `["C1","C2"]`,
	"chunks":             `[{"type":"markdown_text","text":"a"}]`,
	"description_blocks": `[{"type":"rich_text"}]`,
	"dialog":             `{"title":"a"}`,
	"document_content":   `{"type":"markdown","markdown":"a"}`,
	"error":              `{"message":"a"}`,
	"ids":                `["Rec1","Rec2"]`,
	"initial_fields":     `[{"column_id":"Col1"}]`,
	"inputs":             `{"a":{"value":"b"}}`,
	"loading_messages":   `["Thinking…","Still thinking…"]`,
	"metadata":           `{"event_type":"a","event_payload":{}}`,
	"outputs":            `{"a":"b"}`,
	"profile":            `{"status_text":"a"}`,
	"prompts":            `[{"title":"a","message":"b"}]`,
	"schema":             `[{"key":"a","name":"A","type":"text"}]`,
	"unfurls":            `{"https://example.com":{"blocks":[]}}`,
	"user_auth_blocks":   `[{"type":"divider"}]`,
	"user_ids":           `["U1","U2"]`,
	"view":               `{"type":"modal"}`,
}

// Every argument the pinned snapshot describes as carrying JSON, and every
// argument an official SDK sends as JSON, must be one the decoder forwards as
// structure. A missed name answered invalid_array_arg (or invalid_arg_name) to
// the SDK's own request: prompts and loading_messages did exactly that for
// assistant.threads.setSuggestedPrompts and setStatus.
func TestStructuredFieldsCoverEveryJSONArgument(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "specs", "upstream", "slack-api-specs", "web-api", "slack_web_openapi_v2.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name        string `json:"name"`
				Description string `json:"description"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	fromSnapshot := map[string][]string{}
	for path, operations := range snapshot.Paths {
		for _, operation := range operations {
			for _, parameter := range operation.Parameters {
				if jsonArgumentDescription.MatchString(parameter.Description) {
					fromSnapshot[parameter.Name] = append(fromSnapshot[parameter.Name], path)
				}
			}
		}
	}
	if len(fromSnapshot) < 8 {
		t.Fatalf("only %d JSON-bearing arguments found in the snapshot; the scan is broken", len(fromSnapshot))
	}
	names := make([]string, 0, len(fromSnapshot))
	for name := range fromSnapshot {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !decodesStructure(name) {
			t.Errorf("%s is described as JSON by %v but the decoder flattens it", name, fromSnapshot[name])
		}
	}
	for name, shape := range sdkJSONArguments {
		if !decodesStructure(name) {
			t.Errorf("%s is sent as JSON by an official SDK but the decoder flattens it", name)
			continue
		}
		fields, err := decodeJSONFields(strings.NewReader(`{"`+name+`":`+shape+`}`), normalizeJSONField)
		if err != nil {
			t.Errorf("%s: the SDK's JSON body %s was refused: %v", name, shape, err)
			continue
		}
		if strings.TrimSpace(fields[name]) == "" {
			t.Errorf("%s: the SDK's JSON body %s decoded to nothing", name, shape)
		}
	}
}

// decodesStructure reports whether the decoder forwards a JSON value for the
// named argument rather than refusing it as a non-scalar.
func decodesStructure(name string) bool {
	return isStructuredField(name) || isListField(name) || name == "profile" || name == "error"
}

// A structured argument sent as a JSON-encoded string inside a JSON body — the
// form encoding's shape, and what python-slack-sdk sends when a caller passes
// a string — decodes to the same value as the form-encoded request. It used to
// be forwarded verbatim, quotes and all, and chat.postMessage answered no_text
// for a message with perfectly good blocks.
func TestStructuredArgumentsAcceptAJSONEncodedString(t *testing.T) {
	handler, _ := testHandlerWithStore()
	blocks := `[{"type":"section","text":{"type":"mrkdwn","text":"hi"}}]`
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	result := postJSON(handler, "/api/chat.postMessage", `{"channel":"C1","blocks":`+string(encoded)+`}`)
	if code := errorCode(t, result); code != "" {
		t.Fatalf("blocks as a JSON-encoded string answered %q: %s", code, result.Body)
	}
	if !strings.Contains(result.Body.String(), `"text":"hi"`) {
		t.Fatalf("the blocks were not stored: %s", result.Body)
	}
	for name, value := range map[string]string{"blocks": blocks, "profile": `{"status_text":"a"}`} {
		fromJSON, err := decodeJSONFields(strings.NewReader(`{"`+name+`":`+string(mustMarshal(t, value))+`}`), normalizeJSONField)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if fromJSON[name] != value {
			t.Errorf("%s decoded to %q, want the form-encoded twin %q", name, fromJSON[name], value)
		}
	}
}

// The exact JSON bodies python-slack-sdk sends for the assistant thread writes:
// prompts and loading_messages are arrays, not strings.
func TestAssistantThreadWritesAcceptTheSDKsJSONArrays(t *testing.T) {
	handler, _ := testHandlerWithStore()
	posted := postJSON(handler, "/api/chat.postMessage", `{"channel":"C1","text":"help"}`)
	var message struct {
		TS string `json:"ts"`
	}
	if err := json.Unmarshal(posted.Body.Bytes(), &message); err != nil || message.TS == "" {
		t.Fatalf("post=%s", posted.Body)
	}
	prompts := postJSON(handler, "/api/assistant.threads.setSuggestedPrompts",
		`{"channel_id":"C1","thread_ts":"`+message.TS+`","prompts":[{"title":"Summarize","message":"Summarize the thread"}]}`)
	if code := errorCode(t, prompts); code != "" {
		t.Fatalf("setSuggestedPrompts answered %q", code)
	}
	status := postJSON(handler, "/api/assistant.threads.setStatus",
		`{"channel_id":"C1","thread_ts":"`+message.TS+`","status":"is thinking...","loading_messages":["Reading the thread","Drafting"]}`)
	if code := errorCode(t, status); code != "" {
		t.Fatalf("setStatus answered %q", code)
	}
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
