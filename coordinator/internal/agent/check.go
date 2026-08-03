package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// CheckItem is one line of the preflight report the setup wizard shows.
type CheckItem struct {
	Name    string `json:"name"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Latency int64  `json:"latency_ms,omitempty"`
}

// CheckReport is the full preflight result of `worker-agent --check` and of
// the wizard's test step.
type CheckReport struct {
	Coordinator        CheckItem `json:"coordinator"`
	Auth               CheckItem `json:"auth"`
	Python             CheckItem `json:"python"`
	Scimesh            CheckItem `json:"scimesh"`
	Agent              string    `json:"agent_version"`
	CoordinatorVersion string    `json:"coordinator_version,omitempty"`
}

// checkHTTP runs one GET and reports reachability + latency, with a fallback
// detail message when the server answers without JSON.
func checkHTTP(ctx context.Context, url string, timeout time.Duration) (CheckItem, string) {
	started := time.Now()
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return CheckItem{Name: "coordinator", OK: false, Detail: "invalid URL"}, ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		detail := err.Error()
		if strings.Contains(detail, "connection refused") {
			detail = "no coordinator answering at this address"
		}
		return CheckItem{Name: "coordinator", OK: false, Detail: detail}, ""
	}
	defer func() { _ = resp.Body.Close() }()
	version := ""
	if resp.StatusCode == http.StatusOK {
		var body struct {
			Status string `json:"status"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err == nil && body.Status == "ok" {
			return CheckItem{Name: "coordinator", OK: true, Latency: time.Since(started).Milliseconds()}, version
		}
	}
	return CheckItem{Name: "coordinator", OK: false, Detail: fmt.Sprintf("HTTP %d", resp.StatusCode)}, version
}

// CheckCoordinator probes the coordinator's /health endpoint.
func CheckCoordinator(ctx context.Context, url string, timeout time.Duration) CheckReport {
	report := CheckReport{Agent: Version}
	item, _ := checkHTTP(ctx, strings.TrimRight(url, "/")+"/health", timeout)
	report.Coordinator = item
	report.Auth = CheckItem{Name: "auth", OK: true, Detail: "no token configured — will be checked at registration"}
	return report
}

// CheckEnvironment verifies the local runtime against the python3 found on
// PATH.
func CheckEnvironment(ctx context.Context) CheckReport {
	python, err := exec.LookPath("python3")
	if err != nil {
		return CheckReport{Agent: Version, Python: CheckItem{Name: "python", OK: false, Detail: "python3 not found on PATH"}}
	}
	return CheckEnvironmentWithPython(ctx, python)
}

// CheckEnvironmentWithPython verifies the local runtime against a specific
// interpreter — the wizard's managed venv python when the runtime installer
// has created one, so the preflight reflects what the worker will actually
// execute with.
func CheckEnvironmentWithPython(ctx context.Context, python string) CheckReport {
	report := CheckReport{Agent: Version, Python: CheckItem{Name: "python", OK: true, Detail: python}}
	//nolint:gosec // G204: python is a resolved interpreter path, the argument list is constant
	cmd := exec.CommandContext(ctx, python, "-c", "import scimesh; print(scimesh.__version__ if hasattr(scimesh, '__version__') else 'installed')")
	out, err := cmd.Output()
	if err != nil {
		// The worker executes workloads by spawning scimesh's task runner, so
		// the package is a hard requirement, not an optimisation. The PyPI
		// name belongs to a different project, so the wizard installs from
		// SCIMESH_PIP_PACKAGE instead of suggesting a bare pip install.
		report.Scimesh = CheckItem{Name: "scimesh", OK: false, Detail: "the worker runs workloads through scimesh — install it from your wheel or index (SCIMESH_PIP_PACKAGE)"}
		return report
	}
	report.Scimesh = CheckItem{Name: "scimesh", OK: true, Detail: strings.TrimSpace(string(out))}
	return report
}

// RunCheck combines the coordinator probe and the local environment probe; it
// is the body behind `worker-agent --check` and the wizard's test step. A
// non-empty python overrides the interpreter probed for the scimesh package
// (the managed venv after a runtime install).
func RunCheck(ctx context.Context, coordinatorURL, python string) CheckReport {
	report := CheckCoordinator(ctx, coordinatorURL, 15*time.Second)
	var env CheckReport
	if python != "" {
		env = CheckEnvironmentWithPython(ctx, python)
	} else {
		env = CheckEnvironment(ctx)
	}
	report.Python = env.Python
	report.Scimesh = env.Scimesh
	return report
}

// Version is the agent build version; main injects it via -ldflags and the
// setup wizard mirrors it into the report. "dev" marks a local build.
var Version = "dev"

// Platform is the host platform string shown on the wizard.
func Platform() string { return runtime.GOOS + "/" + runtime.GOARCH }
