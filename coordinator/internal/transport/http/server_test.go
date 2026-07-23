package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emil28092005/SciMesh/coordinator/internal/memstore"
	coordhttp "github.com/emil28092005/SciMesh/coordinator/internal/transport/http"
	"github.com/emil28092005/SciMesh/coordinator/internal/usecase"
)

const token = "secret"
const uiToken = "ui-secret"

type env struct {
	ts    *httptest.Server
	blobs *memstore.BlobStore
}

func newEnv(t *testing.T, ready func(context.Context) error) *env {
	return newEnvWithUIToken(t, ready, uiToken)
}

func newEnvWithUIToken(t *testing.T, ready func(context.Context) error, configuredUIToken string) *env {
	t.Helper()
	tasks := memstore.NewTaskRepo()
	jobs := memstore.NewJobRepo()
	work := memstore.NewWorkerRepo()
	arts := memstore.NewArtifactRepo()
	blobs := memstore.NewBlobStore()
	clk := memstore.NewClock(time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC))
	tx := memstore.Tx{}
	lease := 2 * time.Minute

	uc := coordhttp.UseCases{
		RegisterWorker:   usecase.NewRegisterWorker(work, clk),
		CreateJob:        usecase.NewCreateJob(jobs, tasks, tx, clk),
		SubmitDataset:    usecase.NewSubmitDataset(blobs, arts, jobs, tasks, tx, clk),
		ClaimTask:        usecase.NewClaimTask(tasks, clk, lease),
		RenewLease:       usecase.NewRenewLease(tasks, work, tx, clk, lease),
		CompleteTask:     usecase.NewCompleteTask(tasks, jobs, arts, tx, clk),
		FailTask:         usecase.NewFailTask(tasks, jobs, tx, clk),
		GetJobStatus:     usecase.NewGetJobStatus(jobs, tasks),
		UploadArtifact:   usecase.NewUploadArtifact(tasks, arts, blobs, clk),
		DownloadArtifact: usecase.NewDownloadArtifact(arts, blobs),
		GetTaskInput:     usecase.NewGetTaskInput(tasks, arts, blobs),
		Dashboard:        usecase.NewDashboard(memstore.NewUIReadRepo(jobs, tasks, work, arts)),
	}
	srv := coordhttp.NewServer(uc, slog.New(slog.NewTextHandler(io.Discard, nil)), 5*time.Second, 15*time.Second, 1<<30, ready)
	ts := httptest.NewServer(srv.Handler(token, configuredUIToken))
	t.Cleanup(ts.Close)
	return &env{ts: ts, blobs: blobs}
}

func healthy(context.Context) error { return nil }

// do sends an authenticated JSON request and returns status + decoded body.
func (e *env) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), method, e.ts.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var m map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

// get issues an unauthenticated GET and returns the response, failing on error.
func (e *env) get(t *testing.T, path string) *http.Response {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), "GET", e.ts.URL+path, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func TestHealthOK(t *testing.T) {
	e := newEnv(t, healthy)
	resp := e.get(t, "/health") // unauthenticated
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestUIRequiresDistinctCredentialAndRendersDashboard(t *testing.T) {
	e := newEnv(t, healthy)
	request := func() *http.Request {
		req, _ := http.NewRequestWithContext(context.Background(), "GET", e.ts.URL+"/ui", nil)
		return req
	}
	resp, err := http.DefaultClient.Do(request())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no UI auth: %d", resp.StatusCode)
	}

	req := request()
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("worker token authorized UI: %d", resp.StatusCode)
	}

	req = request()
	req.SetBasicAuth("operator", uiToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("UI status: %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "SciMesh operator UI") {
		t.Errorf("dashboard body missing title")
	}
}

func TestUIDisabledReturnsNotFound(t *testing.T) {
	e := newEnvWithUIToken(t, healthy, "")
	resp := e.get(t, "/ui")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled UI = %d, want 404", resp.StatusCode)
	}
}

