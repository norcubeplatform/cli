package backup

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/api/snapdb"
	"github.com/norcubeplatform/cli/internal/clierr"
	"github.com/norcubeplatform/cli/internal/cmdutil"
	"github.com/norcubeplatform/cli/internal/output"
)

// Data source settings the API accepts, as it names them.
var (
	datasourceEngines = []string{string(snapdb.EnginePostgres), string(snapdb.EngineMongoDB)}
	datasourceEnvs    = []string{
		string(snapdb.EnvironmentProd), string(snapdb.EnvironmentStaging),
		string(snapdb.EnvironmentDev), string(snapdb.EnvironmentTest),
	}
	datasourceRegions = []string{string(snapdb.RegionEUCentral1)}
)

func oneOf(flag, v string, allowed []string) error {
	if !slices.Contains(allowed, v) {
		return clierr.Usage(fmt.Errorf("--%s must be one of %s", flag, strings.Join(allowed, ", ")))
	}
	return nil
}

// readConnectionURI reads a connection string from --connection-uri-file;
// "" when the flag isn't set.
func readConnectionURI(cmd *cobra.Command, file string) (string, error) {
	if file == "" {
		return "", nil
	}
	uri, err := cmdutil.ReadText(cmd.InOrStdin(), "", file, "the connection string")
	if err != nil {
		return "", err
	}
	if uri = strings.TrimSpace(uri); uri == "" {
		return "", clierr.Usage(fmt.Errorf("%s holds no connection string", file))
	}
	return uri, nil
}

// setCredentials stores a data source's connection string.
func setCredentials(ctx context.Context, c *snapdbContext, id, uri string) error {
	res, err := c.API.PutDatasourcesIdCredentialsWithResponse(ctx, id, snapdb.PutDatasourcesIdCredentialsJSONRequestBody{
		Mode: snapdb.CredentialModeURI, Uri: &uri,
	})
	if err != nil {
		return err
	}
	if res.JSON200 == nil {
		return apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON500)
	}
	return nil
}

const connectionURIHelp = "Read the database connection string from this file (- for standard input)"

