package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/events"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

// assertForeignKeysEnforced asserts the schema's referential integrity holds on
// every connection the pool can hand out, not just on one of them. It holds the
// connections simultaneously so the pool is forced to open distinct ones.
func assertForeignKeysEnforced(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	const probes = 4
	held := make([]*sql.Conn, 0, probes)
	defer func() {
		for _, connection := range held {
			_ = connection.Close()
		}
	}()
	for index := 0; index < probes; index++ {
		connection, err := s.db.Conn(ctx)
		if err != nil {
			t.Fatalf("acquire connection %d: %v", index, err)
		}
		held = append(held, connection)
		orphan := fmt.Sprintf("C-orphan-%d", index)
		if _, err := connection.ExecContext(ctx, `INSERT INTO conversations(id, workspace_id, name) VALUES (?, 'T-missing', 'orphan')`, orphan); err == nil {
			t.Fatalf("connection %d accepted a conversation referencing a missing workspace", index)
		}
	}
}

func seedConversationFixture(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1", Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversationMember(ctx, "C1", "U1"); err != nil {
		t.Fatal(err)
	}
}

// trailingZeroInstants are chosen so that at least one encoding is a strict byte
// prefix of another under time.RFC3339Nano, which strips trailing zeros from the
// fraction. Under that encoding ".12Z" sorts after ".123456Z" because
// 'Z' (0x5A) > '3' (0x33), so the earlier instant compares greater.
func trailingZeroInstants() []time.Time {
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return []time.Time{
		base.Add(50 * time.Millisecond),      // .050000 -> ".05Z"
		base.Add(100 * time.Millisecond),     // .100000 -> ".1Z"
		base.Add(120 * time.Millisecond),     // .120000 -> ".12Z"
		base.Add(123456 * time.Microsecond),  // .123456 -> ".123456Z"
		base.Add(200 * time.Millisecond),     // .200000 -> ".2Z"
		base.Add(1 * time.Second),            // .000000 -> no fraction at all
		base.Add(1500 * time.Millisecond),    // .500000 -> ".5Z"
		base.Add(1500001 * time.Microsecond), // .500001 -> ".500001Z"
	}
}

func TestStoredTimestampByteOrderMatchesTimeOrder(t *testing.T) {
	instants := trailingZeroInstants()
	for left := range instants {
		for right := range instants {
			leftText := string(domain.NewStoredTime(instants[left]))
			rightText := string(domain.NewStoredTime(instants[right]))
			if (leftText < rightText) != instants[left].Before(instants[right]) {
				t.Fatalf("byte order of %q vs %q disagrees with time order of %s vs %s", leftText, rightText, instants[left], instants[right])
			}
			if (leftText == rightText) != instants[left].Equal(instants[right]) {
				t.Fatalf("byte equality of %q vs %q disagrees with time equality", leftText, rightText)
			}
		}
	}
	for _, instant := range instants {
		decoded, err := domain.ParseStoredTime(string(domain.NewStoredTime(instant)))
		if err != nil {
			t.Fatal(err)
		}
		if !decoded.Equal(instant) {
			t.Fatalf("round trip produced %s, want %s", decoded, instant)
		}
	}
}