func TestUIRejectsCrossOriginUpload(t *testing.T) {
	e := newEnv(t, healthy)
	req, _ := http.NewRequestWithContext(context.Background(), "POST", e.ts.URL+"/ui/api/jobs/upload", strings.NewReader("dataset=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://attacker.example")
	req.SetBasicAuth("operator", uiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin upload = %d, want 403", resp.StatusCode)
	}
}

func TestUIUploadDatasetCreatesJob(t *testing.T) {
	e := newEnv(t, healthy)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("workload", "similarity-search")
	_ = mw.WriteField("parameters", `{"query_smiles":"CCO","top_k":20,"progress_every":0}`)
	_ = mw.WriteField("chunk_rows", "1000")
	file, err := mw.CreateFormFile("file", "chembl.tsv")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(file, "chembl_id\tcanonical_smiles\nCHEMBL1\tCCO\n")
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequestWithContext(context.Background(), "POST", e.ts.URL+"/ui/api/jobs/upload", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.SetBasicAuth("operator", uiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		result, _ := io.ReadAll(resp.Body)
		t.Fatalf("UI upload = %d: %s", resp.StatusCode, result)
	}
}

func TestUIJobAndArtifactAreScopedToTheirJob(t *testing.T) {
	e := newEnv(t, healthy)
	code, job := e.do(t, "POST", "/jobs", `{"workload":"w","input_uri":"s3://in","chunks":[{"chunk_index":0,"input_uri":"s3://c","input_sha256":"sha"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("create: %d", code)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", e.ts.URL+"/ui/jobs/"+job["id"].(string), nil)
	req.SetBasicAuth("operator", uiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("detail: %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("missing UI CSP")
	}
}

func TestUIArtifactDownloadRejectsAnotherJobsArtifact(t *testing.T) {
	e := newEnv(t, healthy)
	code, _ := e.do(t, "POST", "/jobs", `{"workload":"w","input_uri":"s3://in","chunks":[{"chunk_index":0,"input_uri":"s3://c","input_sha256":"sha"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("first job: %d", code)
	}
	_, claim := e.do(t, "POST", "/tasks/claim", `{"worker_id":"w1","capabilities":["w"]}`)
	artifactID := e.putArtifact(t, claim["task_id"].(string), "w1", int(claim["attempt"].(float64)), "result")
	code, second := e.do(t, "POST", "/jobs", `{"workload":"w","input_uri":"s3://in","chunks":[{"chunk_index":0,"input_uri":"s3://c","input_sha256":"sha"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("second job: %d", code)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", e.ts.URL+"/ui/jobs/"+second["id"].(string)+"/artifacts/"+artifactID, nil)
	req.SetBasicAuth("operator", uiToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("cross-job artifact = %d, want 404", resp.StatusCode)
	}
}

func TestHealthUnavailableWhenDBDown(t *testing.T) {
	e := newEnv(t, func(context.Context) error { return context.DeadlineExceeded })
	resp := e.get(t, "/health")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t, healthy)
	send := func(authz string) int {
		req, _ := http.NewRequestWithContext(context.Background(), "POST", e.ts.URL+"/tasks/claim",
			strings.NewReader(`{"worker_id":"w1"}`))
		req.Header.Set("Content-Type", "application/json")
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if code := send(""); code != 401 {
		t.Errorf("no token: status = %d, want 401", code)
	}
	if code := send("Bearer nope"); code != 401 {
		t.Errorf("wrong token: status = %d, want 401", code)
	}
}

func TestRegisterWorker(t *testing.T) {
	e := newEnv(t, healthy)
	code, body := e.do(t, "POST", "/workers/register", `{"name":"lab","capabilities":["w"]}`)
	if code != 201 {
		t.Fatalf("status = %d, want 201", code)
	}
	if body["worker_id"] == nil || body["heartbeat_interval_seconds"] == nil {
		t.Errorf("missing fields in %v", body)
	}
}

func TestRegisterRejectsNoCapabilities(t *testing.T) {
	e := newEnv(t, healthy)
	if code, _ := e.do(t, "POST", "/workers/register", `{"name":"lab"}`); code != 400 {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestFullLifecycle(t *testing.T) {
	e := newEnv(t, healthy)

	// Create a one-chunk job.
	code, job := e.do(t, "POST", "/jobs", `{
		"workload":"w","input_uri":"s3://in",
		"chunks":[{"chunk_index":0,"input_uri":"s3://c0","input_sha256":"sha"}]}`)
	if code != 201 {
		t.Fatalf("create job: %d", code)
	}
	jobID := job["id"].(string)

	// Claim it.
	code, claim := e.do(t, "POST", "/tasks/claim", `{"worker_id":"w1","capabilities":["w"]}`)
	if code != 200 {
		t.Fatalf("claim: %d", code)
	}
	taskID := claim["task_id"].(string)
	attempt := int(claim["attempt"].(float64))

	// Heartbeat.
	if code, _ := e.do(t, "POST", "/tasks/"+taskID+"/heartbeat",
		`{"worker_id":"w1","attempt":`+itoa(attempt)+`}`); code != 200 {
		t.Fatalf("heartbeat: %d", code)
	}

	// Upload a result artifact (PUT, headers carry identity).
	artID := e.putArtifact(t, taskID, "w1", attempt, "q,m\nA,B\n")

	// Submit the result by artifact id.
	if code, _ := e.do(t, "POST", "/tasks/"+taskID+"/result",
		`{"worker_id":"w1","attempt":`+itoa(attempt)+`,"result":{"artifact_id":"`+artID+`"}}`); code != 200 {
		t.Fatalf("result: %d", code)
	}

	// Job is now completed.
	code, prog := e.do(t, "GET", "/jobs/"+jobID, "")
	if code != 200 || prog["status"] != "completed" {
		t.Errorf("job status = %v (code %d), want completed", prog["status"], code)
	}
}

func TestForeignArtifactResultConflict(t *testing.T) {
	e := newEnv(t, healthy)
	e.do(t, "POST", "/jobs", `{"workload":"w","input_uri":"s3://in",
		"chunks":[{"chunk_index":0,"input_uri":"s3://c0","input_sha256":"sha"},
		          {"chunk_index":1,"input_uri":"s3://c1","input_sha256":"sha"}]}`)

	_, cA := e.do(t, "POST", "/tasks/claim", `{"worker_id":"w1","capabilities":["w"]}`)
	_, cB := e.do(t, "POST", "/tasks/claim", `{"worker_id":"w1","capabilities":["w"]}`)
	taskA, attA := cA["task_id"].(string), int(cA["attempt"].(float64))
	taskB, attB := cB["task_id"].(string), int(cB["attempt"].(float64))
	artA := e.putArtifact(t, taskA, "w1", attA, "data")

	// Complete taskB with taskA's artifact → 409.
	if code, _ := e.do(t, "POST", "/tasks/"+taskB+"/result",
		`{"worker_id":"w1","attempt":`+itoa(attB)+`,"result":{"artifact_id":"`+artA+`"}}`); code != 409 {
		t.Errorf("cross-task result: status = %d, want 409", code)
	}
}

func TestUploadDatasetChunksAndServesInput(t *testing.T) {
	e := newEnv(t, healthy)
	tsv := "id\tsmiles\nA\tCC\nB\tCCC\nC\tCCCC\nD\tCCCCC\nE\tCCCCCC\n"

	code, body := e.uploadDataset(t, "w", 2, tsv)
	if code != 201 {
		t.Fatalf("upload: status = %d", code)
	}
	if int(body["task_count"].(float64)) != 3 {
		t.Fatalf("task_count = %v, want 3", body["task_count"])
	}

	// Claim a shard, follow its input.uri, and pull the shard bytes.
	_, claim := e.do(t, "POST", "/tasks/claim", `{"worker_id":"w1","capabilities":["w"]}`)
	input := claim["input"].(map[string]any)
	uri := input["uri"].(string)
	if !strings.HasPrefix(uri, "/tasks/") || !strings.HasSuffix(uri, "/input") {
		t.Fatalf("input.uri = %q", uri)
	}
	req, _ := http.NewRequestWithContext(context.Background(), "GET", e.ts.URL+uri, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get input: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("get input: status = %d", resp.StatusCode)
	}
	shard, _ := io.ReadAll(resp.Body)
	if !strings.HasPrefix(string(shard), "id\tsmiles\n") {
		t.Errorf("shard missing header: %q", shard)
	}
}

func TestErrorMappings(t *testing.T) {
	e := newEnv(t, healthy)
	zero := "00000000-0000-0000-0000-000000000000"

	if code, _ := e.do(t, "GET", "/jobs/"+zero, ""); code != 404 {
		t.Errorf("unknown job: %d, want 404", code)
	}
	if code, _ := e.do(t, "POST", "/tasks/not-a-uuid/heartbeat", `{"worker_id":"w1","attempt":1}`); code != 400 {
		t.Errorf("malformed uuid: %d, want 400", code)
	}
	if code, _ := e.do(t, "POST", "/tasks/claim", `{"worker_id":"w1","totally_unknown":1}`); code != 400 {
		t.Errorf("unknown field: %d, want 400", code)
	}
}

func TestJSONRejectsTrailingValue(t *testing.T) {
	e := newEnv(t, healthy)
	if code, _ := e.do(t, "POST", "/workers/register",
		`{"name":"lab","capabilities":["w"]} {}`); code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", code)
	}
}

func TestUploadDatasetRejectsAmbiguousMultipartInput(t *testing.T) {
	e := newEnv(t, healthy)
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("workload", "w")
	_ = mw.WriteField("chunk_rows", "not-a-number")
	fw, _ := mw.CreateFormFile("file", "chembl.tsv")
	_, _ = io.Copy(fw, strings.NewReader("id\tsmiles\nA\tCC\n"))
	_ = mw.Close()

	req, _ := http.NewRequestWithContext(context.Background(), "POST", e.ts.URL+"/jobs/upload", &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// --- helpers -------------------------------------------------------------

func (e *env) putArtifact(t *testing.T, taskID, worker string, attempt int, data string) string {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), "PUT",
		e.ts.URL+"/tasks/"+taskID+"/artifacts/r.csv", strings.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/csv")
	req.Header.Set("X-Worker-ID", worker)
	req.Header.Set("X-Task-Attempt", itoa(attempt))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put artifact: status = %d", resp.StatusCode)
	}
	var m map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &m)
	return m["artifact_id"].(string)
}

func (e *env) uploadDataset(t *testing.T, workload string, rows int, tsv string) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("workload", workload)
	_ = mw.WriteField("chunk_rows", itoa(rows))
	fw, _ := mw.CreateFormFile("file", "chembl.tsv")
	_, _ = io.Copy(fw, strings.NewReader(tsv))
	_ = mw.Close()

	req, _ := http.NewRequestWithContext(context.Background(), "POST", e.ts.URL+"/jobs/upload", &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	b, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(b, &m)
	return resp.StatusCode, m
}

func itoa(n int) string { return strconv.Itoa(n) }
