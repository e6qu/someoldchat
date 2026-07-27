package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
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

// TestRepositoriesWriteNoVariableWidthTimestamp is the invariant behind the
// encoding: no repository path may reach a timestamp column, an ordering key or
// a keyset cursor through anything but domain.NewStoredTime.
//
// The previous version of this guard read one file — sqlstore.go — and matched
// one spelling, the time.RFC3339* selector. Both narrowings were load bearing:
// the in-memory repository kept building its star and user-reaction cursor keys
// with time.RFC3339Nano the whole time the guard was green, in a different file,
// and an inlined layout string would have passed it in any file. So it now walks
// every non-test source file under internal/store and rejects a bare layout
// literal as well as the named constants.
func TestRepositoriesWriteNoVariableWidthTimestamp(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	walkErr := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if parseErr != nil {
			return parseErr
		}
		scanned++
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			relative = path
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch typed := node.(type) {
			case *ast.SelectorExpr:
				pkg, ok := typed.X.(*ast.Ident)
				if !ok || pkg.Name != "time" {
					return true
				}
				// Every named layout in the time package is variable width in
				// one field or another; none of them may encode a stored value.
				for _, layout := range []string{"RFC3339", "RFC1123", "RFC822", "RFC850", "ANSIC", "UnixDate", "RubyDate", "Kitchen", "Stamp", "Layout", "DateTime", "DateOnly", "TimeOnly"} {
					if typed.Sel.Name == layout || strings.HasPrefix(typed.Sel.Name, layout) {
						t.Errorf("%s references time.%s; stored timestamps must go through domain.NewStoredTime, whose encoding is fixed width", relative, typed.Sel.Name)
					}
				}
			case *ast.BasicLit:
				if typed.Kind != token.STRING {
					return true
				}
				// Go's reference time. Any literal carrying it is a hand-written
				// layout, which is the same defect wearing different clothes.
				if strings.Contains(typed.Value, "15:04:05") || strings.Contains(typed.Value, "2006-01-02") {
					t.Errorf("%s inlines the time layout %s; stored timestamps must go through domain.NewStoredTime", relative, typed.Value)
				}
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		t.Fatal(walkErr)
	}
	if scanned < 4 {
		t.Fatalf("the guard scanned only %d files; it is not covering internal/store", scanned)
	}
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

// TestSQLiteUserEmailMigrationNormalizesAndResolvesCollisions covers the second
// data migration against an already-populated database: addresses are rewritten
// into the canonical form, and two rows that collide once normalized are
// resolved deterministically and recorded, rather than stopping the upgrade for
// ever.
//
// The collision is one the pre-upgrade schema deliberately admitted — its
// uniqueness guard was an index on lower(email), and SQLite folds ASCII only —
// so aborting punished the operator for the product's own per-engine rule and
// left no supported way forward.
func TestSQLiteUserEmailMigrationNormalizesAndResolvesCollisions(t *testing.T) {
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
	// them one identity. The upgrade must complete: the lowest identifier keeps
	// the address, the other is cleared, and the clearing is recorded so an
	// administrator can act on it.
	collision := populate(t, [][3]string{{"U1", "Ä@x.test", "upper"}, {"U2", "ä@x.test", "lower"}})
	resolved, err := Open(ctx, collision)
	if err != nil {
		t.Fatalf("a database an older release admitted must still be upgradable: %v", err)
	}
	defer resolved.Close()
	kept, err := resolved.GetUser(ctx, "U1")
	if err != nil || kept.Email != "ä@x.test" {
		t.Fatalf("lowest identifier kept %+v err=%v, want the normalized address", kept, err)
	}
	cleared, err := resolved.GetUser(ctx, "U2")
	if err != nil || cleared.Email != "" {
		t.Fatalf("colliding identity %+v err=%v, want its address cleared", cleared, err)
	}
	survivor, err := resolved.FindUserByEmail(ctx, "T1", "Ä@X.TEST")
	if err != nil || survivor.ID != "U1" {
		t.Fatalf("the surviving address resolves to %+v err=%v, want U1", survivor, err)
	}
	notices, err := resolved.MigrationNotices(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var recorded bool
	for _, notice := range notices {
		if notice.Kind == MigrationNoticeEmailCleared && notice.Subject == "U2" {
			recorded = true
			if !strings.Contains(notice.Detail, "ä@x.test") || !strings.Contains(notice.Detail, "U1") {
				t.Fatalf("notice detail=%q, want the address and the identity that kept it", notice.Detail)
			}
			if notice.ObservedAt.IsZero() {
				t.Fatal("notice has no observation instant")
			}
		}
	}
	if !recorded {
		t.Fatalf("notices=%+v, want the cleared address recorded for U2", notices)
	}
	// Reopening must not clear anything else, and must not re-resolve.
	if err := resolved.Migrate(ctx); err != nil {
		t.Fatalf("re-running the migration after a resolved collision failed: %v", err)
	}
}

// A message's timestamp is its public identifier, and a read cursor is expressed
// in that same form. Storing a creation instant finer than the identifier can
// express makes a message permanently unread: it stores greater than a cursor
// derived from the very same instant.
//
// This reproduced only where the host clock returns sub-microsecond precision,
// so it passed on a development machine and failed in continuous integration.
// The instant here is chosen explicitly so the assertion does not depend on the
// clock at all.
func TestSQLiteMessageInstantCannotOutrunItsOwnTimestamp(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, filepath.Join(t.TempDir(), "message-instant.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}

	// 789 nanoseconds past a microsecond boundary: a value the Slack-style
	// timestamp cannot represent.
	created := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	if err := s.CreateMessage(ctx, domain.Message{ID: "M1", WorkspaceID: "T1", Conversation: "C1", AuthorID: "U1", Text: "unread", CreatedAt: created}, mustEvent(t, "E1", "T1", created), ""); err != nil {
		t.Fatal(err)
	}

	stored, err := s.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(stored.Messages) != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	instant := stored.Messages[0].CreatedAt
	if roundTripped := domain.NewMessageTimestamp(instant); string(roundTripped) != string(domain.NewMessageTimestamp(created)) {
		t.Fatalf("timestamp=%q, want it stable across the store", roundTripped)
	}
	if instant.Nanosecond()%1000 != 0 {
		t.Fatalf("stored instant %v carries sub-microsecond precision its own timestamp cannot express", instant)
	}

	// A cursor built from the message's own timestamp must mark it read.
	if err := s.SetReadCursor(ctx, domain.ReadCursor{WorkspaceID: "T1", UserID: "U1", Conversation: "C1", LastRead: domain.NewMessageTimestamp(created), UpdatedAt: created}, mustEvent(t, "E2", "T1", created)); err != nil {
		t.Fatal(err)
	}
	page, err := s.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(page.Conversations) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page.Conversations[0].UnreadCount != 0 {
		t.Fatalf("unread=%d, want a message covered by a cursor at its own timestamp to be read", page.Conversations[0].UnreadCount)
	}
}

// mustEvent builds a durable event through the constructor, so a fixture cannot
// describe a payload no producer emits.
func mustEvent(t *testing.T, id domain.EventID, workspace domain.WorkspaceID, at time.Time) events.Event {
	t.Helper()
	event, err := events.New(id, workspace, "U1", events.NewPayload("message.created", events.String("message_id", "M1"), events.String("channel_id", "C1")), at)
	if err != nil {
		t.Fatal(err)
	}
	return event
}

// A message written before domain.MessageInstant existed carries sub-microsecond
// precision its own timestamp cannot express, so a read cursor built from that
// timestamp can never cover it. Truncating at write time repaired only new
// messages; migration step 78 rewrote the ENCODING of the existing ones and
// preserved their RESOLUTION, carrying the defect across the upgrade intact.
func TestSQLiteMigrationRepairsMessageInstantsWrittenBeforeTruncation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy-instants.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SeedWorkspace(ctx, domain.Workspace{ID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SeedUser(ctx, domain.User{ID: "U1", WorkspaceID: "T1"}); err != nil {
		t.Fatal(err)
	}
	if err := first.SeedConversation(ctx, domain.Conversation{ID: "C1", WorkspaceID: "T1", Name: "general"}); err != nil {
		t.Fatal(err)
	}

	// Write the row the way a pre-truncation release did, bypassing CreateMessage
	// so the value reaches the column exactly as it would have then.
	legacy := time.Date(2026, 3, 4, 5, 6, 7, 123456789, time.UTC)
	if _, err := first.db.ExecContext(ctx, `INSERT INTO messages (id, workspace_id, conversation, author_id, text, blocks, attachments, thread_timestamp, created_at, deleted, unfurls) VALUES ('M1','T1','C1','U1','legacy','','[]','',?,0,'{}')`, domain.NewStoredTime(legacy)); err != nil {
		t.Fatal(err)
	}
	// Rewind the recorded schema version so the upgrade path runs again.
	if _, err := first.db.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version >= 81`); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopening an existing database must migrate it: %v", err)
	}
	defer migrated.Close()

	stored, err := migrated.ListMessages(ctx, "C1", domain.PageRequest{Limit: 10})
	if err != nil || len(stored.Messages) != 1 {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	if instant := stored.Messages[0].CreatedAt; instant.Nanosecond()%1000 != 0 {
		t.Fatalf("migrated instant %v still carries sub-microsecond precision its timestamp cannot express", instant)
	}

	// The real consequence: a cursor at the message's own timestamp marks it read.
	if err := migrated.SetReadCursor(ctx, domain.ReadCursor{WorkspaceID: "T1", UserID: "U1", Conversation: "C1", LastRead: domain.NewMessageTimestamp(legacy), UpdatedAt: legacy}, mustEvent(t, "E2", "T1", legacy)); err != nil {
		t.Fatal(err)
	}
	page, err := migrated.ListConversations(ctx, "T1", "U1", domain.ConversationListRequest{Limit: 10})
	if err != nil || len(page.Conversations) != 1 {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	if page.Conversations[0].UnreadCount != 0 {
		t.Fatalf("unread=%d: a message written before truncation is still permanently unread after the upgrade", page.Conversations[0].UnreadCount)
	}
}
