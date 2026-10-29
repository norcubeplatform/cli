package config

import "strings"

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
// order.
var Services = []string{
	ServiceAuth, ServiceWebApp, ServiceSnapDB, ServiceLangsync,
	ServiceDomainradar, ServiceBilling, ServicePrompthub,
}

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

// Production endpoints.
const (
	DefaultAuth        = "https://auth.api.norcube.com"
	DefaultWebApp      = "https://app.norcube.com"
	DefaultSnapDB      = "https://snapdb.api.norcube.com/app/v1"
	DefaultLangsync    = "https://langsync.api.norcube.com"
	DefaultDomainradar = "https://domainradar.api.norcube.com/app/v1"
	DefaultBilling     = "https://billing.api.norcube.com/app/v1"
	DefaultPrompthub   = "https://prompthub.api.norcube.com"
)

var environments = map[string]map[string]string{
	EnvProduction: {
		ServiceAuth:        DefaultAuth,
		ServiceWebApp:      DefaultWebApp,
		ServiceSnapDB:      DefaultSnapDB,
		ServiceLangsync:    DefaultLangsync,
		ServiceDomainradar: DefaultDomainradar,
		ServiceBilling:     DefaultBilling,
		ServicePrompthub:   DefaultPrompthub,
	},
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
