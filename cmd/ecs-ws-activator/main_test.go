package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	dynamodbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/gorilla/websocket"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// randomID must be unpredictable: the previous FNV-1a hash of the remote address
// and the clock collided for two connections from one address in the same
// nanosecond, which refused the second client and let two scale-down owners
// delete each other's lock.
func TestRandomIDIsUnpredictableAndUnique(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for range 128 {
		value, err := randomID()
		if err != nil {
			t.Fatal(err)
		}
		if len(value) != 32 {
			t.Fatalf("id=%q, want 128 bits of hex", value)
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("randomID repeated %q", value)
		}
		seen[value] = struct{}{}
	}
}

func TestEndpointURLsUseConfiguredPortAndIPv6Syntax(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{name: "readiness IPv4", got: readinessURL(endpoint{address: "10.0.0.4"}, 8080), want: "http://10.0.0.4:8080/readyz"},
		{name: "websocket IPv4", got: websocketEndpointURL(endpoint{address: "10.0.0.4"}, 8080, "/socket"), want: "ws://10.0.0.4:8080/socket"},
		{name: "readiness IPv6", got: readinessURL(endpoint{address: "2001:db8::4"}, 8080), want: "http://[2001:db8::4]:8080/readyz"},
		{name: "websocket IPv6", got: websocketEndpointURL(endpoint{address: "2001:db8::4"}, 8080, "/socket"), want: "ws://[2001:db8::4]:8080/socket"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.got != testCase.want {
				t.Fatalf("URL=%q, want %q", testCase.got, testCase.want)
			}
		})
	}
}

func TestReadyEndpointStopsWaitingWhenContextIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &activator{startWait: time.Hour, dynamo: newFakeDynamo(), logger: discardLogger()}
	_, err := a.readyEndpoint(ctx)
	if err != context.Canceled {
		t.Fatalf("error=%v, want context cancellation", err)
	}
}

func TestRunningEndpointsPaginatesTasksAndDescribeBatches(t *testing.T) {
	client := &fakeECSClient{
		listPages: [][]string{makeTaskPage("task-", 0, 100), {"task-100"}},
	}
	for i := 0; i <= 100; i++ {
		client.tasks = append(client.tasks, ecsTask("task-"+itoa(i), "10.0.0."+itoa(i)))
	}
	a := &activator{ecs: client, cluster: "cluster", service: "service", family: "family"}
	endpoints, err := a.runningEndpoints(context.Background())
	if err != nil || len(endpoints) != 101 {
		t.Fatalf("endpoints=%+v err=%v", endpoints, err)
	}
	if len(client.listInputs) != 2 || len(client.describeInputs) != 2 || len(client.describeInputs[0].Tasks) != 100 || len(client.describeInputs[1].Tasks) != 1 {
		t.Fatalf("list=%d describe=%+v", len(client.listInputs), client.describeInputs)
	}
}

// The last disconnect is not by itself a reason to stop the stack. Scaling to
// zero on it meant no idle interval, no drain, and no verified snapshot.
func TestLastDisconnectDoesNotHibernateBeforeTheIdleIntervalElapses(t *testing.T) {
	dynamo := newFakeDynamo()
	control := &fakeControlPlane{}
	a := &activator{dynamo: dynamo, control: control, table: "leases", startWait: time.Minute, leaseTTL: time.Minute, idleTimeout: time.Hour, logger: discardLogger()}
	if err := a.hibernateIfIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if control.hibernates != 0 {
		t.Fatalf("hibernate requests=%d, want none before the idle interval elapses", control.hibernates)
	}
	// The idle instant is recorded so the interval can be measured at all.
	since, err := a.idleSince(context.Background())
	if err != nil || since.IsZero() {
		t.Fatalf("idle since=%v err=%v, want the idle instant recorded", since, err)
	}
	if err := a.hibernateIfIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if control.hibernates != 0 {
		t.Fatalf("hibernate requests=%d, want none while still inside the idle interval", control.hibernates)
	}
}

