package http

import "testing"

func TestUIStatusPresentation(t *testing.T) {
	tests := []struct {
		status string
		label  string
		class  string
	}{
		{"pending", "Ожидает worker", "waiting"},
		{"running", "Выполняется", "active"},
		{"completed", "Задачи завершены", "success"},
		{"failed", "Требует внимания", "danger"},
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
	if got := uiProgressPercent(3, 1, 8); got != 50 {
		t.Errorf("progress = %d, want 50", got)
	}
	if got := uiProgressPercent(1, 1, 0); got != 0 {
		t.Errorf("empty progress = %d, want 0", got)
	}
}
