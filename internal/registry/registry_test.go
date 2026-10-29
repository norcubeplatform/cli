package registry

import (
	"strings"
	"testing"
)

func TestEmbeddedManifestIsValid(t *testing.T) {
	all := All()
	if len(all) == 0 {
		t.Fatal("no services")
	}
	for _, key := range []string{"auth", "web_app", "snapdb", "langsync", "domainradar", "billing", "prompthub"} {
		if _, ok := Get(key); !ok {
			t.Errorf("service %q missing: config files and NORCUBE_%s_URL depend on it", key, strings.ToUpper(key))
		}
	}
	for _, s := range all {
		if strings.Contains(s.Endpoint, "staging") {
			t.Errorf("service %s: only production endpoints belong in the manifest", s.Key)
		}
	}
}

func TestLookup(t *testing.T) {
	for name, want := range map[string]string{
		"backup": "snapdb", "snapdb": "snapdb", "BACKUP": "snapdb",
		"langsync": "langsync", "domainradar": "domainradar",
	} {
		s, ok := Lookup(name)
		if !ok || s.Key != want {
			t.Errorf("Lookup(%q) = %q, %v; want %q", name, s.Key, ok, want)
		}
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("unknown name resolved")
	}
}

func TestCallableHaveAudiences(t *testing.T) {
	for _, s := range Callable() {
		if s.Audience == "" || s.Command == "" {
			t.Errorf("%s is callable without an audience or command", s.Key)
		}
	}
	for _, s := range Callable() {
		if s.Key == "web_app" || s.Key == "auth" {
			t.Errorf("%s should not be callable through `nrc api`", s.Key)
		}
	}
}

func TestParseRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"empty":           `services: []`,
		"unknown field":   "services:\n  - key: a\n    name: A\n    endpoint: https://a.example\n    color: red\n",
		"http endpoint":   "services:\n  - key: a\n    name: A\n    endpoint: http://a.example\n",
		"duplicate name":  "services:\n  - key: a\n    name: A\n    endpoint: https://a.example\n  - key: b\n    name: B\n    aliases: [a]\n    endpoint: https://b.example\n",
		"package no spec": "services:\n  - key: a\n    name: A\n    endpoint: https://a.example\n    package: a\n",
		"bad key":         "services:\n  - key: A B\n    name: A\n    endpoint: https://a.example\n",
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
