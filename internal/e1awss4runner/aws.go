package e1awss4runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2"
	elbtypes "github.com/aws/aws-sdk-go-v2/service/elasticloadbalancingv2/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type Storage interface {
	ReserveRun(ctx context.Context, bucket, key string) error
	DeleteDerivative(ctx context.Context, bucket, key string) error
	DerivativeExists(ctx context.Context, bucket, key string) (bool, error)
	ReadDerivative(ctx context.Context, bucket, key string) ([]byte, error)
	PutResult(ctx context.Context, bucket, key string, data []byte) error
}

type EnvironmentProbe interface {
	Snapshot(ctx context.Context, target ServiceTarget) (HealthSnapshot, error)
}

type Dependencies struct {
	Storage    Storage
	Probe      EnvironmentProbe
	HTTPClient HTTPDoer
}

type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type s3API interface {
	DeleteObject(ctx context.Context, input *s3.DeleteObjectInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	HeadObject(ctx context.Context, input *s3.HeadObjectInput, optFns ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(ctx context.Context, input *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, input *s3.PutObjectInput, optFns ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type ECSAPI interface {
	DescribeServices(ctx context.Context, input *ecs.DescribeServicesInput, optFns ...func(*ecs.Options)) (*ecs.DescribeServicesOutput, error)
	ListTasks(ctx context.Context, input *ecs.ListTasksInput, optFns ...func(*ecs.Options)) (*ecs.ListTasksOutput, error)
	DescribeTasks(ctx context.Context, input *ecs.DescribeTasksInput, optFns ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error)
}

type ELBAPI interface {
	DescribeTargetHealth(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetHealthInput, optFns ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetHealthOutput, error)
	DescribeTargetGroupAttributes(ctx context.Context, input *elasticloadbalancingv2.DescribeTargetGroupAttributesInput, optFns ...func(*elasticloadbalancingv2.Options)) (*elasticloadbalancingv2.DescribeTargetGroupAttributesOutput, error)
}

type AWSStorage struct {
	client s3API
}

type AWSProbe struct {
	ecs ECSAPI
	elb ELBAPI
}

func NewAWSDependencies(ctx context.Context, region string, httpClient HTTPDoer) (Dependencies, error) {
	configuration, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return Dependencies{}, fmt.Errorf("load AWS configuration: %w", err)
	}
	return Dependencies{
		Storage:    &AWSStorage{client: s3.NewFromConfig(configuration)},
		Probe:      &AWSProbe{ecs: ecs.NewFromConfig(configuration), elb: elasticloadbalancingv2.NewFromConfig(configuration)},
		HTTPClient: httpClient,
	}, nil
}

func (s *AWSStorage) DeleteDerivative(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("delete derivative object: %w", err)
	}
	return nil
}

func (s *AWSStorage) DerivativeExists(ctx context.Context, bucket, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err == nil {
		return true, nil
	}
	if isHTTPStatus(err, 404) {
		return false, nil
	}
	return false, fmt.Errorf("head derivative object: %w", err)
}

func (s *AWSStorage) ReadDerivative(ctx context.Context, bucket, key string) ([]byte, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, fmt.Errorf("get derivative object: %w", err)
	}
	if output == nil || output.Body == nil {
		return nil, errors.New("get derivative object returned an empty body")
	}
	defer output.Body.Close()
	data, err := io.ReadAll(io.LimitReader(output.Body, responseBodyLimit+1))
	if err != nil {
		return nil, fmt.Errorf("read derivative object: %w", err)
	}
	if len(data) > responseBodyLimit {
		return nil, fmt.Errorf("read derivative object: response exceeds %d bytes", responseBodyLimit)
	}
	return data, nil
}

func (s *AWSStorage) ReserveRun(ctx context.Context, bucket, key string) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader(`{"reserved":true}`), IfNoneMatch: aws.String("*"),
	})
	if err != nil {
		return errors.New("run reservation failed; existing or uncertain runs must not be retried")
	}
	return nil
}

func (s *AWSStorage) PutResult(ctx context.Context, bucket, key string, data []byte) error {
	contentType := "application/octet-stream"
	switch strings.ToLower(filepath.Ext(key)) {
	case ".json":
		contentType = "application/json"
	case ".csv":
		contentType = "text/csv"
	case ".svg":
		contentType = "image/svg+xml"
	}
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentType: aws.String(contentType),
	})
	if err != nil {
		return fmt.Errorf("put result object: %w", err)
	}
	return nil
}

