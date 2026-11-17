package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

const keyPrefix = "en_live_"

// maskKey is a key as it may be shown: the 8 characters the console also shows, never the secret.
func maskKey(key string) string {
	rest := strings.TrimPrefix(key, keyPrefix)
	if len(rest) < 16 || rest == key {
		return keyPrefix + "…"
	}
	return keyPrefix + rest[:8] + "…"
}

func (a *App) cmdLogin() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "login",
		GroupID: "account",
		Short:   "Save an API key on this computer",
		Long: `Ask for an API key, check that the API accepts it, and save it for later commands.

Keys are created in the console, under API keys; the secret is shown once, when the key is created. The key is
saved in enrichednow/config.json in your configuration directory (` + envConfigDir + ` chooses another one), readable
by you alone. It is never printed.

On a terminal the key is asked for without showing it. In a script, pipe it in. A key in ` + envAPIKey + ` or
--api-key is used before the saved one, and needs no login.`,
		Example: `  enrichednow login
  printf '%s' "$KEY" | enrichednow login`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			key, err := a.askKey()
			if err != nil {
				return err
			}
			if !strings.HasPrefix(key, keyPrefix) {
				return &cliError{code: "usage", msg: "that does not look like an API key", exit: exitUsage,
					hint: "A key starts with " + keyPrefix + ". Create one in the console, under API keys."}
			}
			client, err := a.clientWith(key)
			if err != nil {
				return err
			}
			// A cheap call that needs a key: one run of the account's list.
			if _, err := client.Runs.List(cmd.Context(), &enrichednow.RunListParams{Limit: 1}); err != nil {
				if errors.Is(err, enrichednow.ErrAuthentication) {
					return &cliError{code: "unauthorized", msg: "the API did not accept that key; nothing was saved", exit: exitAuth,
						hint: "Check that you copied all of it. A revoked key cannot be used again: create a new one in the console, under API keys."}
				}
				return err
			}
			path, err := saveConfig(config{APIKey: key})
			if err != nil {
				return err
			}
			fmt.Fprintf(a.Out, "Logged in. Key %s saved to %s\n", maskKey(key), path)
			if os.Getenv(envAPIKey) != "" {
				a.note("%s is set in this shell and is used before the saved key.", envAPIKey)
			}
			return nil
		},
	}
	return cmd
}

// askKey gets the key to save: from --api-key, from the terminal without showing it, or from a pipe.
func (a *App) askKey() (string, error) {
	if a.apiKey != "" {
		return strings.TrimSpace(a.apiKey), nil
	}
	if a.InTTY && a.ReadSecret != nil {
		fmt.Fprint(a.Err, "API key (it is not shown): ")
		key, err := a.ReadSecret()
		if err != nil {
			return "", &cliError{code: "error", msg: "could not read the key: " + err.Error(), exit: exitError}
		}
		if key = strings.TrimSpace(key); key != "" {
			return key, nil
		}
	} else if key, err := a.readLine(); err == nil && key != "" {
		return key, nil
	} else if err != nil && err != io.EOF {
		return "", &cliError{code: "error", msg: "could not read the key: " + err.Error(), exit: exitError}
	}
	return "", &cliError{code: "usage", msg: "no key was given", exit: exitUsage,
		hint: "Run `enrichednow login` and paste the key, or pipe it in: printf '%s' \"$KEY\" | enrichednow login"}
}

func (a *App) cmdLogout() *cobra.Command {
	return &cobra.Command{
		Use:     "logout",
		GroupID: "account",
		Short:   "Forget the saved API key",
		Long: `Delete the key that "enrichednow login" saved on this computer.

The key itself keeps working: to stop it for good, revoke it in the console, under API keys.`,
		Example: `  enrichednow logout`,
		Args:    exactArgs(),
		RunE: func(*cobra.Command, []string) error {
			removed, err := removeConfig()
			if err != nil {
				return err
			}
			if removed {
				fmt.Fprintln(a.Out, "Logged out: the saved key was deleted from this computer.")
			} else {
				fmt.Fprintln(a.Out, "There was no saved key.")
			}
			if os.Getenv(envAPIKey) != "" {
				a.note("%s is still set in this shell.", envAPIKey)
			}
			return nil
		},
	}
}

func (a *App) cmdUsage() *cobra.Command {
	var from, to, groupBy, product string
	cmd := &cobra.Command{
		Use:     "usage",
		GroupID: "account",
		Short:   "Units used over a period",
		Long: `Show the units the account used.

Without --from and --to the period is the current calendar month (UTC) up to today. A run is charged when it
ends ok or partial; a data request is charged a data unit for each record it returns. Each of the two products
has its own monthly allowance: --product data shows the data allowance, otherwise the scraper allowance is shown.`,
		Example: `  enrichednow usage
  enrichednow usage --group-by scraper
  enrichednow usage --from 2026-10-01 --to 2026-10-31 --group-by product
  enrichednow usage --json | jq .total_units`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			usage, err := client.Usage.Get(cmd.Context(), &enrichednow.UsageParams{From: from, To: to, GroupBy: groupBy, Product: product})
			if err != nil {
				return err
			}
			doc := usage.RawJSON()
			return a.emit(result{doc: doc, rows: listOf(doc, "rows"), noun: "rows", human: func(w io.Writer, st *style, width int) {
				what := "units"
				if product == "data" {
					what = "data units"
				}
				fmt.Fprintf(w, "%s to %s: %s %s used; the monthly allowance is %d.\n\n", usage.Period.From, usage.Period.To,
					st.bold(strconv.Itoa(usage.TotalUnits)), what, usage.AllowanceUnits)
				head := map[string]string{"": "DAY", "day": "DAY", "scraper": "SCRAPER", "product": "PRODUCT"}[groupBy]
				t := &table{head: []string{head, "UNITS", "CHARGED"}, right: map[int]bool{1: true, 2: true}}
				for _, row := range usage.Rows {
					t.rows = append(t.rows, []string{row.Key, strconv.Itoa(row.Units), strconv.Itoa(row.Runs)})
				}
				t.render(w, st, width)
			}})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "first `day` (UTC, YYYY-MM-DD), included")
	cmd.Flags().StringVar(&to, "to", "", "last `day` (UTC, YYYY-MM-DD), included")
	cmd.Flags().StringVar(&groupBy, "group-by", "", "one row for each `key`: day (the default), scraper or product")
	cmd.Flags().StringVar(&product, "product", "", "only this `product`'s usage: scrapers or data")
	_ = cmd.RegisterFlagCompletionFunc("group-by", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"day", "scraper", "product"}, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("product", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"scrapers", "data"}, cobra.ShellCompDirectiveNoFileComp
	})
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdHealth() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "health",
		GroupID: "account",
		Short:   "Is the API up",
		Long:    "Ask the API whether it is up. No API key is needed.",
		Example: `  enrichednow health`,
		Args:    exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(false)
			if err != nil {
				return err
			}
			health, err := client.Health(cmd.Context())
			if err != nil {
				return err
			}
			return a.emit(result{doc: health.RawJSON(), noun: "rows", human: func(w io.Writer, st *style, _ int) {
				fmt.Fprintf(w, "%s is %s.\n", a.baseURL(), st.status(health.Status))
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}
