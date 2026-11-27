package cli

import (
	"testing"

	"github.com/norcubeplatform/cli/internal/auth"
)

// The audience names users already type keep working, and come from the registry.
func TestAuthTokenAudiencesComeFromTheRegistry(t *testing.T) {
	for name, want := range map[string]string{
		"auth": auth.AudienceAuth, "billing": auth.AudienceBilling, "backup": auth.AudienceSnapDB, "snapdb": auth.AudienceSnapDB,
		"langsync": auth.AudienceLangsync, "domainradar": auth.AudienceDomainradar, "prompthub": auth.AudiencePrompthub,
		"monitor": auth.AudienceMonitor, "pentest": "pentest",
	} {
		if got := audiences[name]; got != want {
			t.Errorf("--audience %s → %q, want %q", name, got, want)
		}
	}
}
