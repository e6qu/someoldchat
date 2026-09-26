package bearer

import "testing"

func TestTokenReadsTheBearerSchemeCaseInsensitively(t *testing.T) {
	for header, want := range map[string]string{
		"Bearer xoxb-1":     "xoxb-1",
		"bearer xoxb-1":     "xoxb-1",
		"BEARER xoxb-1":     "xoxb-1",
		"Bearer   xoxb-1  ": "xoxb-1",
		"Bearer\txoxb-1":    "xoxb-1",
		"  Bearer xoxb-1":   "xoxb-1",
	} {
		if got, ok := Token(header); !ok || got != want {
			t.Errorf("Token(%q) = %q, %v; want %q", header, got, ok, want)
		}
	}
	for _, header := range []string{"", "Bearer", "Bearer ", "Basic dXNlcjpwYXNz", "Bearerxoxb-1", "xoxb-1", "Token xoxb-1"} {
		if got, ok := Token(header); ok {
			t.Errorf("Token(%q) = %q; want no token", header, got)
		}
	}
}
