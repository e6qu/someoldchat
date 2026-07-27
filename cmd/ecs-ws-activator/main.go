package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/gorilla/websocket"
)

type activator struct {
	ecs       ecsClient
	dynamo    dynamoClient
	control   controlPlane
	cluster   string
	service   string
	family    string
	port      int
	table     string
	startWait time.Duration
	// leaseTTL is short and renewed at a third of it, so a task that dies
	// without running its deferred release cannot hold the application service
	// awake for the rest of the lease.
	leaseTTL time.Duration
	// idleTimeout is the configured idle interval from
	// specs/scale-to-zero.md: hibernation is eligible only after it elapses
	// with no live streams.
	idleTimeout time.Duration
	// streamTimeout bounds how long a proxied WebSocket may go without any
	// evidence that its peer is alive. Both legs are pinged at nine tenths of it
	// and closed when the pong does not arrive.
	streamTimeout time.Duration
	origin        string
	logger        *slog.Logger
	upgrader      websocket.Upgrader
	mu            sync.Mutex
}

type ecsClient interface {
	ListTasks(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
}

type dynamoClient interface {
	PutItem(context.Context, *dynamodb.PutItemInput, ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error)
	DeleteItem(context.Context, *dynamodb.DeleteItemInput, ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error)
	UpdateItem(context.Context, *dynamodb.UpdateItemInput, ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error)
	GetItem(context.Context, *dynamodb.GetItemInput, ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error)
	Scan(context.Context, *dynamodb.ScanInput, ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error)
	TransactWriteItems(context.Context, *dynamodb.TransactWriteItemsInput, ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error)
}

// controlPlane is the lifecycle owner. This binary never changes the desired
// count itself: scaling the application service is one step of a fenced
// hibernate/wake protocol that drains ingress, drains the outbox, snapshots,
// verifies, and publishes a manifest. Driving DesiredCount directly is the blind
// shutdown that specs/scale-to-zero.md forbids, and on a profile where the task
// carries the database volume it is unrecoverable data loss.
type controlPlane interface {
	Activate(context.Context) error
	Hibernate(context.Context) error
}

type endpoint struct {
	address string
	arn     string
}

const (
	scaleDownLock = "scale-down"
	startMarker   = "starting"
	idleMarker    = "idle-since"
)

const maxWebSocketMessageBytes = 4 << 20

// scaleDownLockTTL is deliberately short. The release is a conditional delete
// that can fail on a throttle or a killed task; a long expiry then blocks every
// new connection until it lapses, because acquiring a connection lease checks
// this same item.
const scaleDownLockTTL = 15 * time.Second

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if code := run(logger); code != 0 {
		os.Exit(code)
	}
}