// Once the interval has elapsed, hibernation is delegated to the lifecycle
// activator, which owns the drain, snapshot, verification, and stop sequence.
func TestElapsedIdleIntervalDelegatesHibernationToTheLifecycleActivator(t *testing.T) {
	dynamo := newFakeDynamo()
	control := &fakeControlPlane{}
	a := &activator{dynamo: dynamo, control: control, table: "leases", startWait: time.Minute, leaseTTL: time.Minute, idleTimeout: time.Minute, logger: discardLogger()}
	dynamo.put(idleMarker, map[string]dynamodbtypes.AttributeValue{
		"id":    &dynamodbtypes.AttributeValueMemberS{Value: idleMarker},
		"since": &dynamodbtypes.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Add(-2*time.Minute).Unix(), 10)},
	})
	if err := a.hibernateIfIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if control.hibernates != 1 {
		t.Fatalf("hibernate requests=%d, want exactly one delegated hibernation", control.hibernates)
	}
}

// A live stream is a documented ineligibility condition, and it must also reset
// the idle measurement.
func TestActiveStreamLeaseBlocksHibernationAndClearsTheIdleInstant(t *testing.T) {
	dynamo := newFakeDynamo()
	control := &fakeControlPlane{}
	a := &activator{dynamo: dynamo, control: control, table: "leases", startWait: time.Minute, leaseTTL: time.Minute, idleTimeout: time.Minute, logger: discardLogger()}
	dynamo.put(idleMarker, map[string]dynamodbtypes.AttributeValue{
		"id":    &dynamodbtypes.AttributeValueMemberS{Value: idleMarker},
		"since": &dynamodbtypes.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Add(-2*time.Minute).Unix(), 10)},
	})
	dynamo.put("ws:live", map[string]dynamodbtypes.AttributeValue{
		"id":      &dynamodbtypes.AttributeValueMemberS{Value: "ws:live"},
		"expires": &dynamodbtypes.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10)},
	})
	if err := a.hibernateIfIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if control.hibernates != 0 {
		t.Fatalf("hibernate requests=%d, want none while a stream is live", control.hibernates)
	}
	since, err := a.idleSince(context.Background())
	if err != nil || !since.IsZero() {
		t.Fatalf("idle since=%v err=%v, want the stale idle instant cleared", since, err)
	}
}

