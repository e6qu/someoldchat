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
	backend := flag.String("store", "", "storage backend: memory, sqlite, or dqlite (required)")
	blobS3Bucket := flag.String("blob-s3-bucket", "", "Amazon Simple Storage Service bucket for file storage")
	blobS3Prefix := flag.String("blob-s3-prefix", "", "Amazon Simple Storage Service key prefix for file storage")
	dsn := flag.String("db", "", "SQLite DSN; required for sqlite")
	dqliteDirectory := flag.String("dqlite-directory", "", "dqlite state directory")
	dqliteAddress := flag.String("dqlite-address", "", "dqlite node address")
	dqliteCluster := flag.String("dqlite-cluster", "", "comma-separated dqlite cluster addresses")
	dqliteDatabase := flag.String("dqlite-database", "", "dqlite database name")
	blobDirectory := flag.String("blob-dir", "", "external blob directory (required)")
	blobMaxBytes := flag.Int64("blob-max-bytes", 100<<20, "maximum individual blob size")
	workspace := flag.String("workspace", "", "workspace ID (required)")
	owner := flag.String("owner", "", "unique cleanup worker owner ID (required)")
	limit := flag.Int("batch-size", 100, "bounded cleanup batch size")
	lease := flag.Duration("lease", 30*time.Second, "durable cleanup lease")
	poll := flag.Duration("poll", 250*time.Millisecond, "poll interval")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *backend == "" || *blobDirectory == "" || *workspace == "" || *owner == "" || *limit <= 0 || *lease <= 0 || *poll <= 0 {
		logger.Error("blobgc requires explicit store, blob directory, workspace, owner, and positive limits")
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