func (p *AWSProbe) Snapshot(ctx context.Context, target ServiceTarget) (HealthSnapshot, error) {
	services, err := p.ecs.DescribeServices(ctx, &ecs.DescribeServicesInput{
		Cluster: aws.String(target.Cluster), Services: []string{target.Service},
	})
	if err != nil {
		return HealthSnapshot{}, fmt.Errorf("describe ECS service: %w", err)
	}
	if len(services.Services) != 1 || len(services.Failures) != 0 {
		return HealthSnapshot{}, errors.New("ECS service description is incomplete")
	}
	service := services.Services[0]
	snapshot := HealthSnapshot{
		DesiredTasks: int(service.DesiredCount),
		RunningTasks: int(service.RunningCount),
		PendingTasks: int(service.PendingCount),
	}
	snapshot.Stable = service.DesiredCount == int32(target.ExpectedTasks) && service.RunningCount == int32(target.ExpectedTasks) && service.PendingCount == 0 && len(service.Deployments) == 1 && service.Deployments[0].RolloutState == ecstypes.DeploymentRolloutStateCompleted

	taskARNs, err := p.listTaskARNs(ctx, target)
	if err != nil {
		return HealthSnapshot{}, err
	}
	if len(taskARNs) == 0 {
		return snapshot, nil
	}
	tasks, err := p.ecs.DescribeTasks(ctx, &ecs.DescribeTasksInput{Cluster: aws.String(target.Cluster), Tasks: taskARNs})
	if err != nil {
		return HealthSnapshot{}, fmt.Errorf("describe ECS tasks: %w", err)
	}
	if len(tasks.Failures) != 0 {
		return HealthSnapshot{}, errors.New("ECS task description is incomplete")
	}

	health, err := p.elb.DescribeTargetHealth(ctx, &elasticloadbalancingv2.DescribeTargetHealthInput{TargetGroupArn: aws.String(target.TargetGroupARN)})
	if err != nil {
		return HealthSnapshot{}, fmt.Errorf("describe target health: %w", err)
	}
	healthyIPs := make(map[string]bool)
	for _, description := range health.TargetHealthDescriptions {
		if description.Target != nil && description.Target.Id != nil && description.TargetHealth != nil && description.TargetHealth.State == elbtypes.TargetHealthStateEnumHealthy {
			healthyIPs[*description.Target.Id] = true
		}
	}
	attributes, err := p.elb.DescribeTargetGroupAttributes(ctx, &elasticloadbalancingv2.DescribeTargetGroupAttributesInput{TargetGroupArn: aws.String(target.TargetGroupARN)})
	if err != nil {
		return HealthSnapshot{}, fmt.Errorf("describe target group attributes: %w", err)
	}
	attributeValues := make(map[string]string, len(attributes.Attributes))
	for _, attribute := range attributes.Attributes {
		attributeValues[aws.ToString(attribute.Key)] = aws.ToString(attribute.Value)
	}
	snapshot.Stable = snapshot.Stable && attributeValues["load_balancing.algorithm.type"] == "round_robin" && attributeValues["stickiness.enabled"] == "false"
	for _, task := range tasks.Tasks {
		if aws.ToString(task.LastStatus) != "RUNNING" || aws.ToString(task.DesiredStatus) != "RUNNING" || task.Connectivity != ecstypes.ConnectivityConnected {
			continue
		}
		if healthyIPs[taskPrivateIPv4(task)] {
			if taskID := sanitizedTaskID(aws.ToString(task.TaskArn)); taskID != "" {
				snapshot.HealthyTaskIDs = append(snapshot.HealthyTaskIDs, taskID)
			}
		}
	}
	sort.Strings(snapshot.HealthyTaskIDs)
	snapshot.Stable = snapshot.Stable && len(taskARNs) == target.ExpectedTasks && len(tasks.Tasks) == target.ExpectedTasks && len(healthyIPs) == target.ExpectedTasks && len(snapshot.HealthyTaskIDs) == target.ExpectedTasks
	return snapshot, nil
}

func (p *AWSProbe) listTaskARNs(ctx context.Context, target ServiceTarget) ([]string, error) {
	var taskARNs []string
	var nextToken *string
	for {
		output, err := p.ecs.ListTasks(ctx, &ecs.ListTasksInput{
			Cluster: aws.String(target.Cluster), ServiceName: aws.String(target.Service), DesiredStatus: ecstypes.DesiredStatusRunning, NextToken: nextToken,
		})
		if err != nil {
			return nil, fmt.Errorf("list ECS tasks: %w", err)
		}
		taskARNs = append(taskARNs, output.TaskArns...)
		nextToken = output.NextToken
		if nextToken == nil || *nextToken == "" {
			return taskARNs, nil
		}
	}
}

func taskPrivateIPv4(task ecstypes.Task) string {
	for _, attachment := range task.Attachments {
		if aws.ToString(attachment.Type) != "ElasticNetworkInterface" {
			continue
		}
		for _, detail := range attachment.Details {
			if aws.ToString(detail.Name) == "privateIPv4Address" {
				return aws.ToString(detail.Value)
			}
		}
	}
	return ""
}

func sanitizedTaskID(taskARN string) string {
	separator := strings.LastIndexByte(taskARN, '/')
	if separator < 0 || separator == len(taskARN)-1 {
		return ""
	}
	return taskARN[separator+1:]
}

func isHTTPStatus(err error, status int) bool {
	var responseError interface{ HTTPStatusCode() int }
	return errors.As(err, &responseError) && responseError.HTTPStatusCode() == status
}
