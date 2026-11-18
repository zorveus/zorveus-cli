package statestore

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/url"
	"strings"

	"github.com/zorveus/zorveus-cli/internal/problem"
)

// Backends must compare versions atomically. An empty version means create only.
type Backend interface {
	Read(context.Context) (Object, error)
	Write(context.Context, []byte, string) (string, error)
}

type Object struct {
	Data    []byte
	Version string
	Exists  bool
}

type Config struct {
	Backend      string `json:"backend"`
	Bucket       string `json:"bucket"`
	Key          string `json:"key"`
	Region       string `json:"region,omitempty"`
	Endpoint     string `json:"endpoint,omitempty"`
	UsePathStyle bool   `json:"usePathStyle,omitempty"`
}

type Factory func(context.Context, Config) (Backend, error)

func (config Config) SameLocation(other Config) bool {
	return config.Backend == other.Backend && config.Bucket == other.Bucket && config.Key == other.Key && strings.TrimRight(config.Endpoint, "/") == strings.TrimRight(other.Endpoint, "/")
}

func ParseConfig(data []byte) (Config, error) {
	var config Config
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&config) != nil {
		return Config{}, invalidConfig()
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return Config{}, invalidConfig()
	}
	return config, config.Validate()
}

func (config Config) Validate() error {
	if config.Backend != "s3" || strings.TrimSpace(config.Bucket) == "" || config.Key == "" || len(config.Key) > 1024 {
		return invalidConfig()
	}
	if config.Endpoint == "" {
		return nil
	}
	endpoint, err := url.Parse(config.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != "" && endpoint.Path != "/" {
		return invalidConfig()
	}
	if endpoint.Scheme == "https" {
		return nil
	}
	ip := net.ParseIP(endpoint.Hostname())
	if endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return invalidConfig()
}

func NewBackend(ctx context.Context, config Config) (Backend, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	switch config.Backend {
	case "s3":
		return NewS3(ctx, config)
	default:
		return nil, invalidConfig()
	}
}

func invalidConfig() error {
	return problem.New("invalid_state_backend", "Configure state with backend=s3, bucket, key, and a region or AWS_REGION. Custom endpoints must use HTTPS, or HTTP on loopback for local testing. Keep credentials in the AWS credential chain, not in the manifest.", 2)
}

func Unavailable() error {
	return problem.New("remote_state_unavailable", "Could not read or safely write durable state. Check the S3 endpoint, bucket, key, region, credentials, permissions, and conditional-write support. No pending API mutation was sent after this storage error.", 5)
}

func Conflict() error {
	return problem.New("remote_state_conflict", "Durable state changed or a conditional write could not be confirmed. Stop this deployment and inspect the saved state and lock before retrying.", 5)
}
