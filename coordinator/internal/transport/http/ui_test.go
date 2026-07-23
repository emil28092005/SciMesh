package http

import "testing"

func TestUIStatusPresentation(t *testing.T) {
	tests := []struct {
		status string
		label  string
		class  string
	}{
		{"pending", "Waiting for a worker", "waiting"},
		{"running", "Running", "active"},
		{"completed", "Tasks complete", "success"},
		{"failed", "Needs attention", "danger"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			if got := uiStatusLabel(test.status); got != test.label {
				t.Errorf("label = %q, want %q", got, test.label)
			}
			if got := uiStatusClass(test.status); got != test.class {
				t.Errorf("class = %q, want %q", got, test.class)
			}
		})
	}
}

func TestUIProgressPercent(t *testing.T) {
	if got := uiProgressPercent(3, 1, 0, 8); got != 50 {
		t.Errorf("progress = %d, want 50", got)
	}
	if got := uiProgressPercent(1, 1, 0, 0); got != 0 {
		t.Errorf("empty progress = %d, want 0", got)
	}
}

func TestUITaskErrorPresentationDoesNotExposeCommand(t *testing.T) {
	if got := uiTaskErrorLabel("CalledProcessError"); got != "Local calculation failed" {
		t.Errorf("error label = %q", got)
	}
	if got := uiTaskErrorHint("CalledProcessError"); got == "" {
		t.Error("error hint must explain the failure")
	}
}
