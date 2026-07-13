package generated

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
)

func TestLocalBindingUsesDirectServiceImplementation(t *testing.T) {
	local := ProvideChatServiceLocal(memory.New(), blob.Disabled{})
	if _, ok := local.(service.Messages); !ok {
		t.Fatalf("local binding type=%T, want service.Messages", local)
	}
}

func TestTargetProfilesExposeExplicitReplicaTopology(t *testing.T) {
	profile, ok := TargetProfiles["separate-chat-replicated"]
	if !ok {
		t.Fatal("replicated separate target was not generated")
	}
	if profile.Mode != "separate" || profile.Storage != "dqlite" {
		t.Fatalf("profile=%+v", profile)
	}
	if profile.Processes["http"].Replicas != 3 || profile.Processes["chat"].Replicas != 2 {
		t.Fatalf("processes=%+v", profile.Processes)
	}
}
