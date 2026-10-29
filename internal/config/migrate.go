package config

// fileV1 is the flat format written by norcube before profiles: one set of
// URLs, one user, one active organization.
type fileV1 struct {
	Auth        string `toml:"auth"`
	WebApp      string `toml:"web_app"`
	SnapDB      string `toml:"snapdb"`
	Langsync    string `toml:"langsync"`
	Domainradar string `toml:"domainradar"`
	Billing     string `toml:"billing"`
	Prompthub   string `toml:"prompthub"`

	User      User `toml:"user"`
	ActiveOrg struct {
		ID   string `toml:"id"`
		Slug string `toml:"slug"`
		Name string `toml:"name"`
	} `toml:"active_org"`
}

var v1Keys = []string{
	"auth", "web_app", "snapdb", "langsync", "domainradar", "billing", "prompthub",
	"user", "active_org",
}

func removeV1Keys(raw map[string]any) {
	for _, k := range v1Keys {
		delete(raw, k)
	}
}

// migrateV1 maps a version 1 file to profile "default". The environment is
// the one whose auth URL matches (production when none is set); URLs that
// differ from that environment become endpoint overrides, and an unknown
// auth URL makes a custom profile with every URL listed.
func migrateV1(v1 fileV1) *File {
	urls := map[string]string{
		ServiceAuth:        v1.Auth,
		ServiceWebApp:      v1.WebApp,
		ServiceSnapDB:      v1.SnapDB,
		ServiceLangsync:    v1.Langsync,
		ServiceDomainradar: v1.Domainradar,
		ServiceBilling:     v1.Billing,
		ServicePrompthub:   v1.Prompthub,
	}
	env, endpoints := EnvironmentFor(urls)
	p := &Profile{Environment: env, Endpoints: endpoints, User: v1.User}
	p.SetActiveOrg(Organization{ID: v1.ActiveOrg.ID, Slug: v1.ActiveOrg.Slug, Name: v1.ActiveOrg.Name})
	return &File{
		SchemaVersion:  SchemaVersion,
		DefaultProfile: DefaultProfileName,
		Profiles:       map[string]*Profile{DefaultProfileName: p},
	}
}

// EnvironmentFor describes a set of service URLs as a profile environment
// plus overrides: the environment whose auth URL matches (production when
// the auth URL is empty), with every URL that differs from it as an
// endpoint override. An unknown auth URL is a custom environment listing
// every URL given.
func EnvironmentFor(urls map[string]string) (string, map[string]string) {
	env := EnvProduction
	switch urls[ServiceAuth] {
	case "", environments[EnvProduction][ServiceAuth]:
	default:
		env = EnvCustom
	}
	builtIn := environments[env] // nil for custom
	var endpoints map[string]string
	for svc, url := range urls {
		if url == "" || url == builtIn[svc] {
			continue
		}
		if endpoints == nil {
			endpoints = map[string]string{}
		}
		endpoints[svc] = url
	}
	return env, endpoints
}
