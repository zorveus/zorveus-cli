package statestore

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

const MaxSize = 32 << 20

type s3API interface {
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

type S3 struct {
	client s3API
	config Config
}

func NewS3(ctx context.Context, config Config) (*S3, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	options := []func(*awsconfig.LoadOptions) error{}
	if config.Region != "" {
		options = append(options, awsconfig.WithRegion(config.Region))
	}
	settings, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil || settings.Region == "" {
		return nil, invalidConfig()
	}
	// Optional AWS checksum negotiation is not supported by every S3-compatible service.
	settings.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
	client := s3.NewFromConfig(settings, func(options *s3.Options) {
		options.UsePathStyle = config.UsePathStyle
		if config.Endpoint != "" {
			options.BaseEndpoint = aws.String(config.Endpoint)
		}
	})
	return &S3{client: client, config: config}, nil
}

func (store *S3) Read(ctx context.Context) (Object, error) {
	response, err := store.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(store.config.Bucket), Key: aws.String(store.config.Key)})
	if err != nil {
		var failure smithy.APIError
		if errors.As(err, &failure) && failure.ErrorCode() == "NoSuchKey" {
			return Object{}, nil
		}
		return Object{}, Unavailable()
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxSize+1))
	if err != nil || len(data) > MaxSize || aws.ToString(response.ETag) == "" {
		return Object{}, Unavailable()
	}
	return Object{Data: data, Version: aws.ToString(response.ETag), Exists: true}, nil
}

func (store *S3) Write(ctx context.Context, data []byte, version string) (string, error) {
	if len(data) > MaxSize {
		return "", Unavailable()
	}
	request := &s3.PutObjectInput{Bucket: aws.String(store.config.Bucket), Key: aws.String(store.config.Key), Body: bytes.NewReader(data), ContentType: aws.String("application/json")}
	if version == "" {
		request.IfNoneMatch = aws.String("*")
	} else {
		request.IfMatch = aws.String(version)
	}
	response, err := store.client.PutObject(ctx, request)
	if err != nil {
		var failure smithy.APIError
		if errors.As(err, &failure) {
			switch failure.ErrorCode() {
			case "PreconditionFailed", "ConditionalRequestConflict":
				return "", Conflict()
			}
		}
		return "", Unavailable()
	}
	if aws.ToString(response.ETag) == "" {
		return "", Unavailable()
	}
	return aws.ToString(response.ETag), nil
}
