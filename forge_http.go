package main

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
)

const (
	maxRemoteJSON     = int64(16 << 20)
	maxRemoteError    = int64(1 << 20)
	maxRemoteSnapshot = int64(1 << 30)
)

type authPolicy func(*http.Request)

type httpRequester struct {
	kind     ForgeKind
	baseURL  string
	client   *http.Client
	auth     authPolicy
	secrets  []string
	headers  http.Header
	maxJSON  int64
	maxBytes int64
}

func newHTTPRequester(p profile, baseURL string, auth authPolicy, headers http.Header) *httpRequester {
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
		if len(via) > 0 && !sameOrigin(via[0].URL, request.URL) {
			request.Header.Del("Authorization")
			request.Header.Del("Private-Token")
		}
		return nil
	}
	return &httpRequester{
		kind: p.Provider, baseURL: strings.TrimRight(baseURL, "/"), client: client,
		auth: auth, secrets: secrets, headers: headers.Clone(), maxJSON: maxRemoteJSON,
		maxBytes: maxRemoteSnapshot,
	}
}

func sameOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func (r *httpRequester) newRequest(ctx context.Context, method, endpoint string, body io.Reader) (*http.Request, error) {
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
	request.Header.Set("User-Agent", "gew/"+toolVersion)
	if r.auth != nil {
		r.auth(request)
	}
	return request, nil
}

func (r *httpRequester) doJSON(ctx context.Context, method, endpoint string, requestBody, responseBody any) error {
	var body io.Reader
	if requestBody != nil {
		encoded, err := json.Marshal(requestBody)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := r.newRequest(ctx, method, endpoint, body)
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
		return r.sanitizeError(err)
	}
	defer response.Body.Close()
	limit := r.maxJSON
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		limit = maxRemoteError
	}
	data, err := readBounded(response.Body, limit)
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &RemoteError{Kind: r.kind, Status: response.StatusCode, Method: method, URL: sanitizeEndpoint(endpoint), Body: r.redact(string(data))}
	}
	if responseBody != nil && len(bytes.TrimSpace(data)) > 0 {
		if err := json.Unmarshal(data, responseBody); err != nil {
			return fmt.Errorf("decode response from %s: %w", sanitizeEndpoint(endpoint), err)
		}
	}
	return nil
}

func (r *httpRequester) download(ctx context.Context, endpoint string) ([]byte, error) {
	request, err := r.newRequest(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	response, err := r.client.Do(request)
	if err != nil {
		return nil, r.sanitizeError(err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := readBounded(response.Body, maxRemoteError)
		return nil, &RemoteError{Kind: r.kind, Status: response.StatusCode, Method: http.MethodGet, URL: sanitizeEndpoint(endpoint), Body: r.redact(string(data))}
	}
	return readBounded(response.Body, r.maxBytes)
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
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

func sanitizeEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return endpoint
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	parsed.User = nil
	return parsed.String()
}

func (r *httpRequester) redact(value string) string {
	for _, secret := range r.secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}

func (r *httpRequester) sanitizeError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s remote request failed: %s", r.kind, r.redact(err.Error()))
}
