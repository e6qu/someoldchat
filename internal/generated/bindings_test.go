package generated

import (
	"testing"

	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestLocalBindingUsesDirectServiceImplementation(t *testing.T) {
	local := ProvideChatServiceLocal(memory.New(), blob.Disabled{}, []byte("0123456789abcdef0123456789abcdef"))
	messages, ok := local.(service.Messages)
	if !ok {
		t.Fatalf("local binding type=%T, want service.Messages", local)
	}
	if string(messages.AppCredentialKey) != "0123456789abcdef0123456789abcdef" {
		t.Fatal("local binding dropped the application credential key")
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
	if profile.Processes["http"].Replicas != 4 || profile.Processes["chat"].Replicas != 3 {
		t.Fatalf("processes=%+v", profile.Processes)
	}
}

// TestRemoteBindingReturnsOneHandleForEveryRole covers the invariant the four
// return values are supposed to carry: in the split deployment the chat service,
// the token store, the session store and the session revoker must all be the same
// remote module. Nothing type-checks that, and a wiring mistake that resolved
// session reads against one process and session writes against another would
// compile.
func TestRemoteBindingReturnsOneHandleForEveryRole(t *testing.T) {
	connection, err := grpc.NewClient("passthrough:///chat", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	chat, tokens, sessions, revoker, err := ProvideChatServiceRemote(connection)
	if err != nil {
		t.Fatal(err)
	}
	if chat == nil || tokens == nil || sessions == nil || revoker == nil {
		t.Fatal("remote binding returned a nil handle")
	}
	if any(tokens) != any(chat) || any(sessions) != any(chat) || any(revoker) != any(chat) {
		t.Fatalf("remote binding handed out different handles: chat=%#v tokens=%#v sessions=%#v revoker=%#v", chat, tokens, sessions, revoker)
	}
}

func TestRemoteBindingRejectsAMissingConnection(t *testing.T) {
	if _, _, _, _, err := ProvideChatServiceRemote(nil); err == nil {
		t.Fatal("remote binding accepted a nil connection")
	}
}
