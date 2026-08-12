package backup

import (
	"testing"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
)

func strPtr(s string) *string { return &s }
func boolPtr(b bool) *bool    { return &b }
func intPtr(n int) *int       { return &n }

func statusPtr(s snapdb.ConstantsBackupJobStatus) *snapdb.ConstantsBackupJobStatus { return &s }

// formatVerify's core contract: a never-tested backup renders "—", which must
// never be confusable with a failure; in-flight tests render "testing…"
// regardless of any stale verdict fields.
func TestFormatVerify(t *testing.T) {
	cases := []struct {
		name string
		job  snapdb.DtoBackupJob
		want string
	}{
		{"never tested", snapdb.DtoBackupJob{}, "—"},
		{"queued", snapdb.DtoBackupJob{VerifyStatus: statusPtr(snapdb.JobStatusQueued)}, "testing…"},
		{"running overrides old verdict", snapdb.DtoBackupJob{
			VerifyStatus: statusPtr(snapdb.JobStatusRunning),
			VerifyPassed: boolPtr(true),
		}, "testing…"},
		{"passed", snapdb.DtoBackupJob{
			VerifyStatus: statusPtr(snapdb.JobStatusSuccess),
			VerifyPassed: boolPtr(true),
		}, "passed"},
		{"passed with warnings", snapdb.DtoBackupJob{
			VerifyStatus:   statusPtr(snapdb.JobStatusSuccess),
			VerifyPassed:   boolPtr(true),
			VerifyWarnings: strPtr("index skipped"),
		}, "passed (warnings)"},
		{"validation failed", snapdb.DtoBackupJob{
			VerifyStatus: statusPtr(snapdb.JobStatusSuccess),
			VerifyPassed: boolPtr(false),
		}, "failed"},
		{"restore itself failed", snapdb.DtoBackupJob{
			VerifyStatus: statusPtr(snapdb.JobStatusFailed),
		}, "failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatVerify(tc.job); got != tc.want {
				t.Errorf("formatVerify() = %q, want %q", got, tc.want)
			}
		})
	}
}

// healthRow's core contract: zero restore tests renders dashes across the
// stat columns — "never tested" and "0% passing" must not look alike.
func TestHealthRow(t *testing.T) {
	never := snapdb.DtoDataSource{Name: "prod-postgres", Engine: "postgres"}
	got := healthRow(never)
	want := []string{"prod-postgres", "postgres", "—", "—", "—", "—"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("healthRow(never tested)[%d] = %q, want %q", i, got[i], want[i])
		}
	}

	tested := snapdb.DtoDataSource{
		Name:                 "prod-postgres",
		Engine:               "postgres",
		RestoreCheckTotal:    intPtr(28),
		RestoreCheckPassed:   intPtr(27),
		RestoreCheckWarnings: intPtr(1),
		RestoreCheckLastAt:   strPtr("2026-08-12T04:00:00Z"),
	}
	got = healthRow(tested)
	want = []string{"prod-postgres", "postgres", "96%", "27/28 passed", "1", "2026-08-12 04:00:00"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("healthRow(tested)[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
