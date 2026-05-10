package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/norcubeplatform/cli/internal/auth"
	"github.com/norcubeplatform/cli/internal/clictx"
	"github.com/norcubeplatform/cli/internal/config"
)

func newLoginCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in to Norcube via your browser",
		Long: `Opens a Norcube login page in your default browser. After you sign in there,
the CLI receives a session over a one-shot localhost callback. Tokens are
stored in your operating system's keyring (Keychain / Secret Service /
Windows Credential Manager).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(cmd, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Re-authenticate even if a valid session already exists")
	return cmd
}

func runLogin(cmd *cobra.Command, force bool) error {
	flags := clictx.Get(cmd)
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	authURL := flags.ResolveAuth(cfg)
	webApp := flags.ResolveWebApp(cfg)

	if !force {
		if existing, _ := auth.LoadRefreshToken(authURL); existing != "" {
			who := "this account"
			if cfg.User.Email != "" {
				who = cfg.User.Email
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"Already signed in as %s. Use `norcube login --force` to re-authenticate or `norcube logout` to sign out first.\n",
				who,
			)
			return nil
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Opening %s in your browser…\n", webApp+"/cli-login")

	payload, err := auth.BrowserLogin(cmd.Context(), auth.BrowserLoginOptions{
		WebApp:  webApp,
		Timeout: 5 * time.Minute,
		OnURLReady: func(url string) {
			fmt.Fprintf(cmd.OutOrStdout(), "If your browser didn't open, visit:\n  %s\n", url)
		},
	})
	if err != nil {
		return err
	}

	if err := auth.SaveRefreshToken(authURL, payload.RefreshToken); err != nil {
		return fmt.Errorf("store refresh token: %w", err)
	}
	if err := auth.SaveAccessToken(authURL, auth.AudienceAuth, "", payload.AccessToken); err != nil {
		// non-fatal — we'll just re-mint via /oauth/token next call
	}

	cfg.Auth = authURL
	cfg.WebApp = webApp
	cfg.User = config.User{
		ID:    payload.User.ID,
		Name:  payload.User.Name,
		Email: payload.User.Email,
	}
	cfg.ActiveOrg = config.Organization{
		ID:   payload.DefaultOrg.ID,
		Name: payload.DefaultOrg.Name,
		Slug: payload.DefaultOrg.Slug,
	}
	if err := config.Save(cfg); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(),
		"Signed in as %s (%s).\nActive organization: %s.\n",
		payload.User.Name, payload.User.Email, payload.DefaultOrg.Slug,
	)
	return nil
}

