package main

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
)

func TestRandomIDAcceptsBackgroundOwnerWithoutRequest(t *testing.T) {
	if randomID(nil) == "" {
		t.Fatal("randomID returned an empty owner ID")
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
	a := &activator{startWait: time.Hour}
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

func makeTaskPage(prefix string, start, count int) []string {
	page := make([]string, 0, count)
	for i := start; i < start+count; i++ {
		page = append(page, prefix+itoa(i))
	}
	return page
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

func (*fakeECSClient) UpdateService(context.Context, *ecs.UpdateServiceInput, ...func(*ecs.Options)) (*ecs.UpdateServiceOutput, error) {
	return &ecs.UpdateServiceOutput{}, nil
}

func ecsTask(arn, address string) ecstypes.Task {
	return ecstypes.Task{TaskArn: aws.String(arn), LastStatus: aws.String("RUNNING"), Attachments: []ecstypes.Attachment{{Type: aws.String("ElasticNetworkInterface"), Details: []ecstypes.KeyValuePair{{Name: aws.String("privateIPv4Address"), Value: aws.String(address)}}}}}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
