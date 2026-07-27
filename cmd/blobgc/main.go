package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

// exitConfiguration and exitRuntime separate "the operator gave us something
// impossible" from "something failed while running".
const (
	exitConfiguration = 2
	exitRuntime       = 1
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	// Every teardown runs through run's defers; main is the only place that
	// exits, so the store is closed even on the documented audit exit that
	// reports missing objects.
	if code := run(context.Background(), logger, os.Args[1:]); code != 0 {
		os.Exit(code)
	}
}

func run(ctx context.Context, logger *slog.Logger, args []string) int {
	flags := flag.NewFlagSet("sameoldchat-blobgc", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	backend := flags.String("store", "", "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	blobS3Bucket := flags.String("blob-s3-bucket", "", "Amazon Simple Storage Service bucket for file storage")
	blobS3Prefix := flags.String("blob-s3-prefix", "", "Amazon Simple Storage Service key prefix for file storage")
	dsn := flags.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql")
	dqliteDirectory := flags.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flags.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flags.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flags.String("dqlite-database", "", "dqlite database name")
	blobDirectory := flags.String("blob-dir", "", "external blob directory")
	blobMaxBytes := flags.Int64("blob-max-bytes", 100<<20, "maximum individual blob size")
	workspace := flags.String("workspace", "", "workspace ID (required)")
	owner := flags.String("owner", "", "unique cleanup worker owner ID (required for cleanup mode, rejected with -audit)")
	audit := flags.Bool("audit", false, "audit durable blob references and provider objects, then exit")
	enqueueOrphans := flags.Bool("enqueue-orphans", false, "enqueue audited orphan objects for leased cleanup; requires -audit")
	maxResults := flags.Int("max-audit-results", 1000, "maximum orphan or missing keys returned by an audit; audit mode only")
	minOrphanAge := flags.Duration("min-orphan-age", time.Hour, "grace period an unreferenced object must survive before it counts as an orphan; audit mode only")
	limit := flags.Int("batch-size", 100, "bounded cleanup batch size; cleanup mode only")
	lease := flags.Duration("lease", 30*time.Second, "durable cleanup lease; cleanup mode only")
	poll := flags.Duration("poll", 250*time.Millisecond, "poll interval; cleanup mode only")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return exitConfiguration
	}
	// Every configuration decision is made before the store is opened. Opening
	// first created the SQLite database, and for dqlite joined the cluster, only
	// to reject the command line a moment later; an invocation that is refused
	// must leave no durable state behind.
	if *backend == "" || *workspace == "" || (*blobDirectory == "" && *blobS3Bucket == "") || (*blobDirectory != "" && *blobS3Bucket != "") || *maxResults <= 0 || *minOrphanAge <= 0 {
		logger.Error("blobgc requires one explicit blob store, store backend, workspace, positive audit limit, and positive minimum orphan age")
		return exitConfiguration
	}
	if *enqueueOrphans && !*audit {
		logger.Error("-enqueue-orphans requires -audit")
		return exitConfiguration
	}
	// A flag the selected mode cannot honour is rejected rather than ignored: an
	// operator who passed -owner to an audit, or -min-orphan-age to a cleanup
	// worker, was told nothing and got a run that ignored the setting.
	if *audit {
		if ignored := explicitlySet(flags, "owner", "batch-size", "lease", "poll"); len(ignored) != 0 {
			logger.Error("audit mode cannot honour cleanup worker settings", "ignored", strings.Join(ignored, ", "))
			return exitConfiguration
		}
	} else {
		if ignored := explicitlySet(flags, "max-audit-results", "min-orphan-age"); len(ignored) != 0 {
			logger.Error("cleanup mode cannot honour audit settings", "ignored", strings.Join(ignored, ", "), "hint", "pass -audit to reconcile")
			return exitConfiguration
		}
		if *owner == "" || *limit <= 0 || *lease <= 0 || *poll <= 0 {
			logger.Error("blobgc cleanup mode requires owner and positive batch, lease, and poll values")
			return exitConfiguration
		}
	}
	cluster, err := localchat.ParseCluster(*dqliteCluster)
	if err != nil {
		logger.Error("parse dqlite cluster", "error", err)
		return exitConfiguration
	}
	runtime, err := localchat.Open(ctx, localchat.Config{Backend: localchat.Backend(*backend), DSN: *dsn, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase, BlobDirectory: *blobDirectory, BlobS3Bucket: *blobS3Bucket, BlobS3Prefix: *blobS3Prefix, BlobMaxBytes: *blobMaxBytes})
	if err != nil {
		logger.Error("open blob cleanup state", "error", err)
		return exitRuntime
	}
	defer func() {
		if err := runtime.Closer.Close(); err != nil {
			logger.Error("close blob cleanup state", "error", err)
		}
	}()
	if runtime.BlobStore == nil {
		logger.Error("blob cleanup state did not provide blob storage")
		return exitRuntime
	}
	objects, ok := runtime.BlobStore.(blob.Store)
	if !ok {
		logger.Error("blob cleanup state provided an invalid blob store")
		return exitRuntime
	}
	if *audit {
		walkStore, ok := runtime.BlobStore.(blob.WalkStore)
		if !ok {
			logger.Error("selected blob store does not support reconciliation")
			return exitRuntime
		}
		reconciler, err := blob.NewReconciler(runtime.Store, walkStore, runtime.Store, *maxResults, *minOrphanAge)
		if err != nil {
			logger.Error("configure blob reconciliation", "error", err)
			return exitConfiguration
		}
		result, err := reconciler.Audit(ctx, domain.WorkspaceID(*workspace))
		if err != nil {
			logger.Error("audit blobs", "error", err)
			return exitRuntime
		}
		logger.Info("blob audit", "objects", result.Objects, "references", result.References, "orphans", len(result.OrphanKeys), "missing", len(result.MissingKeys), "duplicates", result.DuplicateKeys, "too_recent_for_orphan_cleanup", result.RecentObjects)
		if *enqueueOrphans {
			count, err := reconciler.EnqueueOrphans(ctx, domain.WorkspaceID(*workspace), result)
			if err != nil {
				logger.Error("enqueue orphan cleanup", "count", count, "error", err)
				return exitRuntime
			}
			logger.Info("enqueued orphan cleanup", "count", count)
		}
		if len(result.MissingKeys) != 0 {
			logger.Error("audit found referenced objects the blob store does not have", "missing", len(result.MissingKeys))
			return exitRuntime
		}
		return 0
	}
	worker, err := blob.NewCleanupWorker(runtime.CleanupSource, objects, *owner, *limit, *lease)
	if err != nil {
		logger.Error("configure blob cleanup worker", "error", err)
		return exitConfiguration
	}
	workerContext, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		if workerContext.Err() != nil {
			return 0
		}
		count, err := worker.RunOnce(workerContext, domain.WorkspaceID(*workspace))
		if err != nil && workerContext.Err() == nil {
			logger.Error("blob cleanup failed", "count", count, "error", err)
		}
		select {
		case <-workerContext.Done():
			return 0
		case <-ticker.C:
		}
	}
}

// explicitlySet names the flags the operator supplied on the command line. A
// flag with a default cannot be distinguished from an unset one by value, so
// rejecting a setting the selected mode cannot honour needs the parsed set.
func explicitlySet(flags *flag.FlagSet, names ...string) []string {
	supplied := make(map[string]bool, len(names))
	flags.Visit(func(f *flag.Flag) { supplied[f.Name] = true })
	found := make([]string, 0, len(names))
	for _, name := range names {
		if supplied[name] {
			found = append(found, "-"+name)
		}
	}
	return found
}
