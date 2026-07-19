package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
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
	ecs       *ecs.Client
	dynamo    *dynamodb.Client
	cluster   string
	service   string
	family    string
	port      int
	subnets   []string
	security  []string
	table     string
	replicas  int32
	startWait time.Duration
	origin    string
	logger    *slog.Logger
	upgrader  websocket.Upgrader
	mu        sync.Mutex
}

type endpoint struct {
	address string
	arn     string
}

const scaleDownLock = "scale-down"

func main() {
	listen := flag.String("listen", "", "HTTP listen address behind the TLS-terminating NLB (required)")
	cluster := flag.String("cluster", "", "ECS cluster (required)")
	service := flag.String("service", "", "ECS application service (required)")
	family := flag.String("family", "", "ECS application task-definition family (required)")
	port := flag.Int("port", 0, "application WebSocket port (required)")
	subnets := flag.String("subnets", "", "comma-separated application subnet IDs (required)")
	security := flag.String("security-groups", "", "comma-separated application security-group IDs (required)")
	table := flag.String("state-table", "", "DynamoDB lease table (required)")
	replicas := flag.Int("replicas", 0, "application replica count while awake (required)")
	startup := flag.Duration("startup-timeout", 0, "maximum application startup wait (required)")
	origin := flag.String("allowed-origin", "", "exact allowed browser Origin; empty permits clients without Origin only")
	flag.Parse()
	if strings.TrimSpace(*listen) == "" || strings.TrimSpace(*cluster) == "" || strings.TrimSpace(*service) == "" || strings.TrimSpace(*family) == "" || *port <= 0 || *table == "" || *replicas <= 0 || *startup <= 0 || len(split(*subnets)) == 0 || len(split(*security)) == 0 {
		fmt.Fprintln(os.Stderr, "ecs-ws-activator requires explicit listen, ECS, network, state, replica, and timeout settings")
		os.Exit(2)
	}
	cfg, err := config.LoadDefaultConfig(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "load AWS config: %v\n", err)
		os.Exit(1)
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	a := &activator{
		ecs: ecs.NewFromConfig(cfg), dynamo: dynamodb.NewFromConfig(cfg), cluster: *cluster, service: *service, family: *family,
		port: *port, subnets: split(*subnets), security: split(*security), table: *table, replicas: int32(*replicas), startWait: *startup, origin: *origin, logger: logger,
		upgrader: websocket.Upgrader{ReadBufferSize: 32 << 10, WriteBufferSize: 32 << 10, CheckOrigin: func(r *http.Request) bool {
			return r.Header.Get("Origin") == "" || r.Header.Get("Origin") == *origin
		}},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /", a.handle)
	server := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	logger.Info("websocket activator listening", "addr", *listen, "service", *service)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("websocket activator stopped", "error", err)
		os.Exit(1)
	}
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
	lease := "ws:" + randomID(r)
	if err := a.acquireLease(r.Context(), lease); err != nil {
		a.fail(w, err)
		return
	}
	defer func() {
		if err := a.releaseLease(context.Background(), lease); err != nil {
			a.logger.Error("release websocket lease failed", "error", err, "lease", lease)
		}
		if err := a.scaleDownIfIdle(context.Background()); err != nil {
			a.logger.Error("scale websocket service to zero failed", "error", err)
		}
	}()

	backend, err := a.readyEndpoint(r.Context())
	if err != nil {
		a.fail(w, err)
		return
	}
	backendURL := websocketEndpointURL(backend, a.port, r.URL.RequestURI())
	backendHeaders := cloneHeaders(r.Header)
	backendHeaders.Del("Connection")
	backendHeaders.Del("Sec-WebSocket-Key")
	backendHeaders.Del("Sec-WebSocket-Version")
	backendHeaders.Del("Sec-WebSocket-Extensions")
	backendConn, response, err := (&websocket.Dialer{HandshakeTimeout: 5 * time.Second}).DialContext(r.Context(), backendURL, backendHeaders)
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

	errorsCh := make(chan error, 2)
	go proxyMessages(errorsCh, clientConn, backendConn)
	go proxyMessages(errorsCh, backendConn, clientConn)
	if err := <-errorsCh; err != nil && !isNormalWebSocketClose(err) {
		a.logger.Info("websocket proxy closed", "error", err)
	}
}

func (a *activator) readyEndpoint(ctx context.Context) (endpoint, error) {
	deadline := time.Now().Add(a.startWait)
	started := false
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
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return candidate, nil
				}
			}
		}
		if len(endpoints) == 0 && !started {
			if err := a.ensureRunning(ctx); err != nil {
				return endpoint{}, err
			}
			started = true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return endpoint{}, fmt.Errorf("websocket application did not become ready within %s", a.startWait)
}

func readinessURL(candidate endpoint, port int) string {
	return "http://" + net.JoinHostPort(candidate.address, strconv.Itoa(port)) + "/readyz"
}

func websocketEndpointURL(candidate endpoint, port int, requestURI string) string {
	return "ws://" + net.JoinHostPort(candidate.address, strconv.Itoa(port)) + requestURI
}

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
	_, err = a.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(a.cluster), Service: aws.String(a.service), DesiredCount: aws.Int32(a.replicas)})
	return err
}