// A client that gives up during a cold start must not look like an idle stack, or
// the cleanup stops the tasks that were seconds from ready and the next client
// repeats it forever.
func TestColdStartInProgressBlocksHibernation(t *testing.T) {
	dynamo := newFakeDynamo()
	control := &fakeControlPlane{}
	a := &activator{dynamo: dynamo, control: control, table: "leases", startWait: time.Minute, leaseTTL: time.Minute, idleTimeout: time.Minute, logger: discardLogger()}
	dynamo.put(idleMarker, map[string]dynamodbtypes.AttributeValue{
		"id":    &dynamodbtypes.AttributeValueMemberS{Value: idleMarker},
		"since": &dynamodbtypes.AttributeValueMemberN{Value: strconv.FormatInt(time.Now().Add(-2*time.Minute).Unix(), 10)},
	})
	if err := a.markStarting(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.hibernateIfIdle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if control.hibernates != 0 {
		t.Fatalf("hibernate requests=%d, want none while a cold start is in progress", control.hibernates)
	}
}

// The lock must expire in seconds. It was written with an expiry over an hour
// away, and acquiring a connection lease checks the same item, so one dropped
// conditional delete made the service unreachable for more than an hour.
func TestScaleDownLockExpiresInSeconds(t *testing.T) {
	dynamo := newFakeDynamo()
	a := &activator{dynamo: dynamo, table: "leases", startWait: time.Hour, leaseTTL: time.Minute, idleTimeout: time.Minute, logger: discardLogger()}
	acquired, err := a.acquireScaleDownLock(context.Background(), "owner")
	if err != nil || !acquired {
		t.Fatalf("acquired=%t err=%v", acquired, err)
	}
	expires, ok := numberAttribute(dynamo.get(scaleDownLock), "expires")
	if !ok {
		t.Fatal("scale-down lock has no expiry")
	}
	if remaining := time.Until(time.Unix(expires, 0)); remaining > time.Minute {
		t.Fatalf("scale-down lock expires in %s, want a short-lived lock", remaining)
	}
}

// The release is retried: a dropped delete blocks every new connection until the
// lock lapses.
func TestScaleDownLockReleaseIsRetried(t *testing.T) {
	dynamo := newFakeDynamo()
	dynamo.deleteFailures = 2
	a := &activator{dynamo: dynamo, table: "leases", startWait: time.Hour, leaseTTL: time.Minute, idleTimeout: time.Minute, logger: discardLogger()}
	if _, err := a.acquireScaleDownLock(context.Background(), "owner"); err != nil {
		t.Fatal(err)
	}
	a.releaseScaleDownLock(context.Background(), "owner")
	if dynamo.get(scaleDownLock) != nil {
		t.Fatal("scale-down lock survived a retried release")
	}
}

// Lease renewal must prove ownership, not merely that the item exists.
func TestRenewLeaseRequiresOwnership(t *testing.T) {
	dynamo := newFakeDynamo()
	a := &activator{dynamo: dynamo, table: "leases", startWait: time.Minute, leaseTTL: time.Minute, idleTimeout: time.Minute, logger: discardLogger()}
	if err := a.acquireLease(context.Background(), "ws:one", "owner-a"); err != nil {
		t.Fatal(err)
	}
	if err := a.renewLease(context.Background(), "ws:one", "owner-a"); err != nil {
		t.Fatalf("owner renewal err=%v", err)
	}
	if err := a.renewLease(context.Background(), "ws:one", "owner-b"); err == nil {
		t.Fatal("a foreign process renewed the lease")
	}
}

// The connection lease must be measured in seconds so a crashed activator cannot
// hold the application service awake — and billed — for over an hour.
func TestConnectionLeaseExpiryTracksTheConfiguredTTL(t *testing.T) {
	a := &activator{leaseTTL: 90 * time.Second}
	if remaining := time.Until(time.Unix(a.leaseExpiry(), 0)); remaining > 2*time.Minute {
		t.Fatalf("lease expires in %s, want the configured short TTL", remaining)
	}
}

// gorilla's dialer writes the handshake headers itself and rejects a request that
// carries any of them, so forwarding the client's Upgrade header made every
// WebSocket activation fail with 503.
func TestBackendDialSucceedsWithClientHandshakeHeadersPresent(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	// The handler runs on the server's goroutine while the assertions run on the
	// test's, so the negotiated subprotocol has to cross that boundary under a
	// lock rather than through a bare captured variable.
	var mu sync.Mutex
	negotiated := ""
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		negotiated = conn.Subprotocol()
		mu.Unlock()
		_ = conn.Close()
	}))
	defer backend.Close()

	client := httptest.NewRequest(http.MethodGet, "/socket", nil)
	client.Header.Set("Upgrade", "websocket")
	client.Header.Set("Connection", "Upgrade")
	client.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	client.Header.Set("Sec-WebSocket-Version", "13")
	client.Header.Set("Sec-WebSocket-Protocol", "chat")
	client.Header.Set("Cookie", "session=abc")

	headers, subprotocols := backendHandshake(client)
	upgrader.Subprotocols = subprotocols
	dialer := &websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: subprotocols}
	conn, response, err := dialer.DialContext(context.Background(), "ws"+strings.TrimPrefix(backend.URL, "http")+"/socket", headers)
	if err != nil {
		t.Fatalf("dial=%v, want the client handshake headers filtered out", err)
	}
	defer conn.Close()
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status=%d", response.StatusCode)
	}
	mu.Lock()
	observed := negotiated
	mu.Unlock()
	if observed != "chat" {
		t.Fatalf("subprotocol=%q, want the client's requested subprotocol forwarded through the dialer", observed)
	}
	// The application still needs the request's own headers.
	if headers.Get("Cookie") != "session=abc" {
		t.Fatal("application headers were dropped")
	}
}

