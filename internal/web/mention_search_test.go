package web

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// A workspace larger than the directory a page carries is searched as the
// member types a mention: the page says where to ask, and the answer names
// the people the typed name matches, saying who is in the conversation.
func TestMentionsSearchAWorkspaceLargerThanThePage(t *testing.T) {
	s, mux := browserWorkspace(t, auth.AllScopes())
	if body := get(t, mux, "/app?channel=Cdev").Body.String(); indexOf(body, "data-people-search=") >= 0 {
		t.Fatal("a small workspace's page asked to search: its directory is whole")
	}
	for index := 0; index < composerPeopleLimit+5; index++ {
		s.SeedUser(domain.User{ID: domain.UserID(fmt.Sprintf("Ufill%04d", index)), WorkspaceID: "T1", Name: fmt.Sprintf("filler%04d", index)})
	}
	s.SeedUser(domain.User{ID: "Uzed", WorkspaceID: "T1", Name: "zed", RealName: "Zed Zebra"})
	s.SeedUser(domain.User{ID: "Uzoe", WorkspaceID: "T1", Name: "zoe", RealName: "Zoe Zephyr"})
	s.SeedConversationMember("Cdev", "Uzoe")
	requireContains(t, "a large workspace's page", get(t, mux, "/app?channel=Cdev").Body.String(), `data-people-search="/app/mentions?channel=Cdev"`)

	var people []mentionPersonView
	response := get(t, mux, "/app/mentions?channel=Cdev&q=ze")
	if err := json.Unmarshal(response.Body.Bytes(), &people); err != nil || response.Code != 200 {
		t.Fatalf("status=%d body=%s err=%v", response.Code, response.Body, err)
	}
	byID := map[string]mentionPersonView{}
	for _, person := range people {
		byID[person.ID] = person
	}
	if zed, ok := byID["Uzed"]; !ok || zed.Member || zed.Unknown || zed.Name != "Zed Zebra" {
		t.Fatalf("zed=%+v in %+v, want found and not in the channel", zed, people)
	}
	if zoe, ok := byID["Uzoe"]; !ok || !zoe.Member {
		t.Fatalf("zoe=%+v in %+v, want found and in the channel", zoe, people)
	}
	if _, ok := byID[string(domain.SlackbotUserID)]; ok {
		t.Fatal("Slackbot was offered to mention into a channel it cannot join")
	}
	if body := get(t, mux, "/app/mentions?channel=Cdev&q=").Body.String(); body != "[]\n" {
		t.Fatalf("an empty query answered %q", body)
	}
	people = nil
	if err := json.Unmarshal(get(t, mux, "/app/mentions?q=zed").Body.Bytes(), &people); err != nil || len(people) != 1 || !people[0].Unknown {
		t.Fatalf("without a conversation=%+v err=%v, want membership left unsaid", people, err)
	}
}
