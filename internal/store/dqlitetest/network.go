//go:build dqlite

// Package dqlitetest provides real, pre-bound TCP transports for dqlite
// qualification clusters.
package dqlitetest

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"

	"github.com/canonical/go-dqlite/v3/app"
	"github.com/canonical/go-dqlite/v3/client"
)

type Network struct {
	ctx       context.Context
	cancel    context.CancelFunc
	dial      client.DialFunc
	nodes     []*node
	wait      sync.WaitGroup
	closeOnce sync.Once
	errMu     sync.Mutex
	closeErr  error
}

type node struct {
	listener net.Listener
	mu       sync.RWMutex
	session  *session
}

type session struct {
	accept   chan net.Conn
	ready    chan struct{}
	done     chan struct{}
	activate sync.Once
	close    sync.Once
	closeErr error
	wait     sync.WaitGroup
}

type Connection struct {
	Address string
	Dial    client.DialFunc
	Accept  chan net.Conn
	node    *node
	session *session
}

func (c Connection) Option() app.Option {
	return app.WithExternalConn(c.Dial, c.Accept)
}

func (c Connection) Activate() {
	c.session.activate.Do(func() { close(c.session.ready) })
}

func (c Connection) Deactivate() error {
	c.node.mu.Lock()
	if c.node.session == c.session {
		c.node.session = nil
	}
	c.node.mu.Unlock()
	return closeSession(c.session)
}

func NewNetwork(size int) (*Network, error) {
	if size < 1 {
		return nil, errors.New("dqlite test network requires at least one node")
	}
	ctx, cancel := context.WithCancel(context.Background())
	network := &Network{ctx: ctx, cancel: cancel, nodes: make([]*node, 0, size)}
	dialer := &net.Dialer{}
	network.dial = func(ctx context.Context, address string) (net.Conn, error) {
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address, nil)
		if err != nil {
			_ = connection.Close()
			return nil, err
		}
		request.Header.Set("Upgrade", "dqlite")
		if err := request.Write(connection); err != nil {
			_ = connection.Close()
			return nil, err
		}
		response, err := http.ReadResponse(bufio.NewReader(connection), request)
		if err != nil {
			_ = connection.Close()
			return nil, err
		}
		if response.StatusCode != http.StatusSwitchingProtocols {
			_ = response.Body.Close()
			_ = connection.Close()
			return nil, fmt.Errorf("dqlite transport upgrade returned %s", response.Status)
		}
		return connection, nil
	}
	for range size {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			_ = network.Close()
			return nil, err
		}
		entry := &node{listener: listener}
		network.nodes = append(network.nodes, entry)
		network.wait.Add(1)
		go network.serve(entry)
	}
	return network, nil
}

func (n *Network) Addresses() []string {
	addresses := make([]string, len(n.nodes))
	for i, node := range n.nodes {
		addresses[i] = node.listener.Addr().String()
	}
	return addresses
}

func (n *Network) Connections() []Connection {
	connections := make([]Connection, len(n.nodes))
	for i, node := range n.nodes {
		node.mu.Lock()
		previous := node.session
		current := &session{accept: make(chan net.Conn), ready: make(chan struct{}), done: make(chan struct{})}
		node.session = current
		connections[i] = Connection{Address: node.listener.Addr().String(), Dial: n.dial, Accept: current.accept, node: node, session: current}
		node.mu.Unlock()
		n.addCloseError(closeSession(previous))
	}
	return connections
}

func (n *Network) Close() error {
	n.closeOnce.Do(func() {
		n.cancel()
		for _, node := range n.nodes {
			n.addCloseError(node.listener.Close())
		}
		n.wait.Wait()
		for _, node := range n.nodes {
			node.mu.Lock()
			current := node.session
			node.session = nil
			node.mu.Unlock()
			n.addCloseError(closeSession(current))
		}
	})
	n.errMu.Lock()
	defer n.errMu.Unlock()
	return n.closeErr
}

func (n *Network) serve(node *node) {
	defer n.wait.Done()
	for {
		connection, err := node.listener.Accept()
		if err != nil {
			if n.ctx.Err() == nil {
				n.addCloseError(err)
			}
			return
		}
		n.wait.Add(1)
		go n.route(node, connection)
	}
}

func (n *Network) addCloseError(err error) {
	if err == nil {
		return
	}
	n.errMu.Lock()
	n.closeErr = errors.Join(n.closeErr, err)
	n.errMu.Unlock()
}

func (n *Network) route(node *node, connection net.Conn) {
	defer n.wait.Done()
	request, err := http.ReadRequest(bufio.NewReader(connection))
	if err != nil {
		_ = connection.Close()
		return
	}
	_ = request.Body.Close()
	if request.Header.Get("Upgrade") != "dqlite" {
		_ = connection.Close()
		return
	}
	node.mu.RLock()
	current := node.session
	if current != nil {
		current.wait.Add(1)
	}
	node.mu.RUnlock()
	if current == nil {
		_ = connection.Close()
		return
	}
	defer current.wait.Done()
	select {
	case <-current.ready:
	case <-current.done:
		_ = connection.Close()
		return
	case <-n.ctx.Done():
		_ = connection.Close()
		return
	}
	select {
	case current.accept <- connection:
	case <-current.done:
		_ = connection.Close()
	case <-n.ctx.Done():
		_ = connection.Close()
	}
}

func closeSession(current *session) error {
	if current == nil {
		return nil
	}
	current.close.Do(func() {
		close(current.done)
		current.wait.Wait()
		select {
		case <-current.ready:
			current.closeErr = drainAcceptLoop(current.accept)
		default:
		}
		close(current.accept)
	})
	return current.closeErr
}

func drainAcceptLoop(accept chan net.Conn) error {
	local, remote := net.Pipe()
	accept <- local
	drain, err := client.New(context.Background(), "drain", client.WithDialFunc(func(context.Context, string) (net.Conn, error) {
		return remote, nil
	}))
	if err != nil {
		_ = remote.Close()
		_ = local.Close()
		return err
	}
	return drain.Close()
}
