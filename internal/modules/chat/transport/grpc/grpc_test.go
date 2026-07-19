package grpc_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	chatgrpc "github.com/sameoldchat/sameoldchat/internal/modules/chat/transport/grpc"
	"github.com/sameoldchat/sameoldchat/internal/service"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestRemoteRequiresMutualTLS(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:"}})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	local := service.Messages{Store: store}
	caCertificate, caKey := testCA(t)
	serverCertificate := testLeafCertificate(t, caCertificate, caKey, "chatd.test", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	clientCertificate := testLeafCertificate(t, caCertificate, caKey, "http.test", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCertificate) {
		t.Fatal("failed to build CA pool")
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{serverCertificate}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: caPool, MinVersion: tls.VersionTLS13})))
	if err := chatgrpc.RegisterServer(server, local, store, store, store); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	secureConn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{Certificates: []tls.Certificate{clientCertificate}, RootCAs: caPool, ServerName: "chatd.test", MinVersion: tls.VersionTLS13})))
	if err != nil {
		t.Fatal(err)
	}
	defer secureConn.Close()
	remote, err := chatgrpc.NewRemote(secureConn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.Post(ctx, "T1", "U1", "C1", "mutual TLS", "", ""); err != nil {
		t.Fatalf("valid client certificate rejected: %v", err)
	}

	noCertificateConn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: caPool, ServerName: "chatd.test", MinVersion: tls.VersionTLS13})))
	if err != nil {
		t.Fatal(err)
	}
	defer noCertificateConn.Close()
	unauthenticated, err := chatgrpc.NewRemote(noCertificateConn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unauthenticated.Post(ctx, "T1", "U1", "C1", "should fail", "", ""); err == nil {
		t.Fatal("client without certificate was accepted")
	}
}

func testCA(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "SameOldChat Test CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key
}

