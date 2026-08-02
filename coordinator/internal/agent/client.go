package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CoordinatorError is a non-retriable coordinator response.
type CoordinatorError struct{ msg string }

func (e *CoordinatorError) Error() string { return e.msg }

// TransientError is a timeout, connection error, or 5xx response.
type TransientError struct{ msg string }

func (e *TransientError) Error() string { return e.msg }

// ConflictError means the worker no longer owns the task lease.
type ConflictError struct{ msg string }

func (e *ConflictError) Error() string { return e.msg }

// Client speaks the v1 worker contract over HTTP with a token provider.
//
// API calls never follow redirects (a redirect is a contract violation); the
// artifact download follows redirects but strips the Authorization header on
// cross-origin hops, matching the Python worker's SameOriginAuthRedirectHandler.
// A 401 response refreshes the token exactly once and retries, so a lapsed JWT
// does not fail an in-flight task.
type Client struct {
	baseURL   string
	tokens    TokenProvider
	timeout   time.Duration
	apiClient *http.Client
	dlClient  *http.Client
}

func NewClient(baseURL string, tokens TokenProvider, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		tokens:  tokens,
		timeout: timeout,
		apiClient: &http.Client{
			Timeout:       timeout,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		dlClient: &http.Client{
			Timeout: timeout,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 10 {
					return fmt.Errorf("too many redirects")
				}
				// Go strips Authorization on cross-host redirects by default;
				// strip it explicitly on any origin change to be safe.
				if len(via) > 0 && origin(req.URL) != origin(via[0].URL) {
					req.Header.Del("Authorization")
				}
				return nil
			},
		},
	}
}

func origin(u *url.URL) string {
	return u.Scheme + "://" + u.Host
}

func (c *Client) authHeaders() (map[string]string, error) {
	token, err := c.tokens.Token()
	if err != nil {
		return nil, &CoordinatorError{msg: "token refresh failed: " + err.Error()}
	}
	if token == "" {
		return map[string]string{}, nil
	}
	return map[string]string{"Authorization": "Bearer " + token}, nil
}

func (c *Client) refreshAndRetry() bool {
	return c.tokens.Refresh() == nil
}

// Register advertises the worker and returns its identity and heartbeat policy.
func (c *Client) Register(name string, capabilities []string, cpuCount int, memoryMB int) (*RegisteredWorker, error) {
	payload := map[string]any{
		"name":         name,
		"capabilities": capabilities,
		"cpu_count":    cpuCount,
	}
	if memoryMB > 0 {
		payload["memory_mb"] = memoryMB
	}
	status, body, err := c.requestJSON("POST", "/workers/register", payload)
	if err != nil {
		return nil, err
	}
	if status != http.StatusCreated {
		return nil, &CoordinatorError{msg: fmt.Sprintf("worker registration rejected with status %d", status)}
	}
	return ParseRegistered(body)
}

// Claim leases one compatible task, or returns nil when the queue is empty.
func (c *Client) Claim(workerID string, capabilities []string) (*Task, error) {
	status, body, err := c.requestJSON("POST", "/tasks/claim", map[string]any{
		"worker_id":       workerID,
		"capabilities":    capabilities,
		"max_concurrency": 1,
	})
	if err != nil {
		return nil, err
	}
	if status == http.StatusNoContent {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, &CoordinatorError{msg: fmt.Sprintf("unexpected claim status %d", status)}
	}
	return ParseTask(body)
}

// Heartbeat renews the lease and returns the new deadline.
func (c *Client) Heartbeat(task *Task, workerID string) (time.Time, error) {
	status, body, err := c.requestJSON("POST", "/tasks/"+task.TaskID+"/heartbeat", map[string]any{
		"worker_id": workerID,
		"attempt":   task.Attempt,
	})
	if err != nil {
		return time.Time{}, err
	}
	if status != http.StatusOK {
		if status == http.StatusConflict {
			return time.Time{}, &ConflictError{msg: "heartbeat rejected because the task lease was lost"}
		}
		return time.Time{}, &CoordinatorError{msg: fmt.Sprintf("heartbeat rejected with status %d", status)}
	}
	raw, ok := body["lease_expires_at"].(string)
	if !ok {
		return time.Time{}, &CoordinatorError{msg: "heartbeat response is missing lease_expires_at"}
	}
	lease, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, &CoordinatorError{msg: "heartbeat returned an invalid lease_expires_at"}
	}
	task.LeaseExpiresAt = lease
	task.leaseExpiresRaw = raw
	return lease, nil
}

