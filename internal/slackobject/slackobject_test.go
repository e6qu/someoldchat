package slackobject

import (
	"encoding/json"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func TestParsePublicURLAcceptsWhatTheWebClientAccepts(t *testing.T) {
	for value, want := range map[string]string{
		"https://chat.example.com/":      "https://chat.example.com",
		"https://chat.example.com/base/": "https://chat.example.com/base",
		"http://127.0.0.1:8080":          "http://127.0.0.1:8080",
		"http://dev.localhost:8080":      "http://dev.localhost:8080",
	} {
		if got, err := ParsePublicURL(value); err != nil || got != want {
			t.Errorf("ParsePublicURL(%q) = %q, %v; want %q", value, got, err, want)
		}
	}
	for _, value := range []string{"", "chat.example.com", "http://chat.example.com", "https://user@chat.example.com", "https://chat.example.com/?a=1", "https://chat.example.com/#x", "ftp://chat.example.com"} {
		if _, err := ParsePublicURL(value); err == nil {
			t.Errorf("ParsePublicURL(%q) accepted", value)
		}
	}
}

func TestAbsoluteResolvesOnlyServerPaths(t *testing.T) {
	for _, test := range []struct{ origin, value, want string }{
		{"https://c.example", "/api/files/F1", "https://c.example/api/files/F1"},
		{"https://c.example", "https://images.example/a.png", "https://images.example/a.png"},
		{"https://c.example", "//evil.example/a.png", "//evil.example/a.png"},
		{"https://c.example", "", ""},
		{"", "/api/files/F1", "/api/files/F1"},
	} {
		if got := Absolute(test.origin, test.value); got != test.want {
			t.Errorf("Absolute(%q, %q) = %q, want %q", test.origin, test.value, got, test.want)
		}
	}
}

// A user object rendered without an origin — the journal's snapshot — and
// then resolved is the object rendered on the origin in the first place.
func TestAbsoluteUserEqualsRenderingOnTheOrigin(t *testing.T) {
	user := domain.User{ID: "U1", WorkspaceID: "T1", Name: "a", Profile: domain.UserProfile{Image24: "/users/T1/U1/photo/tok", Image72: "https://img.example/72.png"}}
	relative, err := json.Marshal(User("", user, false))
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := AbsoluteUser("https://c.example", relative)
	if err != nil {
		t.Fatal(err)
	}
	direct, err := json.Marshal(User("https://c.example", user, false))
	if err != nil {
		t.Fatal(err)
	}
	var left, right any
	if json.Unmarshal(resolved, &left) != nil || json.Unmarshal(direct, &right) != nil {
		t.Fatal("undecodable")
	}
	if a, b := mustJSON(t, left), mustJSON(t, right); a != b {
		t.Fatalf("resolved %s\n  direct %s", a, b)
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