func run(logger *slog.Logger) int {
	listen := flag.String("listen", "", "HTTP listen address behind the TLS-terminating NLB (required)")
	cluster := flag.String("cluster", "", "ECS cluster (required)")
	service := flag.String("service", "", "ECS application service (required)")
	family := flag.String("family", "", "ECS application task-definition family (required)")
	port := flag.Int("port", 0, "application WebSocket port (required)")
	table := flag.String("state-table", "", "DynamoDB lease table (required)")
	startup := flag.Duration("startup-timeout", 0, "maximum application startup wait (required)")
	leaseTTL := flag.Duration("lease-ttl", 90*time.Second, "connection lease expiry, renewed at a third of it")
	idleTimeout := flag.Duration("idle-timeout", 0, "idle interval with no live streams before hibernation is requested (required)")
	activatorURL := flag.String("activator-url", "", "lifecycle activator base URL owning wake and hibernate (required)")
	activatorToken := flag.String("activator-token", "", "control-plane bearer token for the lifecycle activator (required)")
	// -allowed-origin is required. An empty value is not a permissive default: the
	// origin check accepts a request whose Origin equals this value, so an empty
	// value rejects every browser (which always sends one) while still admitting
	// non-browser clients, and the failure appears as an unexplained handshake
	// rejection rather than as bad configuration.
	origin := flag.String("allowed-origin", "", "exact allowed browser Origin, scheme and authority (required)")
	streamTimeout := flag.Duration("stream-timeout", time.Minute, "maximum time a proxied WebSocket may go without a pong or a frame before it is closed")
	flag.Parse()
	configured := settings{
		listen: *listen, cluster: *cluster, service: *service, family: *family, port: *port,
		table: *table, startup: *startup, leaseTTL: *leaseTTL, idleTimeout: *idleTimeout,
		activatorURL: *activatorURL, activatorToken: *activatorToken, origin: *origin,
		streamTimeout: *streamTimeout,
	}.trimmed()
	if err := configured.validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return 2
	}
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "load AWS config: %v\n", err)
		return 1
	}
	a := &activator{
		ecs: ecs.NewFromConfig(cfg), dynamo: dynamodb.NewFromConfig(cfg),
		control: httpControlPlane{base: strings.TrimSuffix(configured.activatorURL, "/"), token: configured.activatorToken, client: &http.Client{Timeout: 30 * time.Second}},
		cluster: configured.cluster, service: configured.service, family: configured.family,
		port: configured.port, table: configured.table, startWait: configured.startup, leaseTTL: configured.leaseTTL, idleTimeout: configured.idleTimeout,
		streamTimeout: configured.streamTimeout, origin: configured.origin, logger: logger,
		upgrader: websocket.Upgrader{ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10, CheckOrigin: func(r *http.Request) bool {
			// A client that sends no Origin is not a browser; the SDK and Socket
			// Mode clients are in that group. A browser must match exactly.
			return allowedOriginPermits(configured.origin, r.Header.Get("Origin"))
		}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", health)
	mux.HandleFunc("GET /", a.handle)
	applicationContext, stopApplication := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopApplication()
	// Idleness is also polled: the last disconnect is not the moment the idle
	// interval elapses, so without a timer a stack that went quiet would stay
	// awake until the next connection closed.
	go a.idleLoop(applicationContext)
	server := &http.Server{Addr: configured.listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return applicationContext }}
	logger.Info("websocket activator listening", "addr", configured.listen, "service", configured.service)
	serverErrors := make(chan error, 1)
	go func() { serverErrors <- server.ListenAndServe() }()
	select {
	case err := <-serverErrors:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("websocket activator stopped", "error", err)
			return 1
		}
	case <-applicationContext.Done():
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancelShutdown()
		if err := server.Shutdown(shutdownContext); err != nil {
			logger.Error("websocket activator shutdown failed", "error", err)
			return 1
		}
	}
	return 0
}

// health answers the load balancer's HTTP probe.
//
// It must answer 200. The NLB target group in deploy/ecs-scale-zero declares
// `matcher = "200"`, which accepts that status and no other, so the 204 this
// route used to return made every edge task permanently unhealthy: with
// deployment_minimum_healthy_percent = 100 and the deployment circuit breaker
// enabled, the service could never converge, no WebSocket traffic was ever
// served, and the unhealthy-target alarm fired continuously.
//
// The body matches the lifecycle activator's own GET /healthz
// (internal/activator/handler.go), so an operator polling either edge sees the
// same shape.
func health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// settings is the configuration of one activator process. It is validated as a
// whole, before any AWS client is built or any listener is bound, so an
// incomplete command line fails at startup with the names of what is missing.
type settings struct {
	listen         string
	cluster        string
	service        string
	family         string
	port           int
	table          string
	startup        time.Duration
	leaseTTL       time.Duration
	idleTimeout    time.Duration
	streamTimeout  time.Duration
	activatorURL   string
	activatorToken string
	origin         string
}

