package cli

import (
	"github.com/norcubeplatform/cli/internal/config"
)

// Results of the platform commands as -o json|yaml prints them. Field
// names are camelCase and stable; resources the API returns keep the API's
// own shape.

// orgRef names an organization.
type orgRef struct {
	ID   string `json:"id" yaml:"id"`
	Slug string `json:"slug,omitempty" yaml:"slug,omitempty"`
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
}

// orgRefOf returns nil for no organization, so JSON says null.
func orgRefOf(o config.Organization) *orgRef {
	if o.ID == "" && o.Slug == "" {
		return nil
	}
	return &orgRef{ID: o.ID, Slug: o.Slug, Name: o.Name}
}

// userRef names a user.
type userRef struct {
	ID    string `json:"id" yaml:"id"`
	Name  string `json:"name,omitempty" yaml:"name,omitempty"`
	Email string `json:"email,omitempty" yaml:"email,omitempty"`
}
