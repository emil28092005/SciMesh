package domain

import (
	"errors"
	"testing"
)

func TestNewWorker(t *testing.T) {
	w, err := NewWorker("lab-01", []string{"similarity_search"}, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if w.Status != WorkerOnline {
		t.Errorf("status = %q, want online", w.Status)
	}
	if w.ID.String() == "" {
		t.Error("worker must get an id")
	}
	if !w.LastHeartbeatAt.Equal(testNow) || !w.CreatedAt.Equal(testNow) {
		t.Error("timestamps must be stamped")
	}
}

func TestNewWorkerRejectsNoCapabilities(t *testing.T) {
	if _, err := NewWorker("lab-01", nil, testNow); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("err = %v, want ErrInvalidInput", err)
	}
	if _, err := NewWorker("lab-01", []string{}, testNow); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty slice: err = %v, want ErrInvalidInput", err)
	}
}
