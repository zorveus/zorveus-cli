package statestore

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/zorveus/zorveus-cli/internal/problem"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (transport transportFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestS3SignsConditionalRequestsAndRequiresObjectVersions(t *testing.T) {
	var saved []byte
	version := 0
	client := s3.NewFromConfig(aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider("test-access", "test-secret", ""), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		HTTPClient: &http.Client{Transport: transportFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.Host != "s3.example.test" || request.URL.Path != "/state-bucket/write-room/prod.json" || !strings.Contains(request.Header.Get("Authorization"), "AWS4-HMAC-SHA256") {
				t.Fatalf("unexpected or unsigned request: %s", request.URL)
			}
			header := http.Header{"Content-Type": []string{"application/xml"}}
			status, data := 200, saved
			if request.Method == http.MethodGet && version == 0 {
				status, data = 404, []byte(`<Error><Code>NoSuchKey</Code></Error>`)
			}
			if request.Method == http.MethodPut {
				if request.Header.Get("If-None-Match") == "*" {
					if !strings.Contains(request.Header.Get("Authorization"), "if-none-match") {
						t.Fatal("condition was not signed")
					}
					if version != 0 {
						status = 412
					}
				} else if match := request.Header.Get("If-Match"); match == "" || match != fmt.Sprintf(`"v%d"`, version) {
					status = 412
				}
				if status == 412 {
					data = []byte(`<Error><Code>PreconditionFailed</Code></Error>`)
				} else {
					var err error
					saved, err = io.ReadAll(request.Body)
					if err != nil {
						t.Fatal(err)
					}
					version++
					data = nil
				}
			}
			if status == 200 {
				header.Set("ETag", fmt.Sprintf(`"v%d"`, version))
			}
			return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(bytes.NewReader(data)), Request: request}, nil
		})}}, func(options *s3.Options) {
		options.BaseEndpoint = aws.String("https://s3.example.test")
		options.UsePathStyle = true
	})
	store := &S3{client: client, config: Config{Backend: "s3", Bucket: "state-bucket", Key: "write-room/prod.json"}}
	ctx := context.Background()
	if object, err := store.Read(ctx); err != nil || object.Exists {
		t.Fatalf("missing key was not distinguished: %v", err)
	}
	first, err := store.Write(ctx, []byte(`{"revision":1}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write(ctx, []byte(`{"revision":2}`), ""); problem.Safe(err).Code != "remote_state_conflict" {
		t.Fatalf("create-only write overwrote state: %v", err)
	}
	second, err := store.Write(ctx, []byte(`{"revision":2}`), first)
	if err != nil || second == first {
		t.Fatalf("conditional update failed: %v", err)
	}
	if _, err := store.Write(ctx, []byte(`{"revision":3}`), first); problem.Safe(err).Code != "remote_state_conflict" {
		t.Fatalf("stale write was accepted: %v", err)
	}
	object, err := store.Read(ctx)
	if err != nil || object.Version != second || !bytes.Equal(object.Data, []byte(`{"revision":2}`)) {
		t.Fatalf("state round trip failed: %v", err)
	}
}

func TestStateConfigurationRejectsCredentialsAndUnsafeEndpoints(t *testing.T) {
	for _, value := range []string{
		`{"backend":"s3","bucket":"bucket","key":"prod.json","accessKey":"private"}`,
		`{"backend":"s3","bucket":"bucket","key":"prod.json","endpoint":"http://storage.example.test"}`,
		`{"backend":"s3","bucket":"bucket","key":"prod.json","endpoint":"https://user:password@storage.example.test"}`,
		`{"backend":"unknown","bucket":"bucket","key":"prod.json"}`,
	} {
		if _, err := ParseConfig([]byte(value)); err == nil {
			t.Fatalf("invalid configuration accepted: %s", value)
		}
	}
	if _, err := ParseConfig([]byte(`{"backend":"s3","bucket":"bucket","key":"prod.json","endpoint":"http://localhost:9000","region":"us-east-1","usePathStyle":true}`)); err != nil {
		t.Fatal(err)
	}
}