// TestSQLiteMessageOrderIsChronologicalAcrossTrailingZeroFractions is the
// end-to-end form of the same contract: ListMessages orders and paginates on the
// stored created_at text, so a variable-width encoding reordered history and made
// keyset pages skip or repeat rows.
func TestSQLiteMessageOrderIsChronologicalAcrossTrailingZeroFractions(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "message-order.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)

	instants := trailingZeroInstants()
	for index, instant := range instants {
		message := domain.Message{ID: domain.MessageID(fmt.Sprintf("M%d", index)), WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: fmt.Sprintf("message %d", index), CreatedAt: instant}
		event := events.Event{ID: domain.EventID(fmt.Sprintf("E%d", index)), WorkspaceID: "T1", Topic: "message.created", Payload: string(message.ID), CreatedAt: instant}
		if err := s.CreateMessage(ctx, message, event, ""); err != nil {
			t.Fatal(err)
		}
	}

	page, err := s.ListMessages(ctx, "C1", domain.PageRequest{Limit: len(instants) + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != len(instants) {
		t.Fatalf("listed %d messages, want %d", len(page.Messages), len(instants))
	}
	for index := 1; index < len(page.Messages); index++ {
		if page.Messages[index].CreatedAt.Before(page.Messages[index-1].CreatedAt) {
			t.Fatalf("message %d (%s) sorted before %d (%s)", index, page.Messages[index].CreatedAt, index-1, page.Messages[index-1].CreatedAt)
		}
	}

	// Walk the whole conversation one message at a time: keyset pagination uses
	// the same comparison, so a broken encoding shows up as a skipped or repeated
	// identifier rather than as a wrong order.
	seen := make([]domain.MessageID, 0, len(instants))
	request := domain.PageRequest{Limit: 1}
	for {
		single, err := s.ListMessages(ctx, "C1", request)
		if err != nil {
			t.Fatal(err)
		}
		for _, message := range single.Messages {
			seen = append(seen, message.ID)
		}
		if !single.HasMore {
			break
		}
		request.Cursor = single.NextCursor
		if len(seen) > len(instants) {
			t.Fatalf("keyset pagination repeated messages: %v", seen)
		}
	}
	if len(seen) != len(instants) {
		t.Fatalf("keyset pagination visited %v, want all %d messages", seen, len(instants))
	}
	unique := make(map[domain.MessageID]struct{}, len(seen))
	for _, id := range seen {
		if _, repeated := unique[id]; repeated {
			t.Fatalf("keyset pagination repeated %q: %v", id, seen)
		}
		unique[id] = struct{}{}
	}
}

// TestSQLiteUnreadCountIncludesMessagesAfterATrailingZeroCursor pins the
// conversations.list badge: with a read cursor at .500000 and a message at
// .550000 the variable-width encoding compared "….5Z" against "….55Z" and
// concluded the later message was not unread.
func TestSQLiteUnreadCountIncludesMessagesAfterATrailingZeroCursor(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "unread-count.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	seedConversationFixture(t, ctx, s)

	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	read := base.Add(500 * time.Millisecond)
	unread := base.Add(550 * time.Millisecond)
	for index, instant := range []time.Time{read, unread} {
		message := domain.Message{ID: domain.MessageID(fmt.Sprintf("M%d", index)), WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "hello", CreatedAt: instant}
		event := events.Event{ID: domain.EventID(fmt.Sprintf("E%d", index)), WorkspaceID: "T1", Topic: "message.created", Payload: string(message.ID), CreatedAt: instant}
		if err := s.CreateMessage(ctx, message, event, ""); err != nil {
			t.Fatal(err)
		}
	}
	cursor := domain.ReadCursor{WorkspaceID: "T1", UserID: "U1", Conversation: "C1", LastRead: domain.NewMessageTimestamp(read), UpdatedAt: read}
	if err := s.SetReadCursor(ctx, cursor, events.Event{ID: "E-cursor", WorkspaceID: "T1", Topic: "conversation.marked", Payload: "C1", CreatedAt: read}); err != nil {
		t.Fatal(err)
	}
	count, err := s.unreadCount(ctx, "T1", "U1", "C1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("unread count = %d, want 1 for the message posted after the read cursor", count)
	}
}

// TestSQLiteExpiredOutboxLeaseCannotAcknowledgeOrRenew pins the lease-fencing
// contract at the exact instant the variable-width encoding got wrong: a lease
// that expired at .100000 renders as "….1Z", which byte-compares greater than
// "….12Z" — the encoding of the later instant that is checking it — so the
// expired owner satisfied `lease_until > now` and marked the event delivered.
func TestSQLiteExpiredOutboxLeaseCannotAcknowledgeOrRenew(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "outbox-lease.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	claimAt := base
	expiry := base.Add(100 * time.Millisecond)
	after := base.Add(120 * time.Millisecond)

	if err := s.AppendEvent(ctx, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return claimAt }
	claimed, err := s.ClaimEvents(ctx, "T1", "worker-a", 10, expiry.Sub(claimAt))
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	sequences := []uint64{claimed[0].Sequence}

	// 20ms after the lease expired.
	s.now = func() time.Time { return after }
	if err := s.AckEvents(ctx, "worker-a", sequences); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("expired owner acknowledgement error=%v, want %v", err, store.ErrLeaseConflict)
	}
	if err := s.RenewEvents(ctx, "worker-a", sequences, time.Minute); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("expired owner renewal error=%v, want %v", err, store.ErrLeaseConflict)
	}
	if err := s.ReleaseEvents(ctx, "worker-a", sequences, after.Add(time.Minute)); !errors.Is(err, store.ErrLeaseConflict) {
		t.Fatalf("expired owner release error=%v, want %v", err, store.ErrLeaseConflict)
	}
	// The event is still undelivered, so another worker can take it over.
	retaken, err := s.ClaimEvents(ctx, "T1", "worker-b", 10, time.Minute)
	if err != nil || len(retaken) != 1 || retaken[0].Sequence != sequences[0] {
		t.Fatalf("retaken=%+v err=%v", retaken, err)
	}
	if err := s.AckEvents(ctx, "worker-b", sequences); err != nil {
		t.Fatalf("live owner acknowledgement error=%v", err)
	}
}