// trimmed normalizes every string setting.
//
// The table name in particular is trimmed before it is compared and before it
// reaches DynamoDB: a value with surrounding whitespace passed the emptiness
// check and then named a table that does not exist, so every lease write and
// every scale-down decision failed at runtime instead of at startup.
func (s settings) trimmed() settings {
	s.listen = strings.TrimSpace(s.listen)
	s.cluster = strings.TrimSpace(s.cluster)
	s.service = strings.TrimSpace(s.service)
	s.family = strings.TrimSpace(s.family)
	s.table = strings.TrimSpace(s.table)
	s.activatorURL = strings.TrimSpace(s.activatorURL)
	s.activatorToken = strings.TrimSpace(s.activatorToken)
	s.origin = strings.TrimSpace(s.origin)
	return s
}

func (s settings) validate() error {
	missing := make([]string, 0, 12)
	for _, required := range []struct {
		flag  string
		empty bool
	}{
		{flag: "-listen", empty: s.listen == ""},
		{flag: "-cluster", empty: s.cluster == ""},
		{flag: "-service", empty: s.service == ""},
		{flag: "-family", empty: s.family == ""},
		{flag: "-port", empty: s.port <= 0},
		{flag: "-state-table", empty: s.table == ""},
		{flag: "-startup-timeout", empty: s.startup <= 0},
		{flag: "-lease-ttl", empty: s.leaseTTL <= 0},
		{flag: "-idle-timeout", empty: s.idleTimeout <= 0},
		{flag: "-stream-timeout", empty: s.streamTimeout <= 0},
		{flag: "-activator-url", empty: s.activatorURL == ""},
		{flag: "-activator-token", empty: s.activatorToken == ""},
		// An empty allowed origin is not a permissive default: it rejects every
		// browser, which always sends an Origin, while still admitting
		// non-browser clients, so the deployment looks reachable while no user
		// can connect.
		{flag: "-allowed-origin", empty: s.origin == ""},
	} {
		if required.empty {
			missing = append(missing, required.flag)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("ecs-ws-activator requires %s", strings.Join(missing, ", "))
	}
	return nil
}

// allowedOriginPermits reports whether a WebSocket handshake may proceed.
//
// An empty configured origin never permits a browser: it is rejected at startup
// instead, because silently admitting only clients that send no Origin turned a
// missing setting into an unexplained handshake failure for every browser.
func allowedOriginPermits(allowed, requested string) bool {
	if strings.TrimSpace(allowed) == "" {
		return false
	}
	requested = strings.TrimSpace(requested)
	return requested == "" || requested == allowed
}

func split(value string) []string {
	parts := make([]string, 0)
	for _, part := range strings.Split(value, ",") {
		if value := strings.TrimSpace(part); value != "" {
			parts = append(parts, value)
		}
	}
	return parts
}

func (a *activator) handle(w http.ResponseWriter, r *http.Request) {
	if !websocket.IsWebSocketUpgrade(r) {
		http.Error(w, "websocket upgrade required", http.StatusBadRequest)
		return
	}
	id, err := randomID()
	if err != nil {
		a.fail(w, err)
		return
	}
	owner, err := randomID()
	if err != nil {
		a.fail(w, err)
		return
	}
	lease := "ws:" + id
	if err := a.acquireLease(r.Context(), lease, owner); err != nil {
		a.fail(w, err)
		return
	}
	leaseContext, cancelLease := context.WithCancel(r.Context())
	defer func() {
		cancelLease()
		cleanupContext, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		if err := a.releaseLease(cleanupContext, lease); err != nil {
			a.logger.Error("release websocket lease failed", "error", err, "lease", lease)
		}
		if err := a.hibernateIfIdle(cleanupContext); err != nil {
			a.logger.Error("request application hibernation failed", "error", err)
		}
	}()
	leaseErrors := make(chan error, 1)
	go a.renewLeaseLoop(leaseContext, lease, owner, leaseErrors)

	backend, err := a.readyEndpoint(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	backendURL := websocketEndpointURL(backend, a.port, r.URL.RequestURI())
	backendHeaders, subprotocols := backendHandshake(r)
	dialer := &websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: subprotocols}
	backendConn, response, err := dialer.DialContext(r.Context(), backendURL, backendHeaders)
	if err != nil {
		a.fail(w, fmt.Errorf("connect application websocket: %w", err))
		return
	}
	defer backendConn.Close()
	clientHeaders := http.Header{}
	if protocol := response.Header.Get("Sec-WebSocket-Protocol"); protocol != "" {
		clientHeaders.Set("Sec-WebSocket-Protocol", protocol)
	}
	clientConn, err := a.upgrader.Upgrade(w, r, clientHeaders)
	if err != nil {
		return
	}
	defer clientConn.Close()
	backendConn.SetReadLimit(maxWebSocketMessageBytes)
	clientConn.SetReadLimit(maxWebSocketMessageBytes)

	writeWait := a.streamTimeout / 6
	if writeWait <= 0 {
		writeWait = time.Second
	}
	clientSide := &peer{conn: clientConn, writeWait: writeWait}
	backendSide := &peer{conn: backendConn, writeWait: writeWait}
	pingPeriod := a.streamTimeout * 9 / 10
	if pingPeriod <= 0 {
		pingPeriod = time.Second
	}
	errorsCh := make(chan error, 4)
	for _, side := range []*peer{clientSide, backendSide} {
		if err := side.watch(a.streamTimeout); err != nil {
			a.logger.Error("arm websocket liveness", "error", err)
			return
		}
	}
	proxyDone := make(chan struct{})
	defer close(proxyDone)
	go clientSide.keepalive(proxyDone, errorsCh, pingPeriod)
	go backendSide.keepalive(proxyDone, errorsCh, pingPeriod)
	go proxyMessages(errorsCh, clientSide, backendSide, a.streamTimeout)
	go proxyMessages(errorsCh, backendSide, clientSide, a.streamTimeout)
	select {
	case err := <-errorsCh:
		if err != nil && !isNormalWebSocketClose(err) {
			a.logger.Info("websocket proxy closed", "error", err)
		}
	case err := <-leaseErrors:
		a.logger.Error("websocket lease renewal failed", "error", err, "lease", lease)
	case <-r.Context().Done():
		_ = clientConn.Close()
		_ = backendConn.Close()
	}
}

// backendHandshake prepares the headers and subprotocols for the backend dial.
//
// The gorilla dialer writes every handshake header itself and rejects a request
// that already carries one, so forwarding the client's Upgrade header made every
// WebSocket activation fail with "duplicate header not allowed: Upgrade". The
// requested subprotocol is returned separately because the dialer is the only
// place it may be set. Everything else the application needs — cookies,
// authorization, forwarding headers — is passed through untouched.
func backendHandshake(r *http.Request) (http.Header, []string) {
	headers := cloneHeaders(r.Header)
	subprotocols := make([]string, 0)
	for _, header := range r.Header.Values("Sec-WebSocket-Protocol") {
		subprotocols = append(subprotocols, split(header)...)
	}
	for _, name := range []string{"Upgrade", "Connection", "Sec-Websocket-Key", "Sec-Websocket-Version", "Sec-Websocket-Extensions", "Sec-Websocket-Protocol"} {
		headers.Del(name)
	}
	return headers, subprotocols
}

func (a *activator) readyEndpoint(ctx context.Context) (endpoint, error) {
	if err := ctx.Err(); err != nil {
		return endpoint{}, err
	}
	// The start marker outlives this request. A client that gives up during a
	// cold start must not look like an idle stack, or the deferred cleanup
	// scales down the tasks that were seconds from ready and the next client
	// repeats it forever.
	if err := a.markStarting(ctx); err != nil {
		return endpoint{}, err
	}
	deadline := time.Now().Add(a.startWait)
	for time.Now().Before(deadline) {
		endpoints, err := a.runningEndpoints(ctx)
		if err != nil {
			return endpoint{}, err
		}
		for _, candidate := range endpoints {
			requestContext, cancel := context.WithTimeout(ctx, time.Second)
			request, err := http.NewRequestWithContext(requestContext, http.MethodGet, readinessURL(candidate, a.port), nil)
			if err != nil {
				cancel()
				return endpoint{}, err
			}
			response, err := http.DefaultClient.Do(request)
			cancel()
			if err == nil {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<10))
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return candidate, nil
				}
			}
		}
		if len(endpoints) == 0 {
			// Re-issued whenever the endpoint set is empty: a task that
			// crash-loops during startup must be driven again, not waited out.
			if err := a.ensureRunning(ctx); err != nil {
				return endpoint{}, err
			}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return endpoint{}, ctx.Err()
		case <-timer.C:
		}
	}
	return endpoint{}, fmt.Errorf("websocket application did not become ready within %s", a.startWait)
}

