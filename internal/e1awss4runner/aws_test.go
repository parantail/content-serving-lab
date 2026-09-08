package e1awss4runner

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type reservationClient struct {
	s3API
	t   *testing.T
	err error
}

func (c reservationClient) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if aws.ToString(input.IfNoneMatch) != "*" || aws.ToString(input.Bucket) != "results" || aws.ToString(input.Key) != "run/_reservation.json" {
		c.t.Fatal("reservation must be conditional and exactly scoped")
	}
	return &s3.PutObjectOutput{}, c.err
}
func TestRunReservationIsConditional(t *testing.T) {
	s := &AWSStorage{client: reservationClient{t: t}}
	if err := s.ReserveRun(context.Background(), "results", "run/_reservation.json"); err != nil {
		t.Fatal(err)
	}
}

func TestAWSProbeMapsHealthyTargetIPsToSanitizedTaskIDs(t *testing.T) {
	ecsClient := &fakeECSClient{
		services: &ecs.DescribeServicesOutput{Services: []types.Service{{
			DesiredCount: 2, RunningCount: 2, Deployments: []types.Deployment{{RolloutState: types.DeploymentRolloutStateCompleted}},
		}}},
		listed: &ecs.ListTasksOutput{TaskArns: []string{
			"arn:aws:ecs:ap-northeast-2:123456789012:task/private/task-b",
			"arn:aws:ecs:ap-northeast-2:123456789012:task/private/task-a",
		}},
		tasks: &ecs.DescribeTasksOutput{Tasks: []types.Task{
			runningTask("arn:aws:ecs:ap-northeast-2:123456789012:task/private/task-b", "10.0.2.8"),
			runningTask("arn:aws:ecs:ap-northeast-2:123456789012:task/private/task-a", "10.0.1.7"),
		}},
	}
	elbClient := &fakeELBClient{
		output: &elasticloadbalancingv2.DescribeTargetHealthOutput{TargetHealthDescriptions: []elbtypes.TargetHealthDescription{
			healthyTarget("10.0.1.7"), healthyTarget("10.0.2.8"),
		}},
		attributes: &elasticloadbalancingv2.DescribeTargetGroupAttributesOutput{Attributes: []elbtypes.TargetGroupAttribute{
			{Key: aws.String("load_balancing.algorithm.type"), Value: aws.String("round_robin")},
			{Key: aws.String("stickiness.enabled"), Value: aws.String("false")},
		}},
	}
	probe := &AWSProbe{ecs: ecsClient, elb: elbClient}
	target := ServiceTarget{Cluster: "private", Service: "service-2", TargetGroupARN: "private-target", ExpectedTasks: 2}

	snapshot, err := probe.Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Stable || snapshot.DesiredTasks != 2 || snapshot.RunningTasks != 2 || snapshot.PendingTasks != 0 {
		t.Fatalf("unexpected health snapshot: %#v", snapshot)
	}
	if len(snapshot.HealthyTaskIDs) != 2 || snapshot.HealthyTaskIDs[0] != "task-a" || snapshot.HealthyTaskIDs[1] != "task-b" {
		t.Fatalf("healthy task IDs = %#v", snapshot.HealthyTaskIDs)
	}

	elbClient.output.TargetHealthDescriptions = append(elbClient.output.TargetHealthDescriptions, healthyTarget("10.0.9.9"))
	snapshot, err = probe.Snapshot(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Stable {
		t.Fatal("snapshot with an extra healthy target must not be stable")
	}
}

type fakeECSClient struct {
	services *ecs.DescribeServicesOutput
	listed   *ecs.ListTasksOutput
	tasks    *ecs.DescribeTasksOutput
}

func (f *fakeECSClient) DescribeServices(context.Context, *ecs.DescribeServicesInput, ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error) {
	return f.services, nil
}

func (f *fakeECSClient) ListTasks(context.Context, *ecs.ListTasksInput, ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	return f.listed, nil
}

func (f *fakeECSClient) DescribeTasks(context.Context, *ecs.DescribeTasksInput, ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	return f.tasks, nil
}

type fakeELBClient struct {
	output     *elasticloadbalancingv2.DescribeTargetHealthOutput
	attributes *elasticloadbalancingv2.DescribeTargetGroupAttributesOutput
}

func (f *fakeELBClient) DescribeTargetHealth(context.Context, *elasticloadbalancingv2.DescribeTargetHealthInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error) {
	return f.output, nil
}

func (f *fakeELBClient) DescribeTargetGroupAttributes(context.Context, *elasticloadbalancingv2.DescribeTargetGroupAttributesInput, ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupAttributesOutput, error) {
	return f.attributes, nil
}

func runningTask(arn, address string) types.Task {
	return types.Task{
		TaskArn: aws.String(arn), LastStatus: aws.String("RUNNING"), DesiredStatus: aws.String("RUNNING"), Connectivity: types.ConnectivityConnected,
		Attachments: []types.Attachment{{Type: aws.String("ElasticNetworkInterface"), Details: []types.KeyValuePair{{Name: aws.String("privateIPv4Address"), Value: aws.String(address)}}}},
	}
}

func healthyTarget(address string) elbtypes.TargetHealthDescription {
	return elbtypes.TargetHealthDescription{
		Target:       &elbtypes.TargetDescription{Id: aws.String(address)},
		TargetHealth: &elbtypes.TargetHealth{State: elbtypes.TargetHealthStateEnumHealthy},
	}
}