// TestSQLiteLiveOutboxLeaseCannotBeStolen is the symmetric half: the claim
// predicate compares `lease_until <= now`, so the same encoding could declare a
// still-valid lease expired and hand the event to a second worker.
func TestSQLiteLiveOutboxLeaseCannotBeStolen(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "outbox-steal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	base := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := s.AppendEvent(ctx, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "message.created", Payload: "M1", CreatedAt: base}); err != nil {
		t.Fatal(err)
	}
	// Lease until .123456; the second worker checks at .500000, whose old
	// encoding "….5Z" byte-compares greater than "….123456Z" even though a lease
	// held until .123456 is long gone by .5 — that direction is correct. The
	// dangerous direction is a lease held until .5 checked at .123456.
	s.now = func() time.Time { return base }
	claimed, err := s.ClaimEvents(ctx, "T1", "worker-a", 10, 500*time.Millisecond)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claimed=%+v err=%v", claimed, err)
	}
	s.now = func() time.Time { return base.Add(123456 * time.Microsecond) }
	stolen, err := s.ClaimEvents(ctx, "T1", "worker-b", 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(stolen) != 0 {
		t.Fatalf("second worker claimed %d events while the first lease was still valid", len(stolen))
	}
}

func TestSQLiteStoredTimestampMigrationRewritesLegacyRows(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-timestamps.db")

	// A database written by the previous encoding: variable-width RFC3339Nano.
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedConversationFixture(t, ctx, first)
	instants := trailingZeroInstants()
	for index, instant := range instants {
		if _, err := first.db.ExecContext(ctx, `INSERT INTO messages (id, workspace_id, conversation, author_id, text, blocks, attachments, thread_timestamp, created_at, deleted, unfurls) VALUES (?, 'T1', 'C1', 'U1', 'legacy', '', '[]', '', ?, 0, '{}')`, fmt.Sprintf("M%d", index), instant.UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 77 WHERE version = ?`, schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	// Reopening runs the migration against the populated database.
	second, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	rows, err := second.db.QueryContext(ctx, `SELECT created_at FROM messages`)
	if err != nil {
		t.Fatal(err)
	}
	stored := make([]string, 0, len(instants))
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		stored = append(stored, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(stored) != len(instants) {
		t.Fatalf("stored %d rows, want %d", len(stored), len(instants))
	}
	for _, value := range stored {
		if !domain.StoredTime(value).Ordered() {
			t.Fatalf("migrated timestamp %q is not fixed width", value)
		}
	}
	page, err := second.ListMessages(ctx, "C1", domain.PageRequest{Limit: len(instants) + 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Messages) != len(instants) {
		t.Fatalf("listed %d migrated messages, want %d", len(page.Messages), len(instants))
	}
	for index := 1; index < len(page.Messages); index++ {
		if page.Messages[index].CreatedAt.Before(page.Messages[index-1].CreatedAt) {
			t.Fatalf("migrated message %d sorted out of order", index)
		}
	}
	for index, instant := range instants {
		if !page.Messages[index].CreatedAt.Equal(instant) {
			t.Fatalf("migrated message %d has timestamp %s, want %s", index, page.Messages[index].CreatedAt, instant)
		}
	}

	// The migration is idempotent, and it is a no-op on a fresh database.
	if err := second.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration failed: %v", err)
	}
	fresh, err := Open(ctx, filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	if err := fresh.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration on a fresh database failed: %v", err)
	}
}

func TestSQLiteStoredTimestampMigrationIsSafeUnderConcurrentStartup(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "concurrent-timestamps.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	seedConversationFixture(t, ctx, first)
	for index, instant := range trailingZeroInstants() {
		if _, err := first.db.ExecContext(ctx, `INSERT INTO messages (id, workspace_id, conversation, author_id, text, blocks, attachments, thread_timestamp, created_at, deleted, unfurls) VALUES (?, 'T1', 'C1', 'U1', 'legacy', '', '[]', '', ?, 0, '{}')`, fmt.Sprintf("M%d", index), instant.UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := first.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 77 WHERE version = ?`, schemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	const replicas = 4
	start := make(chan struct{})
	results := make(chan error, replicas)
	var group sync.WaitGroup
	for index := 0; index < replicas; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			replica, err := Open(context.Background(), path)
			if err == nil {
				err = replica.Close()
			}
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("concurrent replica startup failed: %v", err)
		}
	}
	final, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer final.Close()
	var unordered int
	if err := final.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE length(created_at) <> ?`, domain.StoredTimeWidth).Scan(&unordered); err != nil {
		t.Fatal(err)
	}
	if unordered != 0 {
		t.Fatalf("%d messages still carry a variable-width timestamp", unordered)
	}
}

// TestSQLiteRepositoryWritesNoVariableWidthTimestamp is the invariant behind the
// encoding: no repository path may reach a timestamp column through anything but
// domain.NewStoredTime. A reintroduced time.RFC3339Nano write would restore the
// whole defect class silently, so the guard is on the source, not on one column.
func TestSQLiteRepositoryWritesNoVariableWidthTimestamp(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "sqlstore.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	ast.Inspect(file, func(node ast.Node) bool {
		selector, ok := node.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := selector.X.(*ast.Ident)
		if !ok || pkg.Name != "time" {
			return true
		}
		if strings.HasPrefix(selector.Sel.Name, "RFC3339") {
			t.Fatalf("sqlstore.go references time.%s; stored timestamps must go through domain.NewStoredTime, whose encoding is fixed width", selector.Sel.Name)
		}
		return true
	})
}

// TestSQLiteSearchConversationsTreatsLikeMetacharactersLiterally pins the
// escaping contract: "%" used to match every conversation in the workspace.
func TestSQLiteSearchConversationsTreatsLikeMetacharactersLiterally(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "search-conversations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	for _, conversation := range []domain.Conversation{
		{ID: "C1", WorkspaceID: "T1", Name: "general"},
		{ID: "C2", WorkspaceID: "T1", Name: "random"},
		{ID: "C3", WorkspaceID: "T1", Name: "100%-coverage"},
		{ID: "C4", WorkspaceID: "T1", Name: "snake_case"},
	} {
		if err := s.SeedConversation(ctx, conversation); err != nil {
			t.Fatal(err)
		}
	}
	for query, want := range map[string][]domain.ConversationID{
		"%":     {"C3"},
		"_":     {"C4"},
		"100%":  {"C3"},
		"e_al":  nil,
		"eral":  {"C1"},
		"gener": {"C1"},
	} {
		page, err := s.SearchConversations(ctx, "T1", query, domain.PageRequest{Limit: 10})
		if err != nil {
			t.Fatalf("search %q: %v", query, err)
		}
		if len(page.Conversations) != len(want) {
			t.Fatalf("search %q matched %d conversations, want %d", query, len(page.Conversations), len(want))
		}
		for index := range want {
			if page.Conversations[index].ID != want[index] {
				t.Fatalf("search %q matched %v, want %v", query, page.Conversations, want)
			}
		}
	}
}

// TestSQLiteUserEmailIdentityIsCaseFoldedInGo pins the identity contract that
// SQL lower() could not provide: SQLite folds ASCII only, PostgreSQL is
// locale-aware, so "Ä@x.test" and "ä@x.test" were one user on PostgreSQL and two
// on SQLite — a workspace-scoped identity collision.
func TestSQLiteUserEmailIdentityIsCaseFoldedInGo(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "email-identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatal(err)
	}
	first := domain.User{ID: "U1", WorkspaceID: "T1", Email: "ä@x.test", Name: "first"}
	if err := s.CreateUser(ctx, first, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "U1", Role: domain.WorkspaceRoleMember, Active: true}, events.Event{ID: "E1", WorkspaceID: "T1", Topic: "user.created", Payload: "U1", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	second := domain.User{ID: "U2", WorkspaceID: "T1", Email: "Ä@x.test", Name: "second"}
	if err := s.CreateUser(ctx, second, domain.WorkspaceMembership{WorkspaceID: "T1", UserID: "U2", Role: domain.WorkspaceRoleMember, Active: true}, events.Event{ID: "E2", WorkspaceID: "T1", Topic: "user.created", Payload: "U2", CreatedAt: time.Now().UTC()}); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("non-ASCII case variant error=%v, want %v", err, store.ErrAlreadyExists)
	}
	found, err := s.FindUserByEmail(ctx, "T1", "  Ä@X.TEST  ")
	if err != nil || found.ID != "U1" {
		t.Fatalf("lookup by a non-ASCII case variant returned %+v err=%v", found, err)
	}
}

// TestIntegrityCheckIsNotFakedOnNonSQLiteProfiles pins the replacement for the
// rewrite that turned PRAGMA integrity_check into SELECT 'ok': a profile with no
// physical integrity check must say so rather than report success.
func TestIntegrityCheckIsNotFakedOnNonSQLiteProfiles(t *testing.T) {
	repository := &Store{sqliteDialect: false, now: systemClock}
	if err := repository.IntegrityCheck(context.Background()); !errors.Is(err, ErrIntegrityCheckUnsupported) {
		t.Fatalf("integrity check on a non-SQLite profile err=%v, want %v", err, ErrIntegrityCheckUnsupported)
	}
}

// TestConnectionSettingsAreVerifiedAcrossThePool pins the guard that refuses to
// hand out a repository whose per-connection settings did not apply.
func TestConnectionSettingsAreVerifiedAcrossThePool(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "verify.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.VerifyConnectionSettings(ctx); err != nil {
		t.Fatalf("connection settings: %v", err)
	}
	assertForeignKeysEnforced(t, s)
}

// TestSQLiteUserEmailMigrationNormalizesAndRefusesCollisions covers the second
// data migration against an already-populated database: addresses are rewritten
// into the canonical form, and two rows that would collide once normalized abort
// the upgrade with both identities named instead of failing on a unique index.
func TestSQLiteUserEmailMigrationNormalizesAndRefusesCollisions(t *testing.T) {
	ctx := context.Background()
	populate := func(t *testing.T, rows [][3]string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "legacy-emails.db")
		first, err := Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := first.SeedWorkspace(ctx, domain.Workspace{ID: "T1", Name: "Test"}); err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if _, err := first.db.ExecContext(ctx, `INSERT INTO users(id, workspace_id, email, name) VALUES (?, ?, ?, ?)`, row[0], "T1", row[1], row[2]); err != nil {
				t.Fatal(err)
			}
		}
		// Step 79 has already run on this fresh database, so wind the recorded
		// version back to make the reopen replay it against real data.
		if _, err := first.db.ExecContext(ctx, `UPDATE schema_migrations SET version = 78 WHERE version = ?`, schemaVersion); err != nil {
			t.Fatal(err)
		}
		if _, err := first.db.ExecContext(ctx, `DROP INDEX IF EXISTS users_workspace_email_normalized`); err != nil {
			t.Fatal(err)
		}
		if err := first.Close(); err != nil {
			t.Fatal(err)
		}
		return path
	}

	path := populate(t, [][3]string{{"U1", "MiXeD@Example.COM", "mixed"}, {"U2", "Ä@x.test", "accented"}})
	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	for id, want := range map[domain.UserID]string{"U1": "mixed@example.com", "U2": "ä@x.test"} {
		user, err := migrated.GetUser(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if user.Email != want {
			t.Fatalf("migrated email for %s = %q, want %q", id, user.Email, want)
		}
	}
	found, err := migrated.FindUserByEmail(ctx, "T1", "MIXED@EXAMPLE.COM")
	if err != nil || found.ID != "U1" {
		t.Fatalf("lookup after migration returned %+v err=%v", found, err)
	}
	// The migration is idempotent against the database it just rewrote.
	if err := migrated.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration failed: %v", err)
	}

	// SQLite's ASCII-only lower() let these two rows coexist; normalizing makes
	// them one identity, so the upgrade must stop and name both users.
	collision := populate(t, [][3]string{{"U1", "Ä@x.test", "upper"}, {"U2", "ä@x.test", "lower"}})
	if _, err := Open(ctx, collision); err == nil {
		t.Fatal("a database with two users that normalize to one address was upgraded")
	} else if !strings.Contains(err.Error(), "U1") || !strings.Contains(err.Error(), "U2") {
		t.Fatalf("collision error=%v, want both user identities named", err)
	}
}
