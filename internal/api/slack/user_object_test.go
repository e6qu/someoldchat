package slack

import (
	"bytes"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestUserObjectCarriesRolesBotIdentityAndFetchableImages(t *testing.T) {
	handler, _ := testHandlerWithStore()
	call := apiCaller(t, handler)

	if missing := call("users.info", url.Values{}); missing["error"] != "user_not_found" {
		t.Fatalf("users.info without user=%v", missing)
	}

	admin := call("users.info", url.Values{"user": {"U1"}})["user"].(map[string]any)
	requirePinnedFields(t, "users.info user", admin, pinnedDefinition(t, "objs_user", 0))
	profile := admin["profile"].(map[string]any)
	requirePinnedFields(t, "users.info profile", profile, pinnedDefinition(t, "objs_user_profile", 0))
	if admin["is_admin"] != true || admin["is_owner"] != false || admin["is_bot"] != false || admin["is_app_user"] != false ||
		admin["tz"] != "UTC" || admin["tz_offset"] != float64(0) || len(admin["color"].(string)) != 6 {
		t.Fatalf("admin user=%v", admin)
	}
	avatar, _ := profile["image_24"].(string)
	if !strings.HasPrefix(avatar, "http://example.com/") {
		t.Fatalf("image_24=%q, want an absolute URL on the serving origin", avatar)
	}
	for _, size := range []string{"32", "48", "72", "192", "512", "1024"} {
		if value, _ := profile["image_"+size].(string); !strings.HasPrefix(value, "http://example.com/") {
			t.Fatalf("image_%s=%q", size, value)
		}
	}

	// The default avatar is served where the profile says it is, at that size.
	request := httptest.NewRequest(http.MethodGet, strings.TrimPrefix(avatar, "http://example.com"), nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("default avatar status=%d headers=%v", response.Code, response.Header())
	}
	decoded, err := png.Decode(bytes.NewReader(response.Body.Bytes()))
	if err != nil || decoded.Bounds().Dx() != 24 || decoded.Bounds().Dy() != 24 {
		t.Fatalf("default avatar decoded=%v err=%v", decoded, err)
	}
	for _, path := range []string{"/avatars/T1/U1/25.png", "/avatars/T1/U1/24.svg"} {
		missing := httptest.NewRecorder()
		handler.ServeHTTP(missing, httptest.NewRequest(http.MethodGet, path, nil))
		if missing.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d", path, missing.Code)
		}
	}

	// U2 is the fixture app's bot user.
	bot := call("users.info", url.Values{"user": {"U2"}})["user"].(map[string]any)
	botProfile := bot["profile"].(map[string]any)
	if bot["is_bot"] != true || bot["is_admin"] != false || botProfile["bot_id"] != "B1" || botProfile["api_app_id"] != "A1" {
		t.Fatalf("bot user=%v", bot)
	}

	// A profile change moves updated.
	if set := call("users.profile.set", url.Values{"profile": {`{"display_name":"alice2"}`}}); set["ok"] != true {
		t.Fatalf("users.profile.set=%v", set)
	}
	changed := call("users.info", url.Values{"user": {"U1"}})["user"].(map[string]any)
	if updated, _ := changed["updated"].(float64); updated <= 0 {
		t.Fatalf("updated after a profile change=%v", changed["updated"])
	}

	members := call("users.list", url.Values{})["members"].([]any)
	for _, value := range members {
		member := value.(map[string]any)
		if member["id"] == "U2" && member["is_bot"] != true {
			t.Fatalf("users.list reports the bot user as a person: %v", member)
		}
	}
}
