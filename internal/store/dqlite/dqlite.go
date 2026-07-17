//go:build dqlite

package dqlite

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/canonical/go-dqlite/v3/app"
	"github.com/canonical/go-dqlite/v3/client"
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

type Health struct {
	Leader          string
	Nodes           int
	Voters          int
	ReachableVoters int
	Quorum          bool
}

func Open(ctx context.Context, config Config) (*Store, error) {
	if strings.TrimSpace(config.Directory) == "" || strings.TrimSpace(config.Address) == "" || strings.TrimSpace(config.Database) == "" {
		return nil, errors.New("dqlite requires directory, address, and database; cluster seeds are optional for the bootstrap node")
	}
	options := []app.Option{app.WithAddress(config.Address), app.WithVoters(3)}
	options = append(options, app.WithCluster(append([]string(nil), config.Cluster...)))
	application, err := app.New(config.Directory, options...)
	if err != nil {
		return nil, err
	}
	database, err := application.Open(ctx, config.Database)
	if err != nil {
		return nil, errors.Join(err, application.Close())
	}
	repositories, err := sqlstore.FromDqliteDB(ctx, database)
	if err != nil {
		return nil, errors.Join(err, database.Close(), application.Close())
	}
	return &Store{Store: repositories, application: application, database: database}, nil
}

func (s *Store) Close() error {
	dbErr := s.database.Close()
	appErr := s.application.Close()
	return errors.Join(dbErr, appErr)
}

func (s *Store) Health(ctx context.Context) (health Health, err error) {
	leaderClient, err := s.application.FindLeader(ctx)
	if err != nil {
		return Health{}, err
	}
	defer func() {
		if closeErr := leaderClient.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	leader, err := leaderClient.Leader(ctx)
	if err != nil {
		return Health{}, err
	}
	nodes, err := leaderClient.Cluster(ctx)
	if err != nil {
		return Health{}, err
	}
	voters := 0
	reachableVoters := 0
	for _, node := range nodes {
		if node.Role != client.Voter {
			continue
		}
		voters++
		if strings.TrimSpace(node.Address) == "" {
			continue
		}
		member, memberErr := client.New(ctx, node.Address)
		if memberErr != nil {
			if ctx.Err() != nil {
				return Health{}, ctx.Err()
			}
			continue
		}
		if closeErr := member.Close(); closeErr != nil {
			return Health{}, closeErr
		}
		reachableVoters++
	}
	quorum := voters >= 3 && reachableVoters >= voters/2+1
	return Health{Leader: leader.Address, Nodes: len(nodes), Voters: voters, ReachableVoters: reachableVoters, Quorum: quorum}, nil
}
