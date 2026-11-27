// Package registry is the CLI's list of Norcube services, read from the
// embedded services.yaml. Endpoints, audiences, `nrc api` service names and
// code generation all come from here, so adding a service is one manifest
// entry plus its commands.
package registry

import (
	_ "embed"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

//go:embed services.yaml
var manifest []byte

// Service is one entry of services.yaml.
type Service struct {
	// Key names the service in config files, --endpoint-url and
	// NORCUBE_<KEY>_URL. Stable: changing it breaks users' configs.
	Key string `yaml:"key"`
	// Name is the product name shown to people.
	Name string `yaml:"name"`
	// Command is the CLI command group and the service name `nrc api`
	// accepts. Empty for services without commands (the web app).
	Command string `yaml:"command"`
	// Aliases are further names `nrc api` and the command group accept.
	Aliases []string `yaml:"aliases"`
	// Audience is the OAuth audience of the service's access tokens.
	// Empty when the CLI never calls the service with a token.
	Audience string `yaml:"audience"`
	// Endpoint is the production base URL.
	Endpoint string `yaml:"endpoint"`
	// Spec locates the service's Swagger 2.0 file in the backend
	// monorepo: apps/<app>/docs/<file>.
	Spec *Spec `yaml:"spec"`
	// Package is the generated client under internal/api/<package>;
	// empty when no client is generated yet.
	Package string `yaml:"package"`
	// Plugin is the executable that implements the service's commands
	// (`nrc <command> …` runs `<plugin> …`, see docs/PLUGINS.md); empty
	// for services built into the CLI. Only plugins named here ever run.
	Plugin string `yaml:"plugin"`
}

// Spec is where a service's API description lives in the backend
// monorepo.
type Spec struct {
	App  string `yaml:"app"`
	File string `yaml:"file"`
}

// Path is the spec's path relative to the backend monorepo root.
func (s Spec) Path() string { return "apps/" + s.App + "/docs/" + s.File }

// Names returns every name the service answers to in `nrc api`: its
// command, its aliases and its key.
func (s Service) Names() []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range append([]string{s.Command, s.Key}, s.Aliases...) {
		if n != "" && !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	return out
}

var (
	loadOnce sync.Once
	services []Service
	loadErr  error
)

// All returns the services in manifest order. The manifest is embedded and
// validated by tests, so an error here means a broken build; All panics
// instead of making every caller handle it.
func All() []Service {
	loadOnce.Do(func() { services, loadErr = Parse(manifest) })
	if loadErr != nil {
		panic(fmt.Sprintf("registry: embedded services.yaml: %v", loadErr))
	}
	return services
}

// Get returns the service with this key.
func Get(key string) (Service, bool) {
	for _, s := range All() {
		if s.Key == key {
			return s, true
		}
	}
	return Service{}, false
}

// Lookup finds a service by any of its names (command, alias or key),
// ignoring case.
func Lookup(name string) (Service, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, s := range All() {
		for _, n := range s.Names() {
			if n == name {
				return s, true
			}
		}
	}
	return Service{}, false
}

// Keys returns the key of every service with an endpoint, in manifest
// order (a plugin without a public platform API has none).
func Keys() []string {
	var out []string
	for _, s := range All() {
		if s.Endpoint != "" {
			out = append(out, s.Key)
		}
	}
	return out
}

// Callable returns the services `nrc api` can call: those with an
// audience and a command name, sorted by command.
func Callable() []Service {
	var out []Service
	for _, s := range All() {
		if s.Audience != "" && s.Command != "" {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Command < out[j].Command })
	return out
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// Parse reads and validates a manifest.
func Parse(data []byte) ([]Service, error) {
	var doc struct {
		Services []Service `yaml:"services"`
	}
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if len(doc.Services) == 0 {
		return nil, fmt.Errorf("no services")
	}
	names := map[string]string{}
	claim := func(name, owner string) error {
		if !namePattern.MatchString(name) {
			return fmt.Errorf("service %s: invalid name %q", owner, name)
		}
		if prev, ok := names[name]; ok && prev != owner {
			return fmt.Errorf("name %q is used by both %s and %s", name, prev, owner)
		}
		names[name] = owner
		return nil
	}
	for _, s := range doc.Services {
		if s.Key == "" || s.Name == "" {
			return nil, fmt.Errorf("every service needs a key and a name")
		}
		for _, n := range s.Names() {
			if err := claim(n, s.Key); err != nil {
				return nil, err
			}
		}
		// A plugin whose platform API isn't public yet has no endpoint.
		if s.Endpoint != "" || s.Plugin == "" {
			u, err := url.Parse(s.Endpoint)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return nil, fmt.Errorf("service %s: endpoint must be an https URL, got %q", s.Key, s.Endpoint)
			}
		}
		if s.Package != "" && (s.Spec == nil || s.Spec.App == "" || s.Spec.File == "") {
			return nil, fmt.Errorf("service %s: a generated package needs a spec", s.Key)
		}
		if s.Package != "" && !namePattern.MatchString(s.Package) {
			return nil, fmt.Errorf("service %s: invalid package %q", s.Key, s.Package)
		}
		if s.Plugin != "" && (s.Command == "" || s.Plugin != "nrc-"+s.Command) {
			return nil, fmt.Errorf("service %s: plugin must be named nrc-<command>, got %q", s.Key, s.Plugin)
		}
	}
	return doc.Services, nil
}
