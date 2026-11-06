package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

type trackedResponseBody struct {
	*strings.Reader
	closed bool
}

func (body *trackedResponseBody) Close() error { body.closed = true; return nil }

type responseTransport struct{ body *trackedResponseBody }

func (transport responseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: transport.body}, nil
}

func TestWriteCompletesIgnoredResponseBeforeReturning(t *testing.T) {
	body := &trackedResponseBody{Reader: strings.NewReader(`{"confirmed":true}`)}
	client := &Client{HTTP: &http.Client{Transport: responseTransport{body: body}}}
	if err := client.WriteJSON(context.Background(), "https://example.test", http.MethodPatch, "/resource", "token", map[string]string{"name": "Updated"}, nil); err != nil {
		t.Fatal(err)
	}
	if body.Len() != 0 || !body.closed {
		t.Fatal("the write returned before completing and closing its response")
	}
}