func (a *activator) runningEndpoints(ctx context.Context) ([]endpoint, error) {
	input := &ecs.ListTasksInput{Cluster: aws.String(a.cluster), ServiceName: aws.String(a.service), DesiredStatus: ecstypes.DesiredStatusRunning, Family: aws.String(a.family)}
	output, err := a.ecs.ListTasks(ctx, input)
	if err != nil {
		return nil, err
	}
	if len(output.TaskArns) == 0 {
		return nil, nil
	}
	described, err := a.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(a.cluster), Tasks: output.TaskArns})
	if err != nil {
		return nil, err
	}
	endpoints := make([]endpoint, 0, len(described.Tasks))
	for _, task := range described.Tasks {
		if task.LastStatus != aws.String("RUNNING") || task.TaskArn == nil {
			continue
		}
		for _, attachment := range task.Attachments {
			if attachment.Type == nil || *attachment.Type != "ElasticNetworkInterface" {
				continue
			}
			for _, detail := range attachment.Details {
				if detail.Name != nil && detail.Value != nil && *detail.Name == "privateIPv4Address" {
					endpoints = append(endpoints, endpoint{address: *detail.Value, arn: *task.TaskArn})
				}
			}
		}
	}
	return endpoints, nil
}

func (a *activator) scaleDownIfIdle(ctx context.Context) error {
	owner := "scale-down:" + randomID(nil)
	acquired, err := a.acquireScaleDownLock(ctx, owner)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer func() {
		if _, releaseErr := a.dynamo.DeleteItem(context.Background(), &dynamodb.DeleteItemInput{TableName: aws.String(a.table), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: scaleDownLock}}, ConditionExpression: aws.String("owner = :owner"), ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{":owner": &dynamodbtypes.AttributeValueMemberS{Value: owner}}}); releaseErr != nil {
			a.logger.Error("release websocket scale-down lock failed", "error", releaseErr)
		}
	}()
	active, err := a.activeLeases(ctx)
	if err != nil {
		return err
	}
	if active {
		return nil
	}
	_, err = a.ecs.UpdateService(ctx, &ecs.UpdateServiceInput{Cluster: aws.String(a.cluster), Service: aws.String(a.service), DesiredCount: aws.Int32(0)})
	return err
}

func (a *activator) acquireLease(ctx context.Context, lease string) error {
	now := time.Now().Unix()
	_, err := a.dynamo.TransactWriteItems(ctx, &dynamodb.TransactWriteItemsInput{TransactItems: []dynamodbtypes.TransactWriteItem{
		{ConditionCheck: &dynamodbtypes.ConditionCheck{TableName: aws.String(a.table), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: scaleDownLock}}, ConditionExpression: aws.String("attribute_not_exists(id) OR expires < :now"), ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{":now": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(now)}}}},
		{Put: &dynamodbtypes.Put{TableName: aws.String(a.table), Item: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: lease}, "expires": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Add(a.startWait + time.Hour).Unix())}}, ConditionExpression: aws.String("attribute_not_exists(id)")}},
	}})
	return err
}

func (a *activator) acquireScaleDownLock(ctx context.Context, owner string) (bool, error) {
	_, err := a.dynamo.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(a.table), Item: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: scaleDownLock}, "owner": &dynamodbtypes.AttributeValueMemberS{Value: owner}, "expires": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Add(a.startWait + time.Hour).Unix())}}, ConditionExpression: aws.String("attribute_not_exists(id) OR expires < :now"), ExpressionAttributeValues: map[string]dynamodbtypes.AttributeValue{":now": &dynamodbtypes.AttributeValueMemberN{Value: fmt.Sprint(time.Now().Unix())}}})
	if err != nil {
		var conditional *dynamodbtypes.ConditionalCheckFailedException
		if errors.As(err, &conditional) {
			return false, nil
		}
		return false, err
	}
	return true, nil
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

func (a *activator) releaseLease(ctx context.Context, lease string) error {
	_, err := a.dynamo.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String(a.table), Key: map[string]dynamodbtypes.AttributeValue{"id": &dynamodbtypes.AttributeValueMemberS{Value: lease}}})
	return err
}

func (a *activator) fail(w http.ResponseWriter, err error) {
	a.logger.Error("websocket activation failed", "error", err)
	w.Header().Set("Retry-After", "1")
	http.Error(w, "websocket activation unavailable", http.StatusServiceUnavailable)
}

func proxyMessages(errorsCh chan<- error, from, to *websocket.Conn) {
	for {
		messageType, message, err := from.ReadMessage()
		if err != nil {
			errorsCh <- err
			return
		}
		if err := to.WriteMessage(messageType, message); err != nil {
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

func randomID(r *http.Request) string {
	hash := fnv.New64a()
	remoteAddress := ""
	if r != nil {
		remoteAddress = r.RemoteAddr
	}
	_, _ = hash.Write([]byte(remoteAddress + time.Now().UTC().String()))
	return fmt.Sprintf("%x", hash.Sum64())
}

func isNormalWebSocketClose(err error) bool {
	closeError, ok := err.(*websocket.CloseError)
	return ok && (closeError.Code == websocket.CloseNormalClosure || closeError.Code == websocket.CloseGoingAway)
}