func testLeafCertificate(t *testing.T, parentCertificatePEM []byte, parentKey *ecdsa.PrivateKey, commonName string, usages []x509.ExtKeyUsage) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(parentCertificatePEM)
	if block == nil {
		t.Fatal("invalid parent certificate")
	}
	parent, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: commonName}, DNSNames: []string{commonName}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	privateKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privateKeyPEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func TestRemoteStreamsFileAndUsesMetadataMethods(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"})
	blobs, err := blob.NewFilesystem(t.TempDir(), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	local := service.Messages{Store: store, Blob: blobs}
	server := grpc.NewServer()
	if err := chatgrpc.RegisterServer(server, local, store, store, store); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	remote, err := chatgrpc.NewRemote(conn)
	if err != nil {
		t.Fatal(err)
	}
	content := bytes.Repeat([]byte("file-content-"), 10000)
	file, err := remote.UploadFile(ctx, "T1", "U1", "notes.txt", "Notes", "text/plain", int64(len(content)), bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	if file.ID == "" || file.Size != int64(len(content)) || file.Name != "notes.txt" {
		t.Fatalf("uploaded file = %+v", file)
	}
	info, err := remote.FileInfo(ctx, "T1", "U1", file.ID)
	if err != nil || info.ID != file.ID {
		t.Fatalf("file info = %+v, err = %v", info, err)
	}
	opened, source, err := remote.OpenFile(ctx, "T1", "U1", file.ID)
	if err != nil {
		t.Fatal(err)
	}
	readBack, err := io.ReadAll(source)
	if closeErr := source.Close(); err != nil || closeErr != nil || opened.ID != file.ID || !bytes.Equal(readBack, content) {
		t.Fatalf("downloaded file=%+v bytes=%d readErr=%v closeErr=%v", opened, len(readBack), err, closeErr)
	}
	page, err := remote.Files(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Files) != 1 || page.Files[0].ID != file.ID {
		t.Fatalf("file page = %+v, err = %v", page, err)
	}
	if err := remote.DeleteFile(ctx, "T1", "U1", file.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := remote.FileInfo(ctx, "T1", "U1", file.ID); err == nil {
		t.Fatal("deleted file remained visible")
	}
}

func TestRemoteUsesSameChatContract(t *testing.T) {
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T1", Name: "test"})
	store.SeedUser(domain.User{ID: "U1", WorkspaceID: "T1", Email: "alice@example.com", Name: "alice", Profile: domain.UserProfile{DisplayName: "alice", StatusText: "Available", StatusEmoji: ":wave:"}})
	store.SeedUser(domain.User{ID: "U2", WorkspaceID: "T1", Name: "bob"})
	store.SeedConversation(domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"})
	store.SeedConversationMember("C1", "U2")
	store.SeedToken(context.Background(), "api-token", domain.TokenRecord{WorkspaceID: "T1", UserID: "U1", Scopes: []string{"chat:write"}})
	if err := store.SeedSession(context.Background(), "session-token", domain.SessionRecord{WorkspaceID: "T1", UserID: "U1", Scopes: auth.AllScopes(), ExpiresAt: time.Now().UTC().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	local := service.Messages{Store: store}
	server := grpc.NewServer()
	if err := chatgrpc.RegisterServer(server, local, store, store, store); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()

	ctx := context.Background()
	conn, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	remote, err := chatgrpc.NewRemote(conn)
	if err != nil {
		t.Fatal(err)
	}
	createdUser, err := remote.AdminCreateUser(ctx, "T1", "U1", "new@example.com", "New User", domain.WorkspaceRoleMember)
	if err != nil || createdUser.Email != "new@example.com" || createdUser.RealName != "New User" {
		t.Fatalf("created user=%+v err=%v", createdUser, err)
	}
	if _, err := remote.AdminCreateUser(ctx, "T1", "U1", "NEW@example.com", "Duplicate", domain.WorkspaceRoleMember); err == nil {
		t.Fatal("duplicate manual user was accepted")
	}
	adminUsers, err := remote.AdminListUsers(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(adminUsers.Users) != 3 {
		t.Fatalf("admin users=%+v err=%v", adminUsers, err)
	}
	if _, err := remote.AdminListUsers(ctx, "", "U1", domain.PageRequest{Limit: 10}); err == nil {
		t.Fatal("administrator user listing accepted an empty workspace")
	}
	for _, item := range adminUsers.Users {
		if item.User.ID == createdUser.ID && (item.Membership.Role != domain.WorkspaceRoleMember || !item.Membership.Active) {
			t.Fatalf("created administrator user state=%+v", item)
		}
	}
	tokenStore := auth.TokenStore(remote)
	token, err := tokenStore.LookupToken(ctx, "api-token")
	if err != nil || token.UserID != "U1" || len(token.Scopes) != 1 || token.Scopes[0] != "chat:write" {
		t.Fatalf("token=%+v err=%v", token, err)
	}
	if err := remote.RevokeToken(ctx, "api-token"); err != nil {
		t.Fatal(err)
	}
	token, err = tokenStore.LookupToken(ctx, "api-token")
	if err != nil || !token.Revoked {
		t.Fatalf("revoked token=%+v err=%v", token, err)
	}
	sessionStore := auth.SessionStore(remote)
	session, err := sessionStore.LookupSession(ctx, "session-token")
	if err != nil || session.UserID != "U1" || session.ExpiresAt.IsZero() {
		t.Fatalf("session=%+v err=%v", session, err)
	}
	if err := remote.RevokeSession(ctx, "session-token"); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	session, err = sessionStore.LookupSession(ctx, "session-token")
	if err != nil || !session.Revoked {
		t.Fatalf("revoked session=%+v err=%v", session, err)
	}
	shortContext, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if _, err := remote.UploadFile(shortContext, "T1", "U1", "missing.txt", "Missing", "text/plain", 1, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("upload without blob storage unexpectedly succeeded")
	}
	message, err := remote.Post(ctx, "T1", "U1", "C1", "hello", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if message.Text != "hello" {
		t.Fatalf("message = %+v", message)
	}
	permalink, err := remote.Permalink(ctx, "T1", "U1", "C1", domain.NewMessageTimestamp(message.CreatedAt))
	if err != nil || !strings.Contains(permalink, "/archives/C1/p") {
		t.Fatalf("permalink=%q err=%v", permalink, err)
	}
	retried, err := remote.Post(ctx, "T1", "U1", "C1", "different retry", "", "request-1")
	if err != nil || retried.ID == message.ID {
		// The first request did not carry an idempotency key; this establishes
		// the keyed request independently below.
		t.Fatalf("unexpected unkeyed retry result=%+v err=%v", retried, err)
	}
	keyed, err := remote.Post(ctx, "T1", "U1", "C1", "keyed", "", "request-2")
	if err != nil {
		t.Fatal(err)
	}
	keyedRetry, err := remote.Post(ctx, "T1", "U1", "C1", "different keyed retry", "", "request-2")
	if err != nil || keyedRetry.ID != keyed.ID || keyedRetry.Text != "keyed" {
		t.Fatalf("keyed=%+v retry=%+v err=%v", keyed, keyedRetry, err)
	}
	timestamp := domain.NewMessageTimestamp(message.CreatedAt)
	updated, err := remote.Update(ctx, "T1", "U1", "C1", timestamp, "updated")
	if err != nil || updated.Text != "updated" {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	deleted, err := remote.Delete(ctx, "T1", "U1", "C1", timestamp)
	if err != nil || !deleted.Deleted {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	conversation, err := remote.ConversationInfo(ctx, "T1", "U1", "C1")
	if err != nil || conversation.Name != "general" {
		t.Fatalf("conversation=%+v err=%v", conversation, err)
	}
	rename, err := remote.RenameConversation(ctx, "T1", "U1", "C1", "renamed room")
	if err != nil || rename.Name != "renamed-room" {
		t.Fatalf("renamed conversation=%+v err=%v", rename, err)
	}
	topic, err := remote.SetConversationTopic(ctx, "T1", "U1", "C1", "project discussion")
	if err != nil || topic.Topic != "project discussion" {
		t.Fatalf("topic conversation=%+v err=%v", topic, err)
	}
	purpose, err := remote.SetConversationPurpose(ctx, "T1", "U1", "C1", "for planning")
	if err != nil || purpose.Purpose != "for planning" {
		t.Fatalf("purpose conversation=%+v err=%v", purpose, err)
	}
	archived, err := remote.SetConversationArchived(ctx, "T1", "U1", "C1", true)
	if err != nil || !archived.Archived {
		t.Fatalf("archived conversation=%+v err=%v", archived, err)
	}
	unarchived, err := remote.SetConversationArchived(ctx, "T1", "U1", "C1", false)
	if err != nil || unarchived.Archived {
		t.Fatalf("unarchived conversation=%+v err=%v", unarchived, err)
	}
	user, err := remote.UserInfo(ctx, "T1", "U1", "U1")
	if err != nil || user.ID != "U1" || user.Profile.DisplayName != "alice" || user.Profile.StatusEmoji != ":wave:" {
		t.Fatalf("user=%+v err=%v", user, err)
	}
	user, err = remote.UserByEmail(ctx, "T1", "U1", "ALICE@EXAMPLE.COM")
	if err != nil || user.ID != "U1" || user.Email != "alice@example.com" {
		t.Fatalf("user by email=%+v err=%v", user, err)
	}
	user, err = remote.SetUserProfile(ctx, "T1", "U1", domain.UserProfile{DisplayName: "remote-alice", StatusText: "Ready", StatusEmoji: ":white_check_mark:"})
	if err != nil || user.Profile.DisplayName != "remote-alice" || user.Profile.StatusText != "Ready" {
		t.Fatalf("updated user=%+v err=%v", user, err)
	}
	user, err = remote.SetUserPresence(ctx, "T1", "U1", domain.PresenceAway)
	if err != nil || user.Presence != domain.PresenceAway {
		t.Fatalf("updated presence user=%+v err=%v", user, err)
	}
	dnd, err := remote.DoNotDisturbInfo(ctx, "T1", "U1", "")
	if err != nil || dnd.Enabled || dnd.SnoozeEnabled(time.Now().UTC()) {
		t.Fatalf("initial dnd=%+v err=%v", dnd, err)
	}
	dnd, err = remote.SetSnooze(ctx, "T1", "U1", 5)
	if err != nil || !dnd.SnoozeEnabled(time.Now().UTC()) {
		t.Fatalf("snoozed dnd=%+v err=%v", dnd, err)
	}
	dnd, err = remote.EndSnooze(ctx, "T1", "U1")
	if err != nil || dnd.SnoozeEnabled(time.Now().UTC()) {
		t.Fatalf("ended snooze dnd=%+v err=%v", dnd, err)
	}
	if err := remote.EndDND(ctx, "T1", "U1"); err != nil {
		t.Fatalf("end dnd: %v", err)
	}
	starTimestamp := domain.NewMessageTimestamp(keyed.CreatedAt)
	if err := remote.AddStar(ctx, "T1", "U1", "C1", starTimestamp); err != nil {
		t.Fatalf("add star: %v", err)
	}
	stars, _, more, err := remote.Stars(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(stars) != 1 || stars[0].Message.ID == "" || more {
		t.Fatalf("stars=%+v more=%v err=%v", stars, more, err)
	}
	if err := remote.RemoveStar(ctx, "T1", "U1", "C1", starTimestamp); err != nil {
		t.Fatalf("remove star: %v", err)
	}
	reminder, err := remote.AddReminder(ctx, "T1", "U1", "", "remote reminder", time.Now().UTC().Add(time.Hour))
	if err != nil || reminder.ID == "" || reminder.Text != "remote reminder" {
		t.Fatalf("reminder=%+v err=%v", reminder, err)
	}
	reminders, err := remote.Reminders(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(reminders.Reminders) != 1 || reminders.Reminders[0].ID != reminder.ID || reminders.HasMore {
		t.Fatalf("reminders=%+v err=%v", reminders, err)
	}
	if err := remote.CompleteReminder(ctx, "T1", "U1", reminder.ID); err != nil {
		t.Fatalf("complete reminder: %v", err)
	}
	if err := remote.DeleteReminder(ctx, "T1", "U1", reminder.ID); err != nil {
		t.Fatalf("delete reminder: %v", err)
	}
	scheduled, err := remote.ScheduleMessage(ctx, "T1", "U1", "C1", "scheduled remotely", time.Now().UTC().Add(time.Hour))
	if err != nil || scheduled.ID == "" || scheduled.Text != "scheduled remotely" {
		t.Fatalf("scheduled=%+v err=%v", scheduled, err)
	}
	scheduledPage, err := remote.ScheduledMessages(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(scheduledPage.Items) != 1 || scheduledPage.Items[0].ID != scheduled.ID {
		t.Fatalf("scheduled page=%+v err=%v", scheduledPage, err)
	}
	if err := remote.DeleteScheduledMessage(ctx, "T1", "U1", "C1", scheduled.ID); err != nil {
		t.Fatalf("delete scheduled message: %v", err)
	}
	direct, err := remote.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil || !direct.IsDirect || direct.IsGroupDirect {
		t.Fatalf("direct=%+v err=%v", direct, err)
	}
	reused, err := remote.OpenConversation(ctx, "T1", "U1", []domain.UserID{"U2"})
	if err != nil || reused.ID != direct.ID {
		t.Fatalf("reused=%+v direct=%+v err=%v", reused, direct, err)
	}
	users, err := remote.Users(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || len(users.Users) != 3 || !containsUser(users.Users, "U1") || !containsUser(users.Users, "U2") || !containsUser(users.Users, createdUser.ID) {
		t.Fatalf("users=%+v err=%v", users, err)
	}
	workspace, err := remote.WorkspaceInfo(ctx, "T1", "U1")
	if err != nil || workspace.ID != "T1" || workspace.Name != "test" {
		t.Fatalf("workspace=%+v err=%v", workspace, err)
	}
	conversations, err := remote.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(conversations.Conversations) != 2 || !containsConversation(conversations.Conversations, "C1") {
		t.Fatalf("conversations=%+v err=%v", conversations, err)
	}
	publicConversations, err := remote.Conversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10, Types: []domain.ConversationType{domain.ConversationTypePublic}})
	if err != nil || len(publicConversations.Conversations) != 1 || !containsConversation(publicConversations.Conversations, "C1") {
		t.Fatalf("public conversations=%+v err=%v", publicConversations, err)
	}
	createdConversation, err := remote.CreateConversation(ctx, "T1", "U1", "private-room", true)
	if err != nil || !createdConversation.IsPrivate || createdConversation.Name != "private-room" {
		t.Fatalf("created conversation=%+v err=%v", createdConversation, err)
	}
	joined, err := remote.JoinConversation(ctx, "T1", "U1", "C1")
	if err != nil || joined.ID != "C1" {
		t.Fatalf("joined conversation=%+v err=%v", joined, err)
	}
	members, err := remote.ConversationMembers(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(members.Users) != 2 || members.Users[0].ID != "U1" {
		t.Fatalf("conversation members=%+v err=%v", members, err)
	}
	read, err := remote.MarkRead(ctx, "T1", "U1", "C1", timestamp)
	if err != nil || read.LastRead != timestamp || read.Conversation != "C1" {
		t.Fatalf("read cursor=%+v err=%v", read, err)
	}
	if err := remote.AddReaction(ctx, "T1", "U1", "C1", timestamp, "thumbsup"); err != nil {
		t.Fatal(err)
	}
	userReactions, err := remote.UserReactions(ctx, "T1", "U1", domain.PageRequest{Limit: 10})
	if err != nil || userReactions.HasMore || len(userReactions.Items) != 1 || userReactions.Items[0].Message.ID != message.ID {
		t.Fatalf("user reactions=%+v err=%v", userReactions, err)
	}
	reactions, _, more, err := remote.Reactions(ctx, "T1", "U1", "C1", timestamp, domain.PageRequest{Limit: 10})
	if err != nil || more || len(reactions) != 1 || reactions[0].Name != "thumbsup" {
		t.Fatalf("reactions=%+v more=%t err=%v", reactions, more, err)
	}
	if err := remote.RemoveReaction(ctx, "T1", "U1", "C1", timestamp, "thumbsup"); err != nil {
		t.Fatal(err)
	}
	if err := remote.AddPin(ctx, "T1", "U1", "C1", timestamp); err != nil {
		t.Fatal(err)
	}
	pins, _, more, err := remote.Pins(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || more || len(pins) != 1 || pins[0].Message != message.ID {
		t.Fatalf("pins=%+v more=%t err=%v", pins, more, err)
	}
	if err := remote.RemovePin(ctx, "T1", "U1", "C1", timestamp); err != nil {
		t.Fatal(err)
	}
	if err := remote.KickConversationMember(ctx, "T1", "U1", "C1", "U2"); err != nil {
		t.Fatalf("kick member: %v", err)
	}
	if _, err := remote.InviteConversationMembers(ctx, "T1", "U1", "C1", []domain.UserID{"U2", "U2"}); err != nil {
		t.Fatalf("invite member: %v", err)
	}
	search, err := remote.Search(ctx, "T1", "U1", "keyed", domain.PageRequest{Limit: 10})
	if err != nil || len(search.Messages) != 1 || search.Messages[0].ID != keyed.ID {
		t.Fatalf("search=%+v err=%v", search, err)
	}
	page, err := remote.History(ctx, "T1", "U1", "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(page.Messages) != 3 || page.Messages[0].Text != "updated" || !page.Messages[0].Deleted {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	replies, err := remote.Replies(ctx, "T1", "U1", "C1", timestamp, domain.PageRequest{Limit: 10})
	if err != nil || len(replies.Messages) != 1 || replies.Messages[0].ID != message.ID {
		t.Fatalf("replies=%+v err=%v", replies, err)
	}
	if err := remote.LeaveConversation(ctx, "T1", "U1", "C1"); err != nil {
		t.Fatal(err)
	}
	records, err := remote.ListEventsAfter(ctx, "T1", 0, 23)
	if err != nil || len(records) != 23 || records[0].Sequence != 1 || records[0].Event.Topic != "user.created" || records[0].Event.Payload != string(createdUser.ID) {
		t.Fatalf("events=%+v err=%v", records, err)
	}
}

func TestRemoteConcurrentPostsPreserveEveryCall(t *testing.T) {
	const (
		workers      = 16
		postsPerWork = 50
		expected     = workers * postsPerWork
	)
	store := memory.New()
	store.SeedWorkspace(domain.Workspace{ID: "T-load", Name: "load"})
	store.SeedUser(domain.User{ID: "U-load", WorkspaceID: "T-load", Name: "load"})
	store.SeedConversation(domain.Conversation{ID: "C-load", WorkspaceID: "T-load", Name: "load"})
	store.SeedConversationMember("C-load", "U-load")
	server := grpc.NewServer()
	if err := chatgrpc.RegisterServer(server, service.Messages{Store: store}, store, store, store); err != nil {
		t.Fatal(err)
	}
	listener := bufconn.Listen(1 << 20)
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	ctx := context.Background()
	connection, err := grpc.DialContext(ctx, "bufnet", grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	remote, err := chatgrpc.NewRemote(connection)
	if err != nil {
		t.Fatal(err)
	}

	errorsCh := make(chan error, expected)
	var group sync.WaitGroup
	var accepted atomic.Int32
	group.Add(workers)
	for worker := 0; worker < workers; worker++ {
		go func(worker int) {
			defer group.Done()
			for offset := 0; offset < postsPerWork; offset++ {
				key := fmt.Sprintf("load-%d-%d", worker, offset)
				message, err := remote.Post(ctx, "T-load", "U-load", "C-load", key, "", key)
				if err != nil {
					errorsCh <- err
					continue
				}
				if message.ID == "" || message.Text != key {
					errorsCh <- fmt.Errorf("invalid remote message for %s: %+v", key, message)
					continue
				}
				accepted.Add(1)
			}
		}(worker)
	}
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Error(err)
	}
	if accepted.Load() != expected {
		t.Fatalf("accepted %d messages, want %d", accepted.Load(), expected)
	}

	seen := make(map[domain.MessageID]struct{}, expected)
	var cursor domain.Cursor
	for len(seen) < expected {
		page, err := remote.History(ctx, "T-load", "U-load", "C-load", domain.PageRequest{Limit: 100, Cursor: cursor})
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if len(page.Messages) == 0 {
			t.Fatalf("history ended after %d messages, want %d", len(seen), expected)
		}
		for _, message := range page.Messages {
			if _, exists := seen[message.ID]; exists {
				t.Fatalf("message %s appeared twice", message.ID)
			}
			seen[message.ID] = struct{}{}
		}
		if !page.HasMore {
			break
		}
		cursor = page.NextCursor
	}
	if len(seen) != expected {
		t.Fatalf("history returned %d messages, want %d", len(seen), expected)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := remote.Post(canceled, "T-load", "U-load", "C-load", "canceled", "", "canceled"); err == nil {
		t.Fatal("canceled remote call unexpectedly succeeded")
	}
}

func containsConversation(values []domain.Conversation, want domain.ConversationID) bool {
	for _, value := range values {
		if value.ID == want {
			return true
		}
	}
	return false
}

func containsUser(values []domain.User, want domain.UserID) bool {
	for _, value := range values {
		if value.ID == want {
			return true
		}
	}
	return false
}