// A wrapped close error is an ordinary disconnect, not a proxy failure.
func TestIsNormalWebSocketCloseUnwraps(t *testing.T) {
	wrapped := errors.Join(errors.New("read message"), &websocket.CloseError{Code: websocket.CloseGoingAway})
	if !isNormalWebSocketClose(wrapped) {
		t.Fatal("a wrapped normal close was reported as a proxy failure")
	}
	if isNormalWebSocketClose(errors.New("connection reset")) {
		t.Fatal("an unrelated error was treated as a normal close")
	}
}

func makeTaskPage(prefix string, start, count int) []string {
	page := make([]string, 0, count)
	for i := start; i < start+count; i++ {
		page = append(page, prefix+itoa(i))
	}
	return page
}

type fakeControlPlane struct {
	activates  int
	hibernates int
}

func (c *fakeControlPlane) Activate(context.Context) error  { c.activates++; return nil }
func (c *fakeControlPlane) Hibernate(context.Context) error { c.hibernates++; return nil }

type fakeDynamo struct {
	mu             sync.Mutex
	items          map[string]map[string]dynamodbtypes.AttributeValue
	deleteFailures int
}

func newFakeDynamo() *fakeDynamo {
	return &fakeDynamo{items: make(map[string]map[string]dynamodbtypes.AttributeValue)}
}

func (d *fakeDynamo) put(id string, item map[string]dynamodbtypes.AttributeValue) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.items[id] = item
}

func (d *fakeDynamo) get(id string) map[string]dynamodbtypes.AttributeValue {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.items[id]
}

func itemKey(key map[string]dynamodbtypes.AttributeValue) string {
	if value, ok := key["id"].(*dynamodbtypes.AttributeValueMemberS); ok {
		return value.Value
	}
	return ""
}