func readinessURL(candidate endpoint, port int) string {
	return "http://" + net.JoinHostPort(candidate.address, strconv.Itoa(port)) + "/readyz"
}

func websocketEndpointURL(candidate endpoint, port int, requestURI string) string {
	return "ws://" + net.JoinHostPort(candidate.address, strconv.Itoa(port)) + requestURI
}

// ensureRunning asks the lifecycle activator to wake the stack. Restoring the
// verified snapshot and starting the application replicas are its job; this
// binary only reports that traffic has arrived.
func (a *activator) ensureRunning(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	endpoints, err := a.runningEndpoints(ctx)
	if err != nil {
		return err
	}
	if len(endpoints) > 0 {
		return nil
	}
	return a.control.Activate(ctx)
}

func (a *activator) runningEndpoints(ctx context.Context) ([]endpoint, error) {
	input := &ecs.ListTasksInput{Cluster: aws.String(a.cluster), ServiceName: aws.String(a.service), DesiredStatus: ecstypes.DesiredStatusRunning, Family: aws.String(a.family)}
	var taskARNs []string
	for {
		output, err := a.ecs.ListTasks(ctx, input)
		if err != nil {
			return nil, err
		}
		taskARNs = append(taskARNs, output.TaskArns...)
		if output.NextToken == nil || *output.NextToken == "" {
			break
		}
		input.NextToken = output.NextToken
	}
	if len(taskARNs) == 0 {
		return []endpoint{}, nil
	}
	endpoints := make([]endpoint, 0, len(taskARNs))
	for start := 0; start < len(taskARNs); start += 100 {
		end := start + 100
		if end > len(taskARNs) {
			end = len(taskARNs)
		}
		described, err := a.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(a.cluster), Tasks: taskARNs[start:end]})
		if err != nil {
			return nil, err
		}
		for _, task := range described.Tasks {
			if aws.ToString(task.LastStatus) != "RUNNING" || task.TaskArn == nil {
				continue
			}
			for _, attachment := range task.Attachments {
				if aws.ToString(attachment.Type) != "ElasticNetworkInterface" {
					continue
				}
				for _, detail := range attachment.Details {
					if detail.Name != nil && detail.Value != nil && *detail.Name == "privateIPv4Address" {
						endpoints = append(endpoints, endpoint{address: *detail.Value, arn: *task.TaskArn})
					}
				}
			}
		}
	}
	return endpoints, nil
}