func newDataSourceCreateCmd(d deps) *cobra.Command {
	var name, engine, env, region, uriFile string
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Add a database to back up",
		Long: `Adds a data source: a database Norcube Backup backs up. Give its
connection string with --connection-uri-file (a file, - for standard
input), never on the command line, so it stays out of shell history; it
is stored encrypted and never shown again. A role that can read every
table is enough for backups.

Nothing is backed up until a policy is attached
(` + "`norcube backup policies attach`" + `).`,
		Example: `  norcube backup datasources create --name prod-db --engine postgres --environment prod --connection-uri-file db.uri
  printenv DATABASE_URL | norcube backup datasources create --name prod-db --engine postgres --environment prod --connection-uri-file -`,
		Args: cobra.NoArgs,
		RunE: cmdutil.Run(func(cmd *cobra.Command, _ []string) (any, error) {
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, clierr.Usage(errors.New("--name must not be empty"))
			}
			for _, chk := range []error{oneOf("engine", engine, datasourceEngines), oneOf("environment", env, datasourceEnvs), oneOf("region", region, datasourceRegions)} {
				if chk != nil {
					return nil, chk
				}
			}
			uri, err := readConnectionURI(cmd, uriFile)
			if err != nil {
				return nil, err
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			res, err := c.API.PostDatasourcesWithResponse(cmd.Context(), snapdb.PostDatasourcesJSONRequestBody{
				Name: name, Engine: snapdb.ConstantsDatasourceEngine(engine),
				Environment: snapdb.ConstantsDatasourceEnvironment(env), Region: snapdb.ConstantsDatasourceRegion(region),
			})
			if err != nil {
				return nil, err
			}
			if res.JSON201 == nil {
				return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON403, res.JSON500)
			}
			id := res.JSON201.Id
			if uri != "" {
				if err := setCredentials(cmd.Context(), c, id, uri); err != nil {
					return nil, fmt.Errorf("created data source %s, but storing its connection string failed (retry with `norcube backup datasources update %s --connection-uri-file …`): %w", id, id, err)
				}
			}
			msg := fmt.Sprintf("Created data source %q (%s).", name, id)
			if uri == "" {
				msg += "\nGive it a connection string with `norcube backup datasources update " + id + " --connection-uri-file <file>`."
			}
			return output.Done{
				Message: msg,
				Value:   map[string]any{"id": id, "name": name, "engine": engine, "environment": env, "region": region, "hasCredentials": uri != ""},
				Fields:  []output.Field{{Value: id}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&name, "name", "", "Name shown in lists")
	cmd.Flags().StringVar(&engine, "engine", "", "Database engine: "+strings.Join(datasourceEngines, ", "))
	cmd.Flags().StringVar(&env, "environment", "", "Environment: "+strings.Join(datasourceEnvs, ", "))
	cmd.Flags().StringVar(&region, "region", string(snapdb.RegionEUCentral1), "Region the backups are processed in: "+strings.Join(datasourceRegions, ", "))
	cmd.Flags().StringVar(&uriFile, "connection-uri-file", "", connectionURIHelp)
	_ = cmd.RegisterFlagCompletionFunc("engine", cobra.FixedCompletions(datasourceEngines, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("environment", cobra.FixedCompletions(datasourceEnvs, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("region", cobra.FixedCompletions(datasourceRegions, cobra.ShellCompDirectiveNoFileComp))
	for _, f := range []string{"name", "engine", "environment"} {
		_ = cmd.MarkFlagRequired(f)
	}
	return cmd
}

func newDataSourceUpdateCmd(d deps) *cobra.Command {
	var name, env, region, uriFile string
	cmd := &cobra.Command{
		Use:   "update <id>",
		Short: "Rename a data source, change its environment or connection string",
		Long: `Changes only what you pass. --connection-uri-file replaces the stored
connection string (a file, - for standard input). Pausing and resuming
backups is ` + "`datasources pause|resume`" + `.`,
		Example: `  norcube backup datasources update 9f8e7d6c-… --name prod-db-eu
  norcube backup datasources update 9f8e7d6c-… --connection-uri-file new.uri`,
		Args: cobra.ExactArgs(1),
		RunE: cmdutil.Run(func(cmd *cobra.Command, args []string) (any, error) {
			id := strings.TrimSpace(args[0])
			changed := cmd.Flags().Changed
			var body snapdb.UpdateDataSourceJSONRequestBody
			if changed("name") {
				v := strings.TrimSpace(name)
				if v == "" {
					return nil, clierr.Usage(errors.New("--name must not be empty"))
				}
				body.Name = &v
			}
			if changed("environment") {
				if err := oneOf("environment", env, datasourceEnvs); err != nil {
					return nil, err
				}
				body.Environment = &env
			}
			if changed("region") {
				if err := oneOf("region", region, datasourceRegions); err != nil {
					return nil, err
				}
				body.Region = &region
			}
			uri, err := readConnectionURI(cmd, uriFile)
			if err != nil {
				return nil, err
			}
			metadata := body != (snapdb.UpdateDataSourceJSONRequestBody{})
			if !metadata && uri == "" {
				return nil, clierr.Usage(errors.New("nothing to update: pass --name, --environment, --region or --connection-uri-file"))
			}
			c, err := d.client(cmd)
			if err != nil {
				return nil, err
			}
			if metadata {
				res, err := c.API.UpdateDataSourceWithResponse(cmd.Context(), id, body)
				if err != nil {
					return nil, err
				}
				if res.StatusCode() != http.StatusOK && res.StatusCode() != http.StatusNoContent {
					return nil, apiError(res.HTTPResponse, res.Body, res.JSON400, res.JSON404, res.JSON500)
				}
			}
			if uri != "" {
				if err := setCredentials(cmd.Context(), c, id, uri); err != nil {
					return nil, err
				}
			}
			return output.Done{
				Message: fmt.Sprintf("Updated data source %s.", id),
				Value:   map[string]any{"id": id, "updated": true, "credentialsUpdated": uri != ""},
				Fields:  []output.Field{{Value: id}},
			}, nil
		}),
	}
	cmd.Flags().StringVar(&name, "name", "", "New name")
	cmd.Flags().StringVar(&env, "environment", "", "New environment: "+strings.Join(datasourceEnvs, ", "))
	cmd.Flags().StringVar(&region, "region", "", "New region: "+strings.Join(datasourceRegions, ", "))
	cmd.Flags().StringVar(&uriFile, "connection-uri-file", "", connectionURIHelp)
	_ = cmd.RegisterFlagCompletionFunc("environment", cobra.FixedCompletions(datasourceEnvs, cobra.ShellCompDirectiveNoFileComp))
	return cmd
}
