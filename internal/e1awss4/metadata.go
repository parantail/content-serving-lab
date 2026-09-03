package e1awss4

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const metadataResponseLimit = 2 << 20

type TaskIdentity struct {
	TaskID                 string  `json:"task_id"`
	TaskDefinitionFamily   string  `json:"task_definition_family"`
	TaskDefinitionRevision string  `json:"task_definition_revision"`
	AvailabilityZone       string  `json:"availability_zone"`
	LaunchType             string  `json:"launch_type"`
	CPUVCpu                float64 `json:"cpu_vcpu"`
	MemoryMiB              int64   `json:"memory_mib"`
	ImageDigest            string  `json:"image_digest"`
}

type ResourcePoint struct {
	CPUUsageNanos    uint64
	MemoryUsageBytes uint64
}

type MetadataClient struct {
	baseURL       string
	containerName string
	httpClient    *http.Client
}

func NewMetadataClient(baseURL, containerName string, httpClient *http.Client) (*MetadataClient, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("invalid ECS metadata URI %q", baseURL)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("invalid ECS metadata URI scheme %q", parsed.Scheme)
	}
	if containerName == "" {
		return nil, errors.New("ECS media container name is required")
	}
	if httpClient == nil {
		return nil, errors.New("ECS metadata HTTP client is required")
	}
	return &MetadataClient{baseURL: baseURL, containerName: containerName, httpClient: httpClient}, nil
}

func (c *MetadataClient) LoadTaskIdentity(ctx context.Context) (TaskIdentity, error) {
	var metadata taskMetadata
	if err := c.getJSON(ctx, "/task", &metadata); err != nil {
		return TaskIdentity{}, fmt.Errorf("load ECS task metadata: %w", err)
	}

	taskID, err := taskIDFromARN(metadata.TaskARN)
	if err != nil {
		return TaskIdentity{}, err
	}
	if metadata.Family == "" || metadata.Revision == "" || metadata.AvailabilityZone == "" || metadata.LaunchType == "" {
		return TaskIdentity{}, errors.New("ECS task metadata is missing identity fields")
	}
	if metadata.Limits.CPU <= 0 || metadata.Limits.Memory <= 0 {
		return TaskIdentity{}, errors.New("ECS task metadata is missing resource limits")
	}

	var imageDigest string
	for _, container := range metadata.Containers {
		if container.Name == c.containerName {
			imageDigest = container.ImageID
			break
		}
	}
	if err := validateImageDigest(imageDigest); err != nil {
		return TaskIdentity{}, fmt.Errorf("media container %q: %w", c.containerName, err)
	}

	return TaskIdentity{
		TaskID:                 taskID,
		TaskDefinitionFamily:   metadata.Family,
		TaskDefinitionRevision: string(metadata.Revision),
		AvailabilityZone:       metadata.AvailabilityZone,
		LaunchType:             metadata.LaunchType,
		CPUVCpu:                metadata.Limits.CPU,
		MemoryMiB:              int64(metadata.Limits.Memory),
		ImageDigest:            imageDigest,
	}, nil
}

func (c *MetadataClient) Sample(ctx context.Context) (ResourcePoint, error) {
	stats := make(map[string]containerStats)
	if err := c.getJSON(ctx, "/task/stats", &stats); err != nil {
		return ResourcePoint{}, fmt.Errorf("load ECS task stats: %w", err)
	}
	if len(stats) == 0 {
		return ResourcePoint{}, errors.New("ECS task stats contains no containers")
	}

	var point ResourcePoint
	for _, container := range stats {
		point.CPUUsageNanos += container.CPUStats.CPUUsage.TotalUsage
		point.MemoryUsageBytes += container.MemoryStats.Usage
	}
	return point, nil
}

func (c *MetadataClient) getJSON(ctx context.Context, path string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return err
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, metadataResponseLimit))
		return fmt.Errorf("metadata endpoint returned HTTP %d", response.StatusCode)
	}

	decoder := json.NewDecoder(io.LimitReader(response.Body, metadataResponseLimit))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("metadata endpoint returned multiple JSON values")
		}
		return err
	}
	return nil
}

type taskMetadata struct {
	TaskARN          string          `json:"TaskARN"`
	Family           string          `json:"Family"`
	Revision         flexibleString  `json:"Revision"`
	AvailabilityZone string          `json:"AvailabilityZone"`
	LaunchType       string          `json:"LaunchType"`
	Limits           taskLimits      `json:"Limits"`
	Containers       []taskContainer `json:"Containers"`
}

type taskLimits struct {
	CPU    float64 `json:"CPU"`
	Memory float64 `json:"Memory"`
}

type taskContainer struct {
	Name    string `json:"Name"`
	ImageID string `json:"ImageID"`
}

type containerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
	} `json:"memory_stats"`
}

type flexibleString string

func (s *flexibleString) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err == nil {
		*s = flexibleString(value)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(data, &number); err != nil {
		return errors.New("value must be a string or number")
	}
	*s = flexibleString(number.String())
	return nil
}

func taskIDFromARN(taskARN string) (string, error) {
	separator := strings.LastIndexByte(taskARN, '/')
	if separator < 0 || separator == len(taskARN)-1 {
		return "", errors.New("ECS task metadata contains an invalid TaskARN")
	}
	taskID := taskARN[separator+1:]
	if len(taskID) > 128 {
		return "", errors.New("ECS task ID is too long")
	}
	for _, character := range taskID {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '-' && character != '_' {
			return "", errors.New("ECS task ID contains invalid characters")
		}
	}
	return taskID, nil
}

func validateImageDigest(digest string) error {
	const prefix = "sha256:"
	if !strings.HasPrefix(digest, prefix) || len(digest) != len(prefix)+64 {
		return errors.New("ECS task metadata contains an invalid image digest")
	}
	for _, character := range digest[len(prefix):] {
		if _, err := strconv.ParseUint(string(character), 16, 4); err != nil {
			return errors.New("ECS task metadata contains an invalid image digest")
		}
	}
	return nil
}