func (a *activator) idleLoop(ctx context.Context) {
	interval := a.idleTimeout / 4
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if err := a.hibernateIfIdle(ctx); err != nil && ctx.Err() == nil {
			a.logger.Error("idle hibernation check failed", "error", err)
		}
	}
}

// hibernateIfIdle requests hibernation only when every documented precondition
// this binary can observe holds: no live stream leases, no cold start in
// progress, and the configured idle interval elapsed since the stack went quiet.
// The lifecycle activator owns the remaining eligibility checks and the drain,
// snapshot, verification, and stop sequence.
func (a *activator) hibernateIfIdle(ctx context.Context) error {
	owner, err := randomID()
	if err != nil {
		return err
	}
	acquired, err := a.acquireScaleDownLock(ctx, owner)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer a.releaseScaleDownLock(ctx, owner)
	active, err := a.activeLeases(ctx)
	if err != nil {
		return err
	}
	if active {
		return a.clearIdleSince(ctx)
	}
	starting, err := a.markerActive(ctx, startMarker)
	if err != nil {
		return err
	}
	if starting {
		return nil
	}
	idleSince, err := a.idleSince(ctx)
	if err != nil {
		return err
	}
	if idleSince.IsZero() {
		return a.recordIdleSince(ctx)
	}
	if time.Since(idleSince) < a.idleTimeout {
		return nil
	}
	a.logger.Info("requesting application hibernation", "idle_since", idleSince)
	if err := a.control.Hibernate(ctx); err != nil {
		return err
	}
	return a.clearIdleSince(ctx)
}

