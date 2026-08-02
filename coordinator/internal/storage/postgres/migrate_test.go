package postgres

import (
	"strings"
	"testing"
)

func TestListMigrationsParsesAndOrdersEmbeddedFiles(t *testing.T) {
	migrations, err := listMigrations()
	if err != nil {
		t.Fatalf("list migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no embedded migrations")
	}
	for index, item := range migrations {
		if item.version != index+1 {
			t.Errorf("migration %d has version %d, want contiguous ordering", index, item.version)
		}
		if item.name != expectedMigrationName(item.version) {
			t.Errorf("migration %d file is %q, want %q", item.version, item.name, expectedMigrationName(item.version))
		}
		if strings.TrimSpace(item.sql) == "" {
			t.Errorf("migration %d is empty", item.version)
		}
	}
}

func expectedMigrationName(version int) string {
	switch version {
	case 1:
		return "0001_init.up.sql"
	case 2:
		return "0002_workers.up.sql"
	case 3:
		return "0003_artifacts.up.sql"
	case 4:
		return "0004_result_artifact.up.sql"
	case 5:
		return "0005_uploaded_input.up.sql"
	case 6:
		return "0006_task_running_enum.up.sql"
	case 7:
		return "0007_task_running_lease.up.sql"
	case 8:
		return "0008_artifact_attempt.up.sql"
	case 9:
		return "0009_unique_partial_result_attempt.up.sql"
	case 10:
		return "0010_job_reduction.up.sql"
	case 11:
		return "0011_job_owner.up.sql"
	case 12:
		return "0012_worker_trust.up.sql"
	case 13:
		return "0013_task_results.up.sql"
	case 14:
		return "0014_workload_settings.up.sql"
	default:
		return ""
	}
}
