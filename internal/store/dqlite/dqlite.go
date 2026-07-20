//go:build dqlite

package dqlite

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"

	"github.com/canonical/go-dqlite/v3/app"
	"github.com/canonical/go-dqlite/v3/client"
	"github.com/sameoldchat/sameoldchat/internal/store/sqlstore"
)

type Config struct {
	Directory      string
	Address        string
	Cluster        []string
	Database       string
	ExternalDial   client.DialFunc
	ExternalAccept chan net.Conn
	ExternalReady  func()
	ExternalClose  func() error
}

type Store struct {
	*sqlstore.Store
	application   *app.App
	database      *sql.DB
	dial          client.DialFunc
	externalClose func() error
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
	externalFields := 0
	for _, configured := range []bool{config.ExternalDial != nil, config.ExternalAccept != nil, config.ExternalReady != nil, config.ExternalClose != nil} {
		if configured {
			externalFields++
		}
	}
	if externalFields != 0 && externalFields != 4 {
		return nil, errors.New("dqlite external transport requires dial, accept, ready, and close")
	}
	options := []app.Option{app.WithAddress(config.Address), app.WithVoters(3)}
	options = append(options, app.WithCluster(append([]string(nil), config.Cluster...)))
	dial := client.DefaultDialFunc
	if config.ExternalDial != nil {
		options = append(options, app.WithExternalConn(config.ExternalDial, config.ExternalAccept))
		dial = config.ExternalDial
	}
	application, err := app.New(config.Directory, options...)
	if err != nil {
		return nil, errors.Join(err, closeExternal(config.ExternalClose))
	}
	if config.ExternalReady != nil {
		config.ExternalReady()
	}
	database, err := application.Open(ctx, config.Database)
	if err != nil {
		return nil, errors.Join(err, closeExternal(config.ExternalClose), application.Close())
	}
	repositories, err := sqlstore.FromDqliteDB(ctx, database)
	if err != nil {
		return nil, errors.Join(err, database.Close(), closeExternal(config.ExternalClose), application.Close())
	}
	return &Store{Store: repositories, application: application, database: database, dial: dial, externalClose: config.ExternalClose}, nil
}

func (s *Store) Close() error {
	dbErr := s.database.Close()
	externalErr := closeExternal(s.externalClose)
	appErr := s.application.Close()
	return errors.Join(dbErr, externalErr, appErr)
}

func closeExternal(close func() error) error {
	if close == nil {
		return nil
	}
	return close()
}

func (s *Store) Health(ctx context.Context) (Health, error) {
	leaderClient, err := s.application.FindLeader(ctx)
	if err != nil {
		return Health{}, err
	}
	defer leaderClient.Close()
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
		member, memberErr := client.New(ctx, node.Address, client.WithDialFunc(s.dial))
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