func (a *activator) acquireLease(ctx context.Context, lease, owner string) error {
	now := time.Now().Unix()
	_, err := a.dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []dynamodbtypes.TransactWriteItem{
		{ConditionCheck: &dynamodbtypes.ConditionCheck{TableName: aws.String(a.table), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: scaleDownLock}}, ConditionExpression: aws.String("attribute_not_exists(id) OR expires < :now"), ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{":now": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(now)}}}},
		{Put: &dynamodbtypes.Put{TableName: aws.String(a.table), Item: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: lease}, "owner": &dynamodbtypes.AttributeValueMemberS{Value: owner}, "expires": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(a.leaseExpiry())}}, ConditionExpression: aws.String("attribute_not_exists(id)")}},
	}})
	if err != nil {
		return err
	}
	// A live stream means the stack is not idle, so any recorded idle instant is
	// stale and must not later be read as "idle for long enough".
	return a.clearIdleSince(ctx)
}

func (a *activator) leaseExpiry() int64 {
	return time.Now().Add(a.leaseTTL).Unix()
}

func (a *activator) renewLeaseLoop(ctx context.Context, lease, owner string, errorsCh chan<- error) {
	interval := a.leaseTTL / 3
	if interval <= 0 {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := a.renewLease(ctx, lease, owner); err != nil {
				if ctx.Err() == nil {
					errorsCh <- err
				}
				return
			}
		}
	}
}

// renewLease is conditional on ownership. Without it any process that knows the
// lease id can extend it and hold the application service awake.
func (a *activator) renewLease(ctx context.Context, lease, owner string) error {
	_, err := a.dynamo.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:                 aws.String(a.table),
		Key:                       map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: lease}},
		UpdateExpression:          aws.String("SET #expires = :expires"),
		ExpressionAttributeNames:  map[string]string{"#expires": "expires"},
		ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{":expires": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(a.leaseExpiry())}, ":owner": &dynamodbtypes.AttributeValueMemberS{Value: owner}},
		ConditionExpression:       aws.String("attribute_exists(id) AND owner = :owner"),
	})
	return err
}

func (a *activator) acquireScaleDownLock(ctx context.Context, owner string) (bool, error) {
	_, err := a.dynamo.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(a.table), Item: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: scaleDownLock}, "owner": &dynamodbtypes.AttributeValueMemberS{Value: owner}, "expires": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Add(scaleDownLockTTL).Unix())}}, ConditionExpression: aws.String("attribute_not_exists(id) OR expires < :now"), ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{":now": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Unix())}}})
	if err != nil {
		var conditional *dynamodbtypes.ConditionalCheckFailedException
		if errors.As(err, &conditional) {
			return false, nil
		}
		var transaction *dynamodbtypes.TransactionConflictException
		if errors.As(err, &transaction) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// releaseScaleDownLock retries: a dropped release leaves a lock that blocks every
// new connection until it expires, so it is worth more than one attempt even
// though the short TTL bounds the damage.
func (a *activator) releaseScaleDownLock(ctx context.Context, owner string) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 {
			timer := time.NewTimer(time.Duration(attempt) * 100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				a.logger.Error("release websocket scale-down lock failed", "error", errors.Join(lastErr, ctx.Err()))
				return
			case <-timer.C:
			}
		}
		_, err := a.dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(a.table), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: scaleDownLock}}, ConditionExpression: aws.String("owner = :owner"), ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{":owner": &dynamodbtypes.AttributeValueMemberS{Value: owner}}})
		if err == nil {
			return
		}
		lastErr = err
	}
	a.logger.Error("release websocket scale-down lock failed", "error", lastErr)
}