func (d *fakeDynamo) PutItem(_ context.Context, input *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := itemKey(input.Item)
	existing, exists := d.items[id]
	if input.ConditionExpression != nil && exists {
		expression := aws.ToString(input.ConditionExpression)
		if strings.Contains(expression, "attribute_not_exists(id) OR expires < :now") {
			expires, _ := numberAttribute(existing, "expires")
			if expires >= time.Now().Unix() {
				return nil, &dynamodbtypes.ConditionalCheckFailedException{}
			}
		} else if strings.Contains(expression, "attribute_not_exists(id)") {
			return nil, &dynamodbtypes.ConditionalCheckFailedException{}
		}
	}
	d.items[id] = input.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (d *fakeDynamo) DeleteItem(_ context.Context, input *dynamodb.DeleteItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.DeleteItemOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deleteFailures > 0 {
		d.deleteFailures--
		return nil, errors.New("provisioned throughput exceeded")
	}
	id := itemKey(input.Key)
	if input.ConditionExpression != nil {
		item, exists := d.items[id]
		if !exists {
			return nil, &dynamodbtypes.ConditionalCheckFailedException{}
		}
		wanted, _ := input.ExpressionAttributeValues[":owner"].(*dynamodbtypes.AttributeValueMemberS)
		owner, _ := item["owner"].(*dynamodbtypes.AttributeValueMemberS)
		if wanted == nil || owner == nil || wanted.Value != owner.Value {
			return nil, &dynamodbtypes.ConditionalCheckFailedException{}
		}
	}
	delete(d.items, id)
	return &dynamodb.DeleteItemOutput{}, nil
}

func (d *fakeDynamo) UpdateItem(_ context.Context, input *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	id := itemKey(input.Key)
	item, exists := d.items[id]
	if !exists {
		return nil, &dynamodbtypes.ConditionalCheckFailedException{}
	}
	if strings.Contains(aws.ToString(input.ConditionExpression), "owner = :owner") {
		wanted, _ := input.ExpressionAttributeValues[":owner"].(*dynamodbtypes.AttributeValueMemberS)
		owner, _ := item["owner"].(*dynamodbtypes.AttributeValueMemberS)
		if wanted == nil || owner == nil || wanted.Value != owner.Value {
			return nil, &dynamodbtypes.ConditionalCheckFailedException{}
		}
	}
	item["expires"] = input.ExpressionAttributeValues[":expires"]
	return &dynamodb.UpdateItemOutput{}, nil
}

func (d *fakeDynamo) GetItem(_ context.Context, input *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return &dynamodb.GetItemOutput{Item: d.items[itemKey(input.Key)]}, nil
}

func (d *fakeDynamo) Scan(_ context.Context, _ *dynamodb.ScanInput, _ ...func(*dynamodb.Options)) (*dynamodb.ScanOutput, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	items := make([]map[string]dynamodbtypes.AttributeValue, 0)
	for id, item := range d.items {
		if !strings.HasPrefix(id, "ws:") {
			continue
		}
		if expires, ok := numberAttribute(item, "expires"); ok && expires > time.Now().Unix() {
			items = append(items, item)
		}
	}
	return &dynamodb.ScanOutput{Items: items}, nil
}

func (d *fakeDynamo) TransactWriteItems(ctx context.Context, input *dynamodb.TransactWriteItemsInput, _ ...func(*dynamodb.Options)) (*dynamodb.TransactWriteItemsOutput, error) {
	for _, item := range input.TransactItems {
		if item.ConditionCheck != nil {
			d.mu.Lock()
			existing, exists := d.items[itemKey(item.ConditionCheck.Key)]
			d.mu.Unlock()
			if exists {
				expires, _ := numberAttribute(existing, "expires")
				if expires >= time.Now().Unix() {
					return nil, &dynamodbtypes.ConditionalCheckFailedException{}
				}
			}
		}
	}
	for _, item := range input.TransactItems {
		if item.Put == nil {
			continue
		}
		if _, err := d.PutItem(ctx, &dynamodb.PutItemInput{TableName: item.Put.TableName, Item: item.Put.Item, ConditionExpression: item.Put.ConditionExpression}); err != nil {
			return nil, err
		}
	}
	return &dynamodb.TransactWriteItemsOutput{}, nil
}

type fakeECSClient struct {
	listPages      [][]string
	listInputs     []*ecs.ListTasksInput
	describeInputs []*ecs.DescribeTasksInput
	tasks          []ecstypes.Task
}

func (f *fakeECSClient) ListTasks(_ context.Context, input *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	f.listInputs = append(f.listInputs, input)
	page := len(f.listInputs) - 1
	output := &ecs.ListTasksOutput{TaskArns: f.listPages[page]}
	if page+1 < len(f.listPages) {
		output.NextToken = aws.String("next")
	}
	return output, nil
}

func (f *fakeECSClient) DescribeTasks(_ context.Context, input *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	f.describeInputs = append(f.describeInputs, input)
	tasks := make([]ecstypes.Task, 0, len(input.Tasks))
	for _, requested := range input.Tasks {
		for _, task := range f.tasks {
			if aws.ToString(task.TaskArn) == requested {
				tasks = append(tasks, task)
				break
			}
		}
	}
	return &ecs.DescribeTasksOutput{Tasks: tasks}, nil
}

func ecsTask(arn, address string) ecstypes.Task {
	return ecstypes.Task{TaskArn: aws.String(arn), LastStatus: aws.String("RUNNING"), Attachments: []ecstypes.Attachment{{Type: aws.String("ElasticNetworkInterface"), Details: []ecstypes.KeyValuePair{{Name: aws.String("privateIPv4Address"), Value: aws.String(address)}}}}}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
