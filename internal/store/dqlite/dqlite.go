//go:build dqlite

package dqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/canonical/go-dqlite/v3/app"
	"github.com/sameoldchat/sameoldchat/internal/store/sqlstore"
)

type Config struct {
	Directory string
	Address   string
	Cluster   []string
	Database  string
}

type Store struct {
	*sqlstore.Store
	application *app.App
	database    *sql.DB
}

func Open(ctx context.Context, config Config) (*Store, error) {
	if strings.TrimSpace(config.Directory) == "" || strings.TrimSpace(config.Address) == "" || strings.TrimSpace(config.Database) == "" || len(config.Cluster) != 3 {
		return nil, errors.New("dqlite requires directory, address, database, and exactly three cluster addresses")
	}
	options := []app.Option{app.WithAddress(config.Address), app.WithVoters(3)}
	options = append(options, app.WithCluster(append([]string(nil), config.Cluster...)))
	application, err := app.New(config.Directory, options...)
	if err != nil {
		return nil, err
	}
	database, err := application.Open(ctx, config.Database)
	if err != nil {
		_ = application.Close()
		return nil, err
	}
	repositories, err := sqlstore.FromDB(ctx, database)
	if err != nil {
		_ = database.Close()
		_ = application.Close()
		return nil, err
	}
	return &Store{Store: repositories, application: application, database: database}, nil
}

func (s *Store) Close() error {
	dbErr := s.database.Close()
	appErr := s.application.Close()
	if dbErr != nil {
		return dbErr
	}
	return appErr
}
