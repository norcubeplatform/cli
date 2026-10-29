package clierr

import (
	"errors"
	"fmt"
	"testing"
)

func TestIsUsage(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{Usage(errors.New("bad")), true},
		{fmt.Errorf("context: %w", Usage(errors.New("bad"))), true},
		{errors.New(`unknown command "foo" for "norcube"`), true},
		{errors.New(`required flag(s) "datasource" not set`), true},
		{errors.New(`if any flags in the group [a b] are set none of the others can be; [a b] were all set`), true},
		{errors.New("unknown flag: --nope"), true},
		{errors.New("GET /organizations: status 500"), false},
	}
	for _, tt := range tests {
		if got := IsUsage(tt.err); got != tt.want {
			t.Errorf("IsUsage(%q) = %v, want %v", tt.err, got, tt.want)
		}
	}
	if Usage(nil) != nil {
		t.Error("Usage(nil) must stay nil")
	}
}
