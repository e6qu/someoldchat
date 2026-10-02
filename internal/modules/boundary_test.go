package modules

import (
	"go/build"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const modulePath = "github.com/sameoldchat/sameoldchat"

// The HTTP and HTMX adapters reach chat only through its module API, so a
// process that serves them over the gRPC transport must not link the chat
// implementation or a storage backend. They once imported internal/service
// for its error sentinels and pure helpers, which linked the whole
// implementation into every adapter whatever the composition.
func TestAdaptersDoNotLinkModuleImplementations(t *testing.T) {
	adapters := []string{
		"internal/api/slack",
		"internal/web",
		"internal/realtime",
		"internal/auth",
	}
	forbidden := []string{
		"internal/service",
		"internal/store/memory",
		"internal/store/sqlstore",
		"internal/store/postgres",
		"internal/store/dqlite",
		"internal/modules/chat/transport/grpc",
	}
	root := repositoryRoot(t)
	for _, adapter := range adapters {
		path, found := dependencyPath(t, root, modulePath+"/"+adapter, forbidden)
		if found {
			t.Errorf("%s links a module implementation: %s", adapter, strings.Join(path, " -> "))
		}
	}
}

// dependencyPath walks the non-test imports inside this module, depth first,
// and returns the first chain from start to a forbidden package.
func dependencyPath(t *testing.T, root, start string, forbidden []string) ([]string, bool) {
	t.Helper()
	banned := make(map[string]bool, len(forbidden))
	for _, name := range forbidden {
		banned[modulePath+"/"+name] = true
	}
	visited := map[string]bool{}
	var walk func(string, []string) ([]string, bool)
	walk = func(importPath string, chain []string) ([]string, bool) {
		chain = append(chain, strings.TrimPrefix(importPath, modulePath+"/"))
		if banned[importPath] {
			return chain, true
		}
		if visited[importPath] {
			return nil, false
		}
		visited[importPath] = true
		pkg, err := build.Import(importPath, root, 0)
		if err != nil {
			t.Fatalf("import %s: %v", importPath, err)
		}
		for _, next := range pkg.Imports {
			if !strings.HasPrefix(next, modulePath+"/") {
				continue
			}
			if found, ok := walk(next, chain); ok {
				return found, true
			}
		}
		return nil, false
	}
	return walk(start, nil)
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}