func (a *activator) activeLeases(ctx context.Context) (bool, error) {
	input := &dynamodb.ScanInput{TableName: aws.String(a.table), ConsistentRead: aws.Bool(true), FilterExpression: aws.String("begins_with(id, :prefix) AND expires > :now"), ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{":prefix": &dynamodbtypes.AttributeValueMemberS{Value: "ws:"}, ":now": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Unix())}}}
	for {
		output, err := a.dynamo.Scan(ctx, input)
		if err != nil {
			return false, err
		}
		if len(output.Items) > 0 {
			return true, nil
		}
		if len(output.LastEvaluatedKey) == 0 {
			return false, nil
		}
		input.ExclusiveStartKey = output.LastEvaluatedKey
	}
}

func (a *activator) markStarting(ctx context.Context) error {
	_, err := a.dynamo.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(a.table), Item: map[string]dynamodbtypes.AttributeValue{
		"id":      &dynamodbtypes.AttributeValueMemberS{Value: startMarker},
		"expires": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Add(a.startWait).Unix())},
	}})
	return err
}

func (a *activator) markerActive(ctx context.Context, id string) (bool, error) {
	output, err := a.dynamo.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(a.table), ConsistentRead: aws.Bool(true), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: id}}})
	if err != nil {
		return false, err
	}
	expires, ok := numberAttribute(output.Item, "expires")
	if !ok {
		return false, nil
	}
	return expires > time.Now().Unix(), nil
}

func (a *activator) recordIdleSince(ctx context.Context) error {
	_, err := a.dynamo.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(a.table), Item: map[string]dynamodbtypes.AttributeValue{
		"id":    &dynamodbtypes.AttributeValueMemberS{Value: idleMarker},
		"since": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Unix())},
	}})
	return err
}

func (a *activator) idleSince(ctx context.Context) (time.Time, error) {
	output, err := a.dynamo.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(a.table), ConsistentRead: aws.Bool(true), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: idleMarker}}})
	if err != nil {
		return time.Time{}, err
	}
	since, ok := numberAttribute(output.Item, "since")
	if !ok {
		return time.Time{}, nil
	}
	return time.Unix(since, 0), nil
}

func (a *activator) clearIdleSince(ctx context.Context) error {
	_, err := a.dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(a.table), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: idleMarker}}})
	return err
}

func numberAttribute(item map[string]dynamodbtypes.AttributeValue, name string) (int64, bool) {
	raw, ok := item[name]
	if !ok {
		return 0, false
	}
	number, ok := raw.(*dynamodbtypes.AttributeValueMemberN)
	if !ok {
		return 0, false
	}
	value, err := strconv.ParseInt(number.Value, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

func (a *activator) releaseLease(ctx context.Context, lease string) error {
	_, err := a.dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(a.table), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: lease}}})
	return err
}

func (a *activator) fail(w http.ResponseWriter, err error) {
	a.logger.Error("websocket activation failed", "error", err)
	w.Header().Set("Retry-After", "1")
	http.Error(w, "websocket activation unavailable", http.StatusServiceUnavailable)
}

