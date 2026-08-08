package forge

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gew/internal/version"
)

const (
	MaxRemoteJSON      = int64(16 << 20)
	MaxRemoteError     = int64(1 << 20)
	MaxRemoteSnapshot  = int64(1 << 30)
	DefaultHTTPTimeout = 90 * time.Second
	MinHTTPTimeout     = time.Second
	MaxHTTPTimeout     = 30 * time.Minute
	maxReadAttempts    = 4
)

type AuthPolicy func(*http.Request)

type sanitizedRequestError struct {
	message string
	cause   error
}

func (e *sanitizedRequestError) Error() string { return e.message }
func (e *sanitizedRequestError) Unwrap() error { return e.cause }

type HTTPRequester struct {
	kind     ForgeKind
	baseURL  string
	client   *http.Client
	auth     AuthPolicy
	secrets  []string
	headers  http.Header
	maxJSON  int64
	maxBytes int64
	sleep    func(context.Context, time.Duration) error
	jitter   func(time.Duration) time.Duration
}

func ParseHTTPTimeout(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return DefaultHTTPTimeout, nil
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid request timeout %q: %w", value, err)
	}
	if duration < MinHTTPTimeout || duration > MaxHTTPTimeout {
		return 0, fmt.Errorf("request timeout must be between %s and %s", MinHTTPTimeout, MaxHTTPTimeout)
	}
	return duration, nil
}

func NewHTTPRequester(p Config, baseURL string, auth AuthPolicy, headers http.Header) (*HTTPRequester, error) {
	timeout, err := ParseHTTPTimeout(p.RequestTimeout)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	tlsConfig := transport.TLSClientConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	if p.HTTP1Only {
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
		tlsConfig.NextProtos = []string{"http/1.1"}
	}
	if p.Insecure {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec // explicitly requested by user profile
	}
	transport.TLSClientConfig = tlsConfig
	secrets := []string{p.Token}
	client := &http.Client{Transport: transport, Timeout: timeout}
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if len(via) > 0 && !SameOrigin(via[0].URL, request.URL) {
			request.Header.Del("Authorization")
			request.Header.Del("Private-Token")
		}
		return nil
	}
	return &HTTPRequester{
		kind: p.Provider, baseURL: strings.TrimRight(baseURL, "/"), client: client,
		auth: auth, secrets: secrets, headers: headers.Clone(), maxJSON: MaxRemoteJSON,
		maxBytes: MaxRemoteSnapshot,
		sleep: func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		jitter: func(delay time.Duration) time.Duration {
			if delay <= 0 {
				return 0
			}
			return delay + time.Duration(rand.Int63n(int64(delay/2)+1))
		},
	}, nil
}

func SameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func (r *HTTPRequester) NewRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
	requestURL := endpoint
	if parsed, err := url.Parse(endpoint); err != nil || !parsed.IsAbs() {
		requestURL = r.baseURL + endpoint
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, body)
	if err != nil {
		return nil, err
	}
	for key, values := range r.headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("User-Agent", "gew/"+version.Current)
	if r.auth != nil {
		r.auth(request)
	}
	return request, nil
}

func (r *HTTPRequester) DoJSON(ctx context.Context, method, endpoint string, requestBody, responseBody any) error {
	var encoded []byte
	if requestBody != nil {
		var err error
		encoded, err = json.Marshal(requestBody)
		if err != nil {
			return err
		}
	}
	response, err := r.do(ctx, method, endpoint, func() io.Reader {
		if encoded == nil {
			return nil
		}
		return bytes.NewReader(encoded)
	}, func(request *http.Request) {
		if request.Header.Get("Accept") == "" {
			request.Header.Set("Accept", "application/json")
		}
		if requestBody != nil {
			request.Header.Set("Content-Type", "application/json")
		}
	})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limit := r.maxJSON
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = MaxRemoteError
	}
	data, err := ReadBounded(response.Body, limit)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &RemoteError{Kind: r.kind, Status: response.StatusCode, Method: method, URL: SanitizeEndpoint(endpoint), Body: r.Redact(string(data))}
	}
	if responseBody != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, responseBody); err != nil {
			return fmt.Errorf("decode response from %s: %w", SanitizeEndpoint(endpoint), err)
		}
	}
	return nil
}

