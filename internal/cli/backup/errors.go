package backup

import (
	"net/http"

	"github.com/norcubeplatform/cli/internal/api/apierror"
	"github.com/norcubeplatform/cli/internal/api/snapdb"
)

// apiError renders a non-2xx snapdb response via the shared formatter
// (see internal/api/apierror). Typed bodies win over the raw body.
func apiError(resp *http.Response, body []byte, typed ...*snapdb.ResponseAPIError) error {
	conv := make([]*apierror.Typed, 0, len(typed))
	for _, t := range typed {
		conv = append(conv, typedOf(t))
	}
	return apierror.Format("backup", resp, body, conv...)
}

func typedOf(e *snapdb.ResponseAPIError) *apierror.Typed {
	if e == nil {
		return nil
	}
	return &apierror.Typed{Msg: e.Msg, Type: string(e.Type)}
}
