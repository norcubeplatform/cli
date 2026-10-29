package config

import (
	"testing"

	"github.com/norcubeplatform/cli/internal/registry"
)

// The Service* constants are the keys code refers to; each must exist in
// the registry, and production must have an endpoint for every service.
func TestServiceKeysMatchRegistry(t *testing.T) {
	for _, key := range []string{ServiceAuth, ServiceWebApp, ServiceSnapDB, ServiceLangsync, ServiceDomainradar, ServiceBilling, ServicePrompthub} {
		if _, ok := registry.Get(key); !ok {
			t.Errorf("service %q is not in services.yaml", key)
		}
	}
	prod, ok := EnvironmentEndpoints(EnvProduction)
	if !ok {
		t.Fatal("no production environment")
	}
	for _, key := range Services {
		if prod[key] == "" {
			t.Errorf("production has no endpoint for %s", key)
		}
	}
}
