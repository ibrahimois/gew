package forge

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testRequester(t *testing.T, server string) *HTTPRequester {
	t.Helper()
	requester, err := NewHTTPRequester(Config{Provider: ForgeGitea, URL: server, Token: "secret"}, server, nil, make(http.Header))
	if err != nil {
		t.Fatal(err)
	}
	requester.sleep = func(context.Context, time.Duration) error { return nil }
	requester.jitter = func(delay time.Duration) time.Duration { return delay }
	return requester
}

func TestHTTPRequesterRetriesOnlyIdempotentTransientResponses(t *testing.T) {
	var gets, posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			attempt := gets.Add(1)
			if attempt < 3 {
				http.Error(response, "temporary", http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]string{"status": "ok"})
		case http.MethodPost:
			posts.Add(1)
			http.Error(response, "temporary", http.StatusInternalServerError)
		}
	}))
	defer server.Close()

	requester := testRequester(t, server.URL)
	var result map[string]string
	if err := requester.DoJSON(context.Background(), http.MethodGet, "/read", nil, &result); err != nil {
		t.Fatal(err)
	}
	if gets.Load() != 3 || result["status"] != "ok" {
		t.Fatalf("GET attempts=%d result=%v", gets.Load(), result)
	}
	if err := requester.DoJSON(context.Background(), http.MethodPost, "/write", map[string]string{"x": "y"}, nil); err == nil {
		t.Fatal("POST 500 unexpectedly succeeded")
	}
	if posts.Load() != 1 {
		t.Fatalf("POST attempts=%d, want 1", posts.Load())
	}
}

func TestHTTPRequesterDoesNotRetryAuthenticationOrMalformedSuccess(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, body: `{"message":"no"}`},
		{name: "malformed success", status: http.StatusOK, body: `{`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				response.WriteHeader(test.status)
				_, _ = response.Write([]byte(test.body))
			}))
			defer server.Close()
			requester := testRequester(t, server.URL)
			var target map[string]any
			if err := requester.DoJSON(context.Background(), http.MethodGet, "/", nil, &target); err == nil {
				t.Fatal("request unexpectedly succeeded")
			}
			if calls.Load() != 1 {
				t.Fatalf("calls=%d, want 1", calls.Load())
			}
		})
	}
}

func TestHTTPRequesterCancellationInterruptsBackoff(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "temporary", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	requester := testRequester(t, server.URL)
	requester.sleep = func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := requester.DoJSON(ctx, http.MethodGet, "/", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v, want context.Canceled", err)
	}
}

func TestHTTPRequesterTimeoutValidationAndRedaction(t *testing.T) {
	for _, value := range []string{"500ms", "31m", "invalid"} {
		if _, err := ParseHTTPTimeout(value); err == nil {
			t.Fatalf("ParseHTTPTimeout(%q) succeeded", value)
		}
	}
	if got, err := ParseHTTPTimeout(""); err != nil || got != DefaultHTTPTimeout {
		t.Fatalf("default timeout=%s err=%v", got, err)
	}
	if got, err := ParseHTTPTimeout("5m"); err != nil || got != 5*time.Minute {
		t.Fatalf("configured timeout=%s err=%v", got, err)
	}

	requester, err := NewHTTPRequester(Config{Provider: ForgeGitea, URL: "https://example.test", Token: "top-secret", RequestTimeout: "5m"}, "https://example.test", nil, make(http.Header))
	if err != nil {
		t.Fatal(err)
	}
	sanitized := requester.SanitizeError(errors.New("failed with top-secret"))
	if strings.Contains(sanitized.Error(), "top-secret") || !strings.Contains(sanitized.Error(), "[REDACTED]") {
		t.Fatalf("error was not redacted: %v", sanitized)
	}
}

func TestHTTPRequesterHTTP1OnlyConstrainsTLSALPN(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.ProtoMajor != 1 {
			t.Fatalf("protocol = %s, want HTTP/1.1", request.Proto)
		}
		_ = json.NewEncoder(response).Encode(map[string]bool{"ok": true})
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	requester, err := NewHTTPRequester(Config{Provider: ForgeGitea, URL: server.URL, Insecure: true, HTTP1Only: true}, server.URL, nil, make(http.Header))
	if err != nil {
		t.Fatal(err)
	}
	var response map[string]bool
	if err := requester.DoJSON(context.Background(), http.MethodGet, "/", nil, &response); err != nil {
		t.Fatal(err)
	}
	if !response["ok"] {
		t.Fatal("unexpected response")
	}
}
