package qualification

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// visibleFilesAreNewestFirst pins the order files.list pages in: created_at
// DESC, then id DESC for files created in the same instant, with a cursor that
// resumes exactly where the previous page stopped on every profile.
func visibleFilesAreNewestFirst(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	base := time.Unix(1_700_000_000, 0).UTC()
	create := func(name string, createdAt time.Time) domain.FileID {
		t.Helper()
		file := domain.File{
			ID: domain.FileID("F-" + name + "-" + f.suffix), WorkspaceID: f.workspaceID, Uploader: f.userID,
			Name: name + ".txt", Title: name, MIMEType: "text/plain", BlobKey: "blob-" + name + "-" + f.suffix, Size: 1, CreatedAt: createdAt,
		}
		if err := f.repository.CreateFile(ctx, file, f.event("file-"+name, "file.created", string(file.ID))); err != nil {
			t.Fatal(err)
		}
		return file.ID
	}
	oldest := create("a", base)
	tiedLow := create("b", base.Add(time.Second))
	tiedHigh := create("c", base.Add(time.Second))
	newest := create("d", base.Add(2*time.Second))
	want := []domain.FileID{newest, tiedHigh, tiedLow, oldest}

	var got []domain.FileID
	request := domain.PageRequest{Limit: 2}
	for pages := 0; pages < 4; pages++ {
		page, err := f.repository.ListVisibleFiles(ctx, f.workspaceID, f.userID, request)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range page.Files {
			got = append(got, file.ID)
		}
		if !page.HasMore {
			break
		}
		request.Cursor = page.NextCursor
	}
	if len(got) != len(want) {
		t.Fatalf("files=%v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("files=%v, want %v", got, want)
		}
	}
}

// oauthInstallsReuseTheirBotAndRedeemEveryGrantShape pins the install rules of
// every profile: a reinstall grants to the bot the app already has in the
// workspace, a user-scope-only grant redeems for the installer's user token, a
// redirect_uri is required at redemption only when the authorization named
// one, and a bot token's installer survives oauth.v2.exchange.
func oauthInstallsReuseTheirBotAndRedeemEveryGrantShape(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	now := time.Unix(1_700_000_000, 0).UTC()
	appID := domain.AppID("A-install-" + f.suffix)
	clientID := "client-install-" + f.suffix
	app := domain.App{
		ID: appID, DevelopmentWorkspaceID: f.workspaceID, OwnerID: f.userID, Name: "Installer",
		ClientID: clientID, SigningSecretHash: "hash", SigningSecretCiphertext: "ciphertext",
		VerificationTokenHash: "hash", VerificationTokenCiphertext: "ciphertext",
		ManifestVersion: 1, Distribution: "private", CreatedAt: now, UpdatedAt: now,
	}
	revision := domain.AppManifestRevision{AppID: appID, Version: 1, Manifest: `{"display_information":{"name":"Installer"}}`, CreatedBy: f.userID, CreatedAt: now}
	if err := f.repository.CreateApp(ctx, app, revision, domain.OAuthClient{ID: clientID, SecretHash: domain.HashToken("secret"), AppID: appID}); err != nil {
		t.Fatal(err)
	}
	authorize := func(name, redirect string, botScopes, userScopes []string) domain.OAuthCode {
		t.Helper()
		botUser := domain.User{}
		bot := domain.Bot{}
		grant := domain.OAuthCode{
			Code: "code-" + name + "-" + f.suffix, ClientID: clientID, WorkspaceID: f.workspaceID, UserID: f.userID,
			Scopes: append(append([]string(nil), botScopes...), userScopes...), BotScopes: botScopes, UserScopes: userScopes, RedirectURI: redirect,
		}
		if len(botScopes) != 0 {
			botUser = domain.User{ID: domain.UserID("UB-" + name + "-" + f.suffix), WorkspaceID: f.workspaceID, Name: "installer", RealName: "Installer"}
			bot = domain.Bot{ID: domain.BotID("B-" + name + "-" + f.suffix), WorkspaceID: f.workspaceID, AppID: appID, UserID: botUser.ID, Name: "Installer", UpdatedAt: now}
			grant.BotID, grant.BotUserID = bot.ID, botUser.ID
		}
		granted, err := f.repository.CreateOAuthAuthorization(ctx, botUser, bot, grant)
		if err != nil {
			t.Fatalf("authorize %s: %v", name, err)
		}
		return granted
	}

	first := authorize("first", "", []string{"chat:write"}, nil)
	second := authorize("second", "https://app.example/callback", []string{"chat:write"}, []string{"search:read"})
	if second.BotID != first.BotID || second.BotUserID != first.BotUserID {
		t.Fatalf("reinstall bot=%s/%s, want the first install's %s/%s", second.BotID, second.BotUserID, first.BotID, first.BotUserID)
	}
	if _, err := f.repository.GetBot(ctx, f.workspaceID, domain.BotID("B-second-"+f.suffix)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a reinstall created a second bot: err=%v", err)
	}

	// An authorization that named no redirect_uri redeems without one.
	token, err := f.repository.ExchangeOAuthCode(ctx, clientID, "secret", first.Code, "", "xoxb-first-"+f.suffix, domain.OAuthToken{TokenType: domain.TokenBot})
	if err != nil {
		t.Fatalf("undirected redemption: %v", err)
	}
	if token.BotID != first.BotID || token.UserID != first.BotUserID || token.InstallerID != f.userID || token.WorkspaceName != "Divergence" {
		t.Fatalf("undirected token=%+v", token)
	}
	// One that named it must name it again, and a mismatch spends nothing.
	directed := domain.OAuthToken{TokenType: domain.TokenBot, AuthedUserAccessToken: "xoxp-second-" + f.suffix}
	if _, err := f.repository.ExchangeOAuthCode(ctx, clientID, "secret", second.Code, "https://elsewhere.example/", "xoxb-second-"+f.suffix, directed); !errors.Is(err, store.ErrOAuthRedirectMismatch) {
		t.Fatalf("wrong redirect err=%v, want ErrOAuthRedirectMismatch", err)
	}
	if _, err := f.repository.ExchangeOAuthCode(ctx, clientID, "secret", second.Code, "https://app.example/callback", "xoxb-second-"+f.suffix, directed); err != nil {
		t.Fatalf("directed redemption: %v", err)
	}

	// A user-scope-only grant redeems for the installer's user token.
	userOnly := authorize("user", "", nil, []string{"search:read"})
	issued, err := f.repository.ExchangeOAuthCode(ctx, clientID, "secret", userOnly.Code, "", "xoxb-user-"+f.suffix, domain.OAuthToken{TokenType: domain.TokenBot, AuthedUserAccessToken: "xoxp-user-" + f.suffix})
	if err != nil {
		t.Fatalf("user-only redemption: %v", err)
	}
	if issued.TokenType != domain.TokenUser || issued.AccessToken != "xoxp-user-"+f.suffix || issued.UserID != f.userID || issued.BotID != "" || issued.AuthedUserAccessToken != "" {
		t.Fatalf("user-only token=%+v", issued)
	}
	record, err := f.repository.LookupToken(ctx, "xoxp-user-"+f.suffix)
	if err != nil || record.TokenType != domain.TokenUser || record.UserID != f.userID {
		t.Fatalf("user-only record=%+v err=%v", record, err)
	}
	if _, err := f.repository.LookupToken(ctx, "xoxb-user-"+f.suffix); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a user-only grant minted a bot token: err=%v", err)
	}

	// oauth.v2.exchange keeps the member who installed the bot.
	exchanged, err := f.repository.ExchangeOAuthAccessToken(ctx, clientID, "secret", "xoxb-first-"+f.suffix, "xoxe.xoxb-next-"+f.suffix, "xoxe-refresh-"+f.suffix, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if exchanged.InstallerID != f.userID || exchanged.UserID != first.BotUserID || exchanged.WorkspaceName != "Divergence" {
		t.Fatalf("exchanged token=%+v", exchanged)
	}
}