// Submit completes a task with the uploaded coordinator-owned artifact.
func (c *Client) Submit(task *Task, workerID string, uploaded *Uploaded, metrics map[string]any) error {
	status, _, err := c.requestJSON("POST", "/tasks/"+task.TaskID+"/result", map[string]any{
		"worker_id": workerID,
		"attempt":   task.Attempt,
		"result":    map[string]any{"artifact_id": uploaded.ArtifactID},
		"metrics":   metrics,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		if status == http.StatusConflict {
			return &ConflictError{msg: "result rejected because the task lease was lost"}
		}
		return &CoordinatorError{msg: fmt.Sprintf("result rejected with status %d", status)}
	}
	return nil
}

// Fail reports a sanitized failure.
func (c *Client) Fail(task *Task, workerID string, code, message string, retryable bool) error {
	status, _, err := c.requestJSON("POST", "/tasks/"+task.TaskID+"/failure", map[string]any{
		"worker_id":     workerID,
		"attempt":       task.Attempt,
		"error_code":    code,
		"error_message": message,
		"retryable":     retryable,
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		if status == http.StatusConflict {
			return &ConflictError{msg: "failure rejected because the task lease was lost"}
		}
		return &CoordinatorError{msg: fmt.Sprintf("failure report rejected with status %d", status)}
	}
	return nil
}

// Download streams the task input to destination and returns its SHA-256.
func (c *Client) Download(uri, destination string) (string, error) {
	resolved, err := url.Parse(uri)
	if err != nil {
		return "", fmt.Errorf("invalid input URI: %w", err)
	}
	if !resolved.IsAbs() {
		base, parseErr := url.Parse(c.baseURL)
		if parseErr != nil {
			return "", fmt.Errorf("invalid coordinator URL")
		}
		resolved = base.ResolveReference(resolved)
	}
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return "", fmt.Errorf("input URI must be an HTTP(S) URL")
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, resolved.String(), nil)
	if err != nil {
		return "", err
	}
	headers, err := c.authHeaders()
	if err != nil {
		return "", err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.dlClient.Do(request)
	if err != nil {
		return "", &TransientError{msg: "input download failed"}
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized && c.refreshAndRetry() {
		return c.Download(uri, destination)
	}
	if response.StatusCode != http.StatusOK {
		return "", &CoordinatorError{msg: fmt.Sprintf("input download rejected with status %d", response.StatusCode)}
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o750); err != nil {
		return "", err
	}
	// #nosec G304 -- destination is the worker's own attempt directory file.
	target, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(target, digest), response.Body)
	closeErr := target.Close()
	if copyErr != nil {
		_ = os.Remove(destination)
		return "", &TransientError{msg: "input download interrupted"}
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// Upload streams a partial artifact and verifies the returned metadata.
func (c *Client) Upload(task *Task, workerID string, path, contentType string) (*Uploaded, error) {
	// #nosec G304 -- the upload path is this worker's own artifact file.
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		_ = file.Close()
		return nil, err
	}
	localSHA := hex.EncodeToString(digest.Sum(nil))
	uploadURL := c.baseURL + "/tasks/" + url.PathEscape(task.TaskID) + "/artifacts/" + url.PathEscape(filepath.Base(path))
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPut, uploadURL, file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("X-Worker-ID", workerID)
	request.Header.Set("X-Task-Attempt", strconv.Itoa(task.Attempt))
	headers, err := c.authHeaders()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.apiClient.Do(request)
	_ = file.Close()
	if err != nil {
		return nil, &TransientError{msg: "artifact upload failed"}
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return nil, &TransientError{msg: "artifact upload interrupted"}
	}
	if response.StatusCode == http.StatusConflict {
		return nil, &ConflictError{msg: "artifact upload rejected because the task lease was lost"}
	}
	if response.StatusCode == http.StatusUnauthorized && c.refreshAndRetry() {
		return c.Upload(task, workerID, path, contentType)
	}
	if response.StatusCode != http.StatusOK {
		return nil, &CoordinatorError{msg: fmt.Sprintf("artifact upload rejected with status %d", response.StatusCode)}
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, &CoordinatorError{msg: "artifact upload returned invalid metadata"}
	}
	uploaded, err := ParseUploaded(payload)
	if err != nil {
		return nil, &CoordinatorError{msg: "artifact upload returned invalid metadata"}
	}
	if uploaded.SHA256 != localSHA || uploaded.SizeBytes != info.Size() {
		return nil, &CoordinatorError{msg: "artifact upload metadata does not match local artifact"}
	}
	return uploaded, nil
}

func (c *Client) requestJSON(method, path string, payload any) (int, map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}
	request, err := http.NewRequestWithContext(context.Background(), method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	headers, err := c.authHeaders()
	if err != nil {
		return 0, nil, err
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response, err := c.apiClient.Do(request)
	if err != nil {
		return 0, nil, &TransientError{msg: "coordinator request failed"}
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return 0, nil, &TransientError{msg: "coordinator request interrupted"}
	}
	if response.StatusCode == http.StatusUnauthorized && c.refreshAndRetry() {
		return c.requestJSON(method, path, payload)
	}
	if response.StatusCode >= 500 {
		return response.StatusCode, nil, &TransientError{msg: fmt.Sprintf("coordinator returned %d", response.StatusCode)}
	}
	var decoded map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return response.StatusCode, nil, &CoordinatorError{msg: "coordinator returned invalid JSON"}
		}
	}
	return response.StatusCode, decoded, nil
}
