package domainradar

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"github.com/norcubeplatform/cli/internal/api/domainradar"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
)

// maxCheckDomains caps `check`: more domains belong in a bulk check, which
// runs on the server and survives the terminal closing.
const maxCheckDomains = 100

// checkConcurrency is how many single checks run at once. Enough to make
// a short list quick without hammering the API.
const checkConcurrency = 4

// checkFlags are the flags every command that runs checks shares.
type checkFlags struct {
	mode   string
	maxAge time.Duration
}

func addCheckFlags(cmd *cobra.Command, f *checkFlags) {
	cmd.Flags().StringVar(&f.mode, "mode", string(domainradar.CheckModeVerified), modeFlagHelp)
	cmd.Flags().DurationVar(&f.maxAge, "max-age", 0, "Accept a cached answer up to this old (e.g. 1h, 24h; cached answers are free). 0 uses the service default")
	_ = cmd.RegisterFlagCompletionFunc("mode", cobra.FixedCompletions(checkModes, cobra.ShellCompDirectiveNoFileComp))
}

// validate checks the flags before any request and returns them in the
// API's types.
func (f checkFlags) validate() (*domainradar.ConstantsCheckMode, *string, error) {
	if !slices.Contains(checkModes, f.mode) {
		return nil, nil, clierr.Usage(fmt.Errorf("--mode must be one of %s (got %q)", strings.Join(checkModes, ", "), f.mode))
	}
	if f.maxAge < 0 {
		return nil, nil, clierr.Usage(fmt.Errorf("--max-age must not be negative"))
	}
	mode := domainradar.ConstantsCheckMode(f.mode)
	var maxAge *string
	if f.maxAge > 0 {
		s := f.maxAge.String()
		maxAge = &s
	}
	return &mode, maxAge, nil
}

func newCheckCmd(d deps) *cobra.Command {
	var (
		flags checkFlags
		file  string
	)
	cmd := &cobra.Command{
		Use:   "check <domain>...",
		Short: "Check whether domains are available to register",
		Long: `Checks each domain live and prints whether it is available, with the
method that answered and, for registered domains, the expiry date and
registrar. Domains come from the arguments, a file (--file) or standard
input (--file - or a single -), one per line.

Up to 100 domains at a time; for more, use "domainradar bulk create", which
runs on the server.

` + metered,
		Example: `  norcube domainradar check example.com
  norcube domainradar check acme.io acme.dev acme.app --mode bloom
  norcube domainradar check --file names.txt --mode info -o json
  cat names.txt | norcube domainradar check - -o text --query "[?available].domain"`,
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			mode, maxAge, err := flags.validate()
			if err != nil {
				return nil, err
			}
			domains, err := cmdutil.ReadItems(cmdutil.ItemSource{
				Args: args, File: file, Stdin: cmd.InOrStdin(), Max: maxCheckDomains, What: "domains",
			})
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			results, err := checkAll(cmd.Context(), c.API, domains, mode, maxAge)
			if err != nil {
				return nil, err
			}
			return checkTable(results), nil
		}),
	}
	addCheckFlags(cmd, &flags)
	cmd.Flags().StringVarP(&file, "file", "f", "", "Read domains from this file, one per line (- for standard input)")
	return cmd
}

// checkAll checks domains with bounded concurrency and returns the results
// in the order given. The first failed request cancels the rest: a
// missing permission or an exhausted budget fails the same way for every
// domain, so there's no point sending them.
func checkAll(ctx context.Context, api domainradar.ClientWithResponsesInterface, domains []string, mode *domainradar.ConstantsCheckMode, maxAge *string) ([]domainradar.DtoDomainCheck, error) {
	results := make([]domainradar.DtoDomainCheck, len(domains))
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(checkConcurrency)
	for i, domain := range domains {
		g.Go(func() error {
			res, err := api.CheckDomainWithResponse(ctx, domainradar.CheckDomainJSONRequestBody{
				Domain: &domain, Mode: mode, MaxAge: maxAge,
			})
			if err != nil {
				return err
			}
			if res.JSON200 == nil {
				return apiError(res.HTTPResponse, res.Body)
			}
			results[i] = *res.JSON200
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}
