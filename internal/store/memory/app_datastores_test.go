package memory

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/store"
)

func TestAppDatastoreItemsAreOrderedReplacedDeletedAndUninstalled(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now().UTC()
	values := []domain.AppDatastoreItem{
		{AppID: "A1", WorkspaceID: "T1", Datastore: "incidents", ID: "one", Item: `{"id":"one","value":1}`, UpdatedAt: now},
		{AppID: "A1", WorkspaceID: "T1", Datastore: "incidents", ID: "two", Item: `{"id":"two","value":2}`, UpdatedAt: now},
	}
	if err := s.PutAppDatastoreItems(ctx, values); err != nil {
		t.Fatal(err)
	}
	patch := values[0]
	patch.Item = `{"id":"one","other":"kept"}`
	merged, err := s.MergeAppDatastoreItems(ctx, []domain.AppDatastoreItem{patch})
	if err != nil || len(merged) != 1 || merged[0].Item != `{"id":"one","other":"kept","value":1}` {
		t.Fatalf("merged=%+v err=%v", merged, err)
	}
	values[0].Item = `{"id":"one","value":3}`
	if err := s.PutAppDatastoreItems(ctx, values[:1]); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetAppDatastoreItems(ctx, "A1", "T1", "incidents", []string{"two", "missing", "one"})
	if err != nil || len(got) != 2 || got[0].ID != "two" || got[1].Item != values[0].Item {
		t.Fatalf("ordered items=%+v err=%v", got, err)
	}
	page, more, cursor, err := s.ListAppDatastoreItems(ctx, "A1", "T1", "incidents", domain.PageRequest{Limit: 1})
	if err != nil || !more || cursor == "" || len(page) != 1 || page[0].ID != "one" {
		t.Fatalf("first page=%+v more=%v cursor=%q err=%v", page, more, cursor, err)
	}
	page, more, _, err = s.ListAppDatastoreItems(ctx, "A1", "T1", "incidents", domain.PageRequest{Limit: 1, Cursor: cursor})
	if err != nil || more || len(page) != 1 || page[0].ID != "two" {
		t.Fatalf("second page=%+v more=%v err=%v", page, more, err)
	}
	if err := s.DeleteAppDatastoreItems(ctx, "A1", "T1", "incidents", []string{"two"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAppDatastoreItems(ctx, "A1", "T1", "incidents", []string{"one", "two"})
	if err != nil || len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("after delete=%+v err=%v", got, err)
	}

	s.appInstallations["A1\x00T1"] = domain.AppInstallation{AppID: "A1", WorkspaceID: "T1", Enabled: true}
	if err := s.UninstallApp(ctx, "T1", "A1"); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetAppDatastoreItems(ctx, "A1", "T1", "incidents", []string{"one"})
	if err != nil || len(got) != 0 {
		t.Fatalf("items survived uninstall=%+v err=%v", got, err)
	}
	if err := s.PutAppDatastoreItems(ctx, nil); !errors.Is(err, store.ErrInvalidArgument) {
		t.Fatalf("empty write error=%v, want %v", err, store.ErrInvalidArgument)
	}
}

func TestAppDatastoreMergeDoesNotLoseConcurrentFields(t *testing.T) {
	ctx := context.Background()
	s := New()
	now := time.Now().UTC()
	base := domain.AppDatastoreItem{AppID: "A1", WorkspaceID: "T1", Datastore: "records", ID: "one", Item: `{"id":"one"}`, UpdatedAt: now}
	if err := s.PutAppDatastoreItems(ctx, []domain.AppDatastoreItem{base}); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for _, patch := range []string{`{"id":"one","left":1}`, `{"id":"one","right":2}`} {
		group.Add(1)
		go func() {
			defer group.Done()
			value := base
			value.Item = patch
			_, err := s.MergeAppDatastoreItems(ctx, []domain.AppDatastoreItem{value})
			errs <- err
		}()
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.GetAppDatastoreItems(ctx, "A1", "T1", "records", []string{"one"})
	if err != nil || len(got) != 1 || got[0].Item != `{"id":"one","left":1,"right":2}` {
		t.Fatalf("item=%+v err=%v", got, err)
	}
}
