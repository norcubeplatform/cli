package output

import (
	"fmt"

	"github.com/jmespath-community/go-jmespath"
)

// QueryError is an invalid --query expression.
type QueryError struct{ Err error }

func (e *QueryError) Error() string { return fmt.Sprintf("--query: %v", e.Err) }
func (e *QueryError) Unwrap() error { return e.Err }

// CompileQuery checks a JMESPath expression, so a typo fails before any
// request is made.
func CompileQuery(expr string) error {
	if _, err := jmespath.Compile(expr); err != nil {
		return &QueryError{Err: err}
	}
	return nil
}

// Query applies a JMESPath expression to what -o json would print for
// value and returns the result, ready for any format.
func Query(value any, expr string) (Generic, error) {
	q, err := jmespath.Compile(expr)
	if err != nil {
		return Generic{}, &QueryError{Err: err}
	}
	data, err := toGeneric(payload(value))
	if err != nil {
		return Generic{}, err
	}
	out, err := q.Search(data)
	if err != nil {
		return Generic{}, &QueryError{Err: err}
	}
	return Generic{V: out}, nil
}
