package agent

import (
	"testing"
	"time"
)

func validTaskPayload() map[string]any {
	return map[string]any{
		"task_id":          "11111111-1111-4111-8111-111111111111",
		"attempt":          1.0,
		"lease_expires_at": "2026-08-02T00:00:00Z",
		"workload":         "similarity-search",
		"input": map[string]any{
			"uri":    "/tasks/11111111-1111-4111-8111-111111111111/input",
			"sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
		"parameters": map[string]any{"query_smiles": "CCO"},
	}
}

func TestParseTaskAcceptsValidPayload(t *testing.T) {
	task, err := ParseTask(validTaskPayload())
	if err != nil {
		t.Fatalf("ParseTask: %v", err)
	}
	if task.TaskID != "11111111-1111-4111-8111-111111111111" {
		t.Errorf("task id = %q", task.TaskID)
	}
	if task.Attempt != 1 || task.Workload != "similarity-search" {
		t.Errorf("attempt/workload = %d/%q", task.Attempt, task.Workload)
	}
	if task.Input.SHA256 != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("sha256 = %q", task.Input.SHA256)
	}
	if task.LeaseExpiresAt.IsZero() {
		t.Error("lease must parse")
	}
}

func TestParseTaskRejectsInvalidPayloads(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"non-uuid task id", func(p map[string]any) { p["task_id"] = "../outside" }},
		{"zero attempt", func(p map[string]any) { p["attempt"] = 0 }},
		{"naive lease", func(p map[string]any) { p["lease_expires_at"] = "2026-08-02T00:00:00" }},
		{"network-path uri", func(p map[string]any) {
			p["input"].(map[string]any)["uri"] = "//outside.example/input"
		}},
		{"dot-segment uri", func(p map[string]any) {
			p["input"].(map[string]any)["uri"] = "/tasks/../outside/input"
		}},
		{"short sha256", func(p map[string]any) {
			p["input"].(map[string]any)["sha256"] = "abc"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			payload := validTaskPayload()
			test.mutate(payload)
			if _, err := ParseTask(payload); err == nil {
				t.Error("expected ParseTask to reject the payload")
			}
		})
	}
}

func TestParseRegisteredAndUploaded(t *testing.T) {
	registered, err := ParseRegistered(map[string]any{
		"worker_id":                  "22222222-2222-4222-8222-222222222222",
		"heartbeat_interval_seconds": 15.0,
	})
	if err != nil {
		t.Fatalf("ParseRegistered: %v", err)
	}
	if registered.HeartbeatIntervalSeconds != 15 {
		t.Errorf("interval = %v", registered.HeartbeatIntervalSeconds)
	}

	uploaded, err := ParseUploaded(map[string]any{
		"artifact_id": "33333333-3333-4333-8333-333333333333",
		"uri":         "https://coordinator.example/artifacts/333/download",
		"sha256":      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"size_bytes":  12.0,
	})
	if err != nil {
		t.Fatalf("ParseUploaded: %v", err)
	}
	if uploaded.SizeBytes != 12 {
		t.Errorf("size = %d", uploaded.SizeBytes)
	}

	if _, err := ParseUploaded(map[string]any{"artifact_id": "missing"}); err == nil {
		t.Error("expected invalid upload metadata to fail")
	}
}

func TestLeaseHeartbeatDelayIsBelowHalfTTL(t *testing.T) {
	task, err := ParseTask(validTaskPayload())
	if err != nil {
		t.Fatal(err)
	}
	task.LeaseExpiresAt = time.Now().Add(60 * time.Second)
	heartbeat := newLeaseHeartbeat(task, "worker", nil, 15*time.Second)
	delay := heartbeat.nextDelay()
	if delay > 30*time.Second || delay <= 0 {
		t.Errorf("delay = %v, want < 30s", delay)
	}
}
