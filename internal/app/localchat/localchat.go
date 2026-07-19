package localchat

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/sameoldchat/sameoldchat/internal/auth"
	"github.com/sameoldchat/sameoldchat/internal/blob"
	"github.com/sameoldchat/sameoldchat/internal/domain"
	"github.com/sameoldchat/sameoldchat/internal/generated"
	chatapi "github.com/sameoldchat/sameoldchat/internal/modules/chat/api"
	"github.com/sameoldchat/sameoldchat/internal/outbox"
	"github.com/sameoldchat/sameoldchat/internal/scheduler"
	"github.com/sameoldchat/sameoldchat/internal/store"
	"github.com/sameoldchat/sameoldchat/internal/store/memory"
	"github.com/sameoldchat/sameoldchat/internal/store/postgres"
	"github.com/sameoldchat/sameoldchat/internal/store/sqlstore"
)

type Runtime struct {
	Service         chatapi.Service
	Store           store.Store
	Closer          io.Closer
	TokenStore      auth.TokenStore
	TokenSeeder     TokenSeeder
	SessionStore    auth.SessionStore
	SessionRevoker  auth.SessionRevoker
	SessionSeeder   SessionSeeder
	OutboxSource    outbox.Source
	CleanupSource   blob.CleanupSource
	ScheduledSource scheduler.Source
	BlobStore       blob.Store
}

type Backend string

const (
	BackendMemory     Backend = "memory"
	BackendSQLite     Backend = "sqlite"
	BackendPostgreSQL Backend = "postgresql"
	BackendDqlite     Backend = "dqlite"
)

type Config struct {
	Backend             Backend
	DSN                 string
	DqliteDirectory     string
	DqliteAddress       string
	DqliteCluster       []string
	DqliteDatabase      string
	BlobDirectory       string
	BlobS3Bucket        string
	BlobS3Prefix        string
	BlobMaxBytes        int64
	BootstrapAdminEmail string
}

func ParseCluster(value string) ([]string, error) {
	if value == "" {
		return []string{}, nil
	}
	parts := strings.Split(value, ",")
	cluster := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, errors.New("dqlite cluster contains an empty address")
		}
		if _, exists := seen[part]; exists {
			return nil, fmt.Errorf("dqlite cluster contains duplicate address %q", part)
		}
		seen[part] = struct{}{}
		cluster = append(cluster, part)
	}
	return cluster, nil
}

type TokenSeeder interface {
	SeedToken(context.Context, string, domain.TokenRecord) error
}
type SessionSeeder interface {
	SeedSession(context.Context, string, domain.SessionRecord) error
}

type bootstrapStore interface {
	SeedWorkspace(context.Context, domain.Workspace) error
	SeedUser(context.Context, domain.User) error
	SeedConversation(context.Context, domain.Conversation) error
}

