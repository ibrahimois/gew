package forge

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gew/internal/version"
)

const (
	MaxRemoteJSON     = int64(16 << 20)
	MaxRemoteError    = int64(1 << 20)
	MaxRemoteSnapshot = int64(1 << 30)
)

type AuthPolicy func(*http.Request)

type HTTPRequester struct {
	kind     ForgeKind
	baseURL  string
	client   *http.Client
	auth     AuthPolicy
	secrets  []string
	headers  http.Header
	maxJSON  int64
	maxBytes int64
}

func NewHTTPRequester(p Config, baseURL string, auth AuthPolicy, headers http.Header) *HTTPRequester {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if p.Insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // explicitly requested by user profile
	}
	secrets := []string{p.Token}
	client := &http.Client{Transport: transport, Timeout: 90 * time.Second}
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
	}
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
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := r.NewRequest(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", "application/json")
	}
	if requestBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := r.client.Do(request)
	if err != nil {
		return r.SanitizeError(err)
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
	request, err := r.NewRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, r.SanitizeError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := ReadBounded(response.Body, MaxRemoteError)
		return nil, &RemoteError{Kind: r.kind, Status: response.StatusCode, Method: http.MethodGet, URL: SanitizeEndpoint(endpoint), Body: r.Redact(string(data))}
	}
	return ReadBounded(response.Body, r.maxBytes)
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
	return fmt.Errorf("%s remote request failed: %s", r.kind, r.Redact(err.Error()))
}

func (r *HTTPRequester) Client() *http.Client { return r.client }