// fileSharesNameTheirCarryingMessages pins files.info's shares read: one entry
// per live message that carries the file, oldest first, with the
// conversation's name and privacy, on every profile.
func fileSharesNameTheirCarryingMessages(t *testing.T, open opener) {
	ctx := context.Background()
	f, closeRepository := newFixture(t, ctx, open)
	defer closeRepository()

	file := domain.File{
		ID: domain.FileID("F-shares-" + f.suffix), WorkspaceID: f.workspaceID, Uploader: f.userID,
		Name: "plan.txt", Title: "Plan", MIMEType: "text/plain", BlobKey: string(f.workspaceID) + "/plan", Size: 4,
		CreatedAt: time.Unix(1_700_000_500, 0).UTC(),
	}
	if err := f.repository.CreateFile(ctx, file, f.event("shares-file", "file.created", string(file.ID))); err != nil {
		t.Fatal(err)
	}
	share := func(name string, at int64, thread domain.MessageTimestamp) domain.Message {
		t.Helper()
		message := domain.Message{
			ID: domain.MessageID("M-" + name + "-" + f.suffix), WorkspaceID: f.workspaceID, Conversation: f.channelID,
			AuthorID: f.userID, Text: name, Attachments: "[]", ThreadTimestamp: thread,
			CreatedAt: domain.MessageInstant(time.Unix(at, 0).UTC()),
		}
		if err := f.repository.CreateFileShareMessage(ctx, []domain.FileID{file.ID}, message,
			[]events.Event{f.event("shares-"+name, "message.created", string(message.ID))}); err != nil {
			t.Fatal(err)
		}
		return message
	}
	first := share("first", 1_700_000_501, "")
	reply := share("reply", 1_700_000_502, domain.NewMessageTimestamp(first.CreatedAt))
	shares, err := f.repository.ListFileShares(ctx, file.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(shares) != 2 {
		t.Fatalf("shares=%+v, want two", shares)
	}
	want := []domain.FileShare{
		{Conversation: f.channelID, ConversationName: "divergence", Timestamp: domain.NewMessageTimestamp(first.CreatedAt), SharedBy: f.userID},
		{Conversation: f.channelID, ConversationName: "divergence", Timestamp: domain.NewMessageTimestamp(reply.CreatedAt), ThreadTimestamp: reply.ThreadTimestamp, SharedBy: f.userID},
	}
	for index := range want {
		if !reflect.DeepEqual(shares[index], want[index]) {
			t.Fatalf("shares[%d]=%+v, want %+v", index, shares[index], want[index])
		}
	}
}