// DoBody sends a known-length non-JSON request and decodes a bounded JSON
// response. Mutating calls are intentionally single-shot in do.
func (r *HTTPRequester) DoBody(ctx context.Context, method, endpoint, contentType string, contentLength int64, body io.Reader, responseBody any) error {
	response, err := r.do(ctx, method, endpoint, func() io.Reader { return body }, func(request *http.Request) {
		request.Header.Set("Content-Type", contentType)
		request.Header.Set("Accept", "application/json")
		request.ContentLength = contentLength
		request.Header.Del("Expect")
	})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	limit := r.maxJSON
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = MaxRemoteError
	}
	data, err := ReadBounded(response.Body, limit)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &RemoteError{Kind: r.kind, Status: response.StatusCode, Method: method, URL: SanitizeEndpoint(endpoint), Body: r.Redact(string(data))}
	}
	if responseBody != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, responseBody); err != nil {
			return fmt.Errorf("decode response from %s: %w", SanitizeEndpoint(endpoint), err)
		}
	}
	return nil
}

func (r *HTTPRequester) Download(ctx context.Context, endpoint string) ([]byte, error) {
	response, err := r.do(ctx, http.MethodGet, endpoint, func() io.Reader { return nil }, nil)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := ReadBounded(response.Body, MaxRemoteError)
		return nil, &RemoteError{Kind: r.kind, Status: response.StatusCode, Method: http.MethodGet, URL: SanitizeEndpoint(endpoint), Body: r.Redact(string(data))}
	}
	return ReadBounded(response.Body, r.maxBytes)
}

func (r *HTTPRequester) DownloadReader(ctx context.Context, endpoint, accept string) (io.ReadCloser, error) {
	response, err := r.do(ctx, http.MethodGet, endpoint, func() io.Reader { return nil }, func(request *http.Request) {
		if accept != "" {
			request.Header.Set("Accept", accept)
		}
	})
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		defer response.Body.Close()
		data, _ := ReadBounded(response.Body, MaxRemoteError)
		return nil, &RemoteError{Kind: r.kind, Status: response.StatusCode, Method: http.MethodGet, URL: SanitizeEndpoint(endpoint), Body: r.Redact(string(data))}
	}
	return response.Body, nil
}

func (r *HTTPRequester) do(ctx context.Context, method, endpoint string, body func() io.Reader, configure func(*http.Request)) (*http.Response, error) {
	attempts := 1
	if method == http.MethodGet || method == http.MethodHead {
		attempts = maxReadAttempts
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		request, err := r.NewRequest(ctx, method, endpoint, body())
		if err != nil {
			return nil, err
		}
		if configure != nil {
			configure(request)
		}
		response, err := r.client.Do(request)
		if err == nil && !retryableStatus(response.StatusCode) {
			return response, nil
		}
		if err == nil && attempt == attempts {
			return response, nil
		}
		if err != nil && (!retryableTransportError(err) || attempt == attempts) {
			return nil, r.SanitizeError(err)
		}

		delay := time.Duration(1<<uint(attempt-1)) * 250 * time.Millisecond
		if err == nil {
			delay = retryAfter(response.Header.Get("Retry-After"), delay)
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
			_ = response.Body.Close()
		}
		delay = r.jitter(delay)
		if delay > 30*time.Second {
			delay = 30 * time.Second
		}
		if sleepErr := r.sleep(ctx, delay); sleepErr != nil {
			return nil, r.SanitizeError(sleepErr)
		}
	}
	return nil, errors.New("remote request retry loop exhausted")
}

func retryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func retryableTransportError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var certificateError *tls.CertificateVerificationError
	if errors.As(err, &certificateError) {
		return false
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return false
	}
	var networkError net.Error
	return errors.As(err, &networkError) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func retryAfter(value string, fallback time.Duration) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := time.Until(when); delay > 0 {
			return delay
		}
	}
	return fallback
}

func ReadBounded(reader io.Reader, limit int64) ([]byte, error) {
	limited := io.LimitReader(reader, limit+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("remote response exceeds %d bytes", limit)
	}
	return data, nil
}

func SanitizeEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

func (r *HTTPRequester) Redact(value string) string {
	for _, secret := range r.secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func (r *HTTPRequester) SanitizeError(err error) error {
	if err == nil {
		return nil
	}
	return &sanitizedRequestError{
		message: fmt.Sprintf("%s remote request failed: %s", r.kind, r.Redact(err.Error())),
		cause:   err,
	}
}

func (r *HTTPRequester) Client() *http.Client { return r.client }
