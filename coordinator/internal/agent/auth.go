package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenProvider supplies the current bearer token. A static token is served
// forever; a worker key is exchanged at the userservice for short-lived JWTs
// and refreshed before they expire (mirroring the former Python worker).
type TokenProvider interface {
	Token() (string, error)
	Refresh() error
}

// StaticToken serves a fixed token forever; empty means no Authorization.
type StaticToken struct{ token string }

func (s *StaticToken) Token() (string, error) { return s.token, nil }
func (s *StaticToken) Refresh() error         { return nil }

// WorkerKeyToken exchanges a long-lived worker key for short-lived JWTs.
type WorkerKeyToken struct {
	userserviceURL string
	workerKey      string
	timeout        time.Duration
	leeway         float64
	mu             sync.Mutex
	token          string
	refreshAt      time.Time
}

func NewWorkerKeyToken(userserviceURL, workerKey string, timeout time.Duration) *WorkerKeyToken {
	return &WorkerKeyToken{
		userserviceURL: strings.TrimRight(userserviceURL, "/"),
		workerKey:      workerKey,
		timeout:        timeout,
		leeway:         0.2,
	}
}

// Token returns the current token, exchanging first when missing or stale.
func (p *WorkerKeyToken) Token() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.token == "" || time.Now().After(p.refreshAt) {
		if err := p.exchangeLocked(); err != nil {
			return "", err
		}
	}
	return p.token, nil
}

// Refresh forces an immediate exchange.
func (p *WorkerKeyToken) Refresh() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.exchangeLocked()
}

func (p *WorkerKeyToken) exchangeLocked() error {
	payload, err := json.Marshal(map[string]string{"key": p.workerKey})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, p.userserviceURL+"/worker-tokens/exchange", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: p.timeout, Transport: tlsTransport(nil)}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("worker key exchange request failed")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("worker key exchange rejected with status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("worker key exchange request failed")
	}
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		return fmt.Errorf("worker key exchange response is invalid")
	}
	token, _ := data["token"].(string)
	if token == "" {
		return fmt.Errorf("worker key exchange response is missing a token")
	}
	var ttl time.Duration
	switch value := data["expires_in"].(type) {
	case float64:
		ttl = time.Duration(value * float64(time.Second))
	case int:
		ttl = time.Duration(value) * time.Second
	}
	p.token = token
	p.refreshAt = time.Time{}
	if ttl > 0 {
		p.refreshAt = time.Now().Add(time.Duration(float64(ttl) * (1.0 - p.leeway)))
	}
	return nil
}

// NewTokenProvider picks the strategy: a worker key (with userservice) wins
// over a static bearer token.
func NewTokenProvider(workerKey, userserviceURL, bearerToken string, timeout time.Duration) TokenProvider {
	if workerKey != "" && userserviceURL != "" {
		return NewWorkerKeyToken(userserviceURL, workerKey, timeout)
	}
	return &StaticToken{token: bearerToken}
}