// peer owns one side of a proxied stream.
//
// Gorilla permits one concurrent writer per connection, and this proxy now has
// two: the goroutine forwarding the other side's frames, and the keepalive that
// pings this side. The mutex is what makes both legal on the same connection;
// without it the added pings would corrupt the frame stream.
type peer struct {
	conn      *websocket.Conn
	writeMu   sync.Mutex
	writeWait time.Duration
}

func (p *peer) send(messageType int, payload []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	if err := p.conn.SetWriteDeadline(time.Now().Add(p.writeWait)); err != nil {
		return err
	}
	return p.conn.WriteMessage(messageType, payload)
}

// watch arms the liveness contract for the side this peer reads.
//
// Neither leg had a read or write deadline and neither generated pings, while
// renewLeaseLoop extended the connection lease regardless of peer health. One
// half-open client — a laptop lid closed on a train, an NLB idle-timeout on a
// silent flow — therefore pinned a lease forever and held the whole application
// service awake at full replica cost, which is precisely the outcome
// scale-to-zero exists to prevent. A missed pong now closes the stream, releases
// the lease, and lets the stack hibernate.
func (p *peer) watch(pongWait time.Duration) error {
	if err := p.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
		return err
	}
	p.conn.SetPongHandler(func(string) error { return p.conn.SetReadDeadline(time.Now().Add(pongWait)) })
	// The default ping handler replies on the connection directly, which would
	// bypass the write mutex the keepalive relies on.
	p.conn.SetPingHandler(func(payload string) error {
		if err := p.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			return err
		}
		err := p.send(websocket.PongMessage, []byte(payload))
		if errors.Is(err, websocket.ErrCloseSent) {
			return nil
		}
		return err
	})
	return nil
}

func (p *peer) keepalive(done <-chan struct{}, errorsCh chan<- error, pingPeriod time.Duration) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := p.send(websocket.PingMessage, nil); err != nil {
				errorsCh <- err
				return
			}
		}
	}
}

func proxyMessages(errorsCh chan<- error, from, to *peer, pongWait time.Duration) {
	for {
		messageType, message, err := from.conn.ReadMessage()
		if err != nil {
			errorsCh <- err
			return
		}
		// Traffic is liveness too: a busy stream must not be closed just because
		// the pong for an unsent ping has not arrived.
		if err := from.conn.SetReadDeadline(time.Now().Add(pongWait)); err != nil {
			errorsCh <- err
			return
		}
		if err := to.send(messageType, message); err != nil {
			errorsCh <- err
			return
		}
	}
}

func cloneHeaders(source http.Header) http.Header {
	result := make(http.Header, len(source))
	for key, values := range source {
		result[key] = append([]string(nil), values...)
	}
	return result
}

// randomID is unguessable. The previous implementation was FNV-1a over the
// remote address and the current time, so two connections from one address in the
// same nanosecond collided: the second was refused, and two scale-down owners
// could each delete the other's lock.
func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

// isNormalWebSocketClose unwraps: a bare type assertion misses a wrapped close
// error, so ordinary client disconnects were logged as proxy failures.
func isNormalWebSocketClose(err error) bool {
	var closeError *websocket.CloseError
	if !errors.As(err, &closeError) {
		return false
	}
	return closeError.Code == websocket.CloseNormalClosure || closeError.Code == websocket.CloseGoingAway
}

type httpControlPlane struct {
	base   string
	token  string
	client *http.Client
}

func (c httpControlPlane) Activate(ctx context.Context) error { return c.post(ctx, "/activate") }

func (c httpControlPlane) Hibernate(ctx context.Context) error { return c.post(ctx, "/hibernate") }

func (c httpControlPlane) post(ctx context.Context, path string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	switch {
	case response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusAccepted:
		return nil
	case response.StatusCode == http.StatusConflict:
		// The lifecycle owner declined: the stack is not in an eligible state.
		// That is an answer, not a failure.
		return nil
	default:
		return fmt.Errorf("lifecycle activator %s returned %d", path, response.StatusCode)
	}
}
