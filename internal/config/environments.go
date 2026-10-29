package config

import (
	"strings"

	"github.com/norcubeplatform/cli/internal/registry"
)

// Service keys, as used in [profiles.<name>.endpoints] and --endpoint-url.
const (
	ServiceAuth        = "auth"
	ServiceWebApp      = "web_app"
	ServiceSnapDB      = "snapdb"
	ServiceLangsync    = "langsync"
	ServiceDomainradar = "domainradar"
	ServiceBilling     = "billing"
	ServicePrompthub   = "prompthub"
)

// Services lists every service the CLI has an endpoint for, in display
// order (the order of services.yaml).
var Services = registry.Keys()

// IsService reports whether name is a known service key.
func IsService(name string) bool {
	for _, s := range Services {
		if s == name {
			return true
		}
	}
	return false
}

// ServiceEnvVar is the environment variable that overrides a service's
// endpoint: NORCUBE_WEB_APP for the web app, NORCUBE_<SERVICE>_URL for the
// rest.
func ServiceEnvVar(service string) string {
	if service == ServiceWebApp {
		return "NORCUBE_WEB_APP"
	}
	return "NORCUBE_" + strings.ToUpper(service) + "_URL"
}

// Environments a profile can select.
const (
	EnvProduction = "production"
	// EnvCustom has no built-in endpoints: the profile lists its own, and
	// any it leaves out fall back to production.
	EnvCustom = "custom"
)

// environments holds the built-in endpoint sets. Production comes from the
// service registry; custom has none of its own.
var environments = map[string]map[string]string{
	EnvProduction: productionEndpoints(),
}

func productionEndpoints() map[string]string {
	out := map[string]string{}
	for _, s := range registry.All() {
		out[s.Key] = s.Endpoint
	}
	return out
}

// EnvironmentEndpoints returns the built-in endpoints of a named
// environment. Custom (and unknown) environments have none.
func EnvironmentEndpoints(env string) (map[string]string, bool) {
	e, ok := environments[env]
	if !ok {
		return nil, false
	}
	out := make(map[string]string, len(e))
	for k, v := range e {
		out[k] = v
	}
	return out, true
}

// IsEnvironment reports whether env is a valid profile environment.
func IsEnvironment(env string) bool {
	_, builtIn := environments[env]
	return builtIn || env == EnvCustom
}
