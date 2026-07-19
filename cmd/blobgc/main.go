package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/sameoldchat/sameoldchat/internal/app/localchat"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
)

func main() {
	backend := flag.String("store", "", "storage backend: memory, sqlite, postgresql, or dqlite (required)")
	blobS3Bucket := flag.String("blob-s3-bucket", "", "Amazon Simple Storage Service bucket for file storage")
	blobS3Prefix := flag.String("blob-s3-prefix", "", "Amazon Simple Storage Service key prefix for file storage")
	dsn := flag.String("db", "", "SQLite or PostgreSQL DSN; required for sqlite and postgresql")
	dqliteDirectory := flag.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flag.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flag.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flag.String("dqlite-database", "", "dqlite database name")
	blobDirectory := flag.String("blob-dir", "", "external blob directory")
	blobMaxBytes := flag.Int64("blob-max-bytes", 100<<20, "maximum individual blob size")
	workspace := flag.String("workspace", "", "workspace ID (required)")
	owner := flag.String("owner", "", "unique cleanup worker owner ID for cleanup mode")
	audit := flag.Bool("audit", false, "audit durable blob references and provider objects, then exit")
	enqueueOrphans := flag.Bool("enqueue-orphans", false, "enqueue audited orphan objects for leased cleanup")
	maxResults := flag.Int("max-audit-results", 1000, "maximum orphan or missing keys returned by an audit")
	limit := flag.Int("batch-size", 100, "bounded cleanup batch size")
	lease := flag.Duration("lease", 30*time.Second, "durable cleanup lease")
	poll := flag.Duration("poll", 250*time.Millisecond, "poll interval")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *backend == "" || *workspace == "" || (*blobDirectory == "" && *blobS3Bucket == "") || (*blobDirectory != "" && *blobS3Bucket != "") || *maxResults <= 0 {
		logger.Error("blobgc requires one explicit blob store, store backend, workspace, and positive audit limit")
		os.Exit(2)
	}
	if !*audit && (*owner == "" || *limit <= 0 || *lease <= 0 || *poll <= 0) {
		logger.Error("blobgc cleanup mode requires owner and positive batch, lease, and poll values")
		os.Exit(2)
	}
	cluster, err := localchat.ParseCluster(*dqliteCluster)
	if err != nil {
		logger.Error("parse dqlite cluster", "error", err)
		os.Exit(2)
	}
	runtime, err := localchat.Open(context.Background(), localchat.Config{Backend: localchat.Backend(*backend), DSN: *dsn, DqliteDirectory: *dqliteDirectory, DqliteAddress: *dqliteAddress, DqliteCluster: cluster, DqliteDatabase: *dqliteDatabase, BlobDirectory: *blobDirectory, BlobS3Bucket: *blobS3Bucket, BlobS3Prefix: *blobS3Prefix, BlobMaxBytes: *blobMaxBytes})
	if err != nil {
		logger.Error("open blob cleanup state", "error", err)
		os.Exit(1)
	}
	defer runtime.Closer.Close()
	if runtime.BlobStore == nil {
		logger.Error("blob cleanup state did not provide blob storage")
		os.Exit(1)
	}
	objects, ok := runtime.BlobStore.(blob.Store)
	if !ok {
		logger.Error("blob cleanup state provided an invalid blob store")
		os.Exit(1)
	}
	if *enqueueOrphans && !*audit {
		logger.Error("-enqueue-orphans requires -audit")
		os.Exit(2)
	}
	if *audit {
		listStore, ok := runtime.BlobStore.(blob.WalkStore)
		if !ok {
			logger.Error("selected blob store does not support reconciliation")
			os.Exit(1)
		}
		reconciler, err := blob.NewReconciler(runtime.Store, listStore, runtime.Store, *maxResults)
		if err != nil {
			logger.Error("configure blob reconciliation", "error", err)
			os.Exit(2)
		}
		result, err := reconciler.Audit(context.Background(), domain.WorkspaceID(*workspace))
		if err != nil {
			logger.Error("audit blobs", "error", err)
			os.Exit(1)
		}
		logger.Info("blob audit", "objects", result.Objects, "references", result.References, "orphans", len(result.OrphanKeys), "missing", len(result.MissingKeys), "duplicates", result.DuplicateKeys)
		if *enqueueOrphans {
			count, err := reconciler.EnqueueOrphans(context.Background(), domain.WorkspaceID(*workspace), result)
			if err != nil {
				logger.Error("enqueue orphan cleanup", "count", count, "error", err)
				os.Exit(1)
			}
			logger.Info("enqueued orphan cleanup", "count", count)
		}
		if len(result.MissingKeys) != 0 {
			os.Exit(1)
		}
		return
	}
	worker, err := blob.NewCleanupWorker(runtime.CleanupSource, objects, *owner, *limit, *lease)
	if err != nil {
		logger.Error("configure blob cleanup worker", "error", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	ticker := time.NewTicker(*poll)
	defer ticker.Stop()
	for {
		count, err := worker.RunOnce(ctx, domain.WorkspaceID(*workspace))
		if err != nil {
			logger.Error("blob cleanup failed", "count", count, "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