func Open(ctx context.Context, config Config) (Runtime, error) {
	if config.Backend != BackendMemory && config.Backend != BackendSQLite && config.Backend != BackendPostgreSQL && config.Backend != BackendDqlite {
		return Runtime{}, fmt.Errorf("unsupported local storage backend %q", config.Backend)
	}
	if config.Backend == BackendMemory && (config.DSN != "" || config.DqliteDirectory != "" || config.DqliteAddress != "" || len(config.DqliteCluster) != 0 || config.DqliteDatabase != "") {
		return Runtime{}, errors.New("memory storage does not accept database settings")
	}
	if config.Backend == BackendSQLite && (config.DSN == "" || config.DqliteDirectory != "" || config.DqliteAddress != "" || len(config.DqliteCluster) != 0 || config.DqliteDatabase != "") {
		return Runtime{}, errors.New("SQLite storage requires only a DSN")
	}
	if config.Backend == BackendPostgreSQL && (config.DSN == "" || config.DqliteDirectory != "" || config.DqliteAddress != "" || len(config.DqliteCluster) != 0 || config.DqliteDatabase != "") {
		return Runtime{}, errors.New("PostgreSQL storage requires only a DSN")
	}
	if config.Backend == BackendDqlite && (config.DSN != "" || config.DqliteDirectory == "" || config.DqliteAddress == "" || config.DqliteDatabase == "") {
		return Runtime{}, errors.New("dqlite storage requires directory, address, and database; cluster seeds are optional for the bootstrap node")
	}
	var chatStore store.Store
	var closer io.Closer
	blobStore, err := openBlobStore(ctx, config)
	if err != nil {
		return Runtime{}, err
	}
	switch config.Backend {
	case BackendMemory:
		memoryStore := memory.New()
		memoryStore.SeedWorkspace(domain.Workspace{ID: "Tdev", Name: "SameOldChat"})
		memoryStore.SeedUser(domain.User{ID: "Udev", WorkspaceID: "Tdev", Email: strings.TrimSpace(config.BootstrapAdminEmail), Name: "sameoldchat", RealName: "SameOldChat"})
		memoryStore.SeedConversation(domain.Conversation{ID: "Cdev", WorkspaceID: "Tdev", Name: "general"})
		chatStore, closer = memoryStore, memoryCloser{}
	case BackendSQLite:
		sqlStore, err := sqlstore.Open(ctx, config.DSN)
		if err != nil {
			return Runtime{}, err
		}
		chatStore, closer = sqlStore, sqlStore
	case BackendPostgreSQL:
		postgresStore, err := postgres.Open(ctx, config.DSN)
		if err != nil {
			return Runtime{}, err
		}
		chatStore, closer = postgresStore, postgresStore
	case BackendDqlite:
		var err error
		chatStore, closer, err = openDqlite(ctx, config)
		if err != nil {
			return Runtime{}, err
		}
	}
	if config.Backend == BackendSQLite || config.Backend == BackendPostgreSQL || config.Backend == BackendDqlite {
		selected, ok := chatStore.(bootstrapStore)
		if !ok {
			_ = closer.Close()
			return Runtime{}, errors.New("selected SQL store does not support bootstrap")
		}
		if err := bootstrap(ctx, selected, config.BootstrapAdminEmail); err != nil {
			_ = closer.Close()
			return Runtime{}, err
		}
	}
	tokenStore, ok := chatStore.(auth.TokenStore)
	if !ok {
		_ = closer.Close()
		return Runtime{}, errors.New("selected store does not support token lookup")
	}
	tokenSeeder, ok := chatStore.(TokenSeeder)
	if !ok {
		_ = closer.Close()
		return Runtime{}, errors.New("selected store does not support token seeding")
	}
	sessionStore, ok := chatStore.(auth.SessionStore)
	if !ok {
		_ = closer.Close()
		return Runtime{}, errors.New("selected store does not support session lookup")
	}
	sessionRevoker, ok := chatStore.(auth.SessionRevoker)
	if !ok {
		_ = closer.Close()
		return Runtime{}, errors.New("selected store does not support session revocation")
	}
	sessionSeeder, ok := chatStore.(SessionSeeder)
	if !ok {
		_ = closer.Close()
		return Runtime{}, errors.New("selected store does not support session seeding")
	}
	outboxSource, ok := chatStore.(outbox.Source)
	if !ok {
		_ = closer.Close()
		return Runtime{}, errors.New("selected store does not support outbox delivery")
	}
	cleanupSource, ok := chatStore.(blob.CleanupSource)
	if !ok {
		_ = closer.Close()
		return Runtime{}, errors.New("selected store does not support blob cleanup")
	}
	scheduledSource, ok := chatStore.(scheduler.Source)
	if !ok {
		_ = closer.Close()
		return Runtime{}, errors.New("selected store does not support scheduled message execution")
	}
	return Runtime{Service: generated.ProvideChatServiceLocal(chatStore, blobStore), Store: chatStore, Closer: closer, TokenStore: tokenStore, TokenSeeder: tokenSeeder, SessionStore: sessionStore, SessionRevoker: sessionRevoker, SessionSeeder: sessionSeeder, OutboxSource: outboxSource, CleanupSource: cleanupSource, ScheduledSource: scheduledSource, BlobStore: blobStore}, nil
}

func openBlobStore(ctx context.Context, config Config) (blob.Store, error) {
	if config.BlobDirectory != "" && config.BlobS3Bucket != "" {
		return nil, errors.New("blob storage must select filesystem or Amazon Simple Storage Service, not both")
	}
	if config.BlobDirectory == "" && config.BlobS3Bucket == "" {
		return blob.Disabled{}, nil
	}
	if config.BlobMaxBytes <= 0 {
		return nil, errors.New("blob storage requires a positive size limit")
	}
	if config.BlobDirectory != "" {
		return blob.NewFilesystem(config.BlobDirectory, config.BlobMaxBytes)
	}
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load Amazon Simple Storage Service configuration: %w", err)
	}
	return blob.NewS3(s3.NewFromConfig(awsConfig), config.BlobS3Bucket, config.BlobS3Prefix, config.BlobMaxBytes)
}

func bootstrap(ctx context.Context, selected bootstrapStore, adminEmail string) error {
	if err := selected.SeedWorkspace(ctx, domain.Workspace{ID: "Tdev", Name: "SameOldChat"}); err != nil {
		return err
	}
	if err := selected.SeedUser(ctx, domain.User{ID: "Udev", WorkspaceID: "Tdev", Email: strings.TrimSpace(adminEmail), Name: "sameoldchat", RealName: "SameOldChat"}); err != nil {
		return err
	}
	return selected.SeedConversation(ctx, domain.Conversation{ID: "Cdev", WorkspaceID: "Tdev", Name: "general"})
}

type memoryCloser struct{}

func (memoryCloser) Close() error { return nil }
