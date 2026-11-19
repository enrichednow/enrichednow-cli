package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

func (a *App) cmdWebhooks() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "webhooks",
		GroupID: "core",
		Short:   "Endpoints told when a run ends, their signing secrets, and what was sent",
		Long: `A webhook endpoint is a URL of yours that is sent a signed POST when a run ends with one of the events it
asked for: run.succeeded, run.partial, run.failed, run.unsupported, run.cancelled.

The signing secret of an endpoint is shown once, when it is added or rotated: keep it, it is what you check a
request with. "enrichednow webhooks verify" checks a request the way your code should.

Documentation: ` + docsURL + `/webhooks/`,
		Example: `  enrichednow webhooks add https://example.com/hooks/enrichednow --events run.succeeded,run.failed
  enrichednow webhooks list
  enrichednow webhooks deliveries --status failed
  enrichednow webhooks resend whd_1a2b3c`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.cmdWebhooksList(), a.cmdWebhooksAdd(), a.cmdWebhooksShow(), a.cmdWebhooksUpdate(), a.cmdWebhooksRemove(),
		a.cmdWebhooksRotate(), a.cmdWebhooksRunSecret(), a.cmdWebhooksDeliveries(), a.cmdWebhooksResend(), a.cmdWebhooksVerify())
	return cmd
}

func endpointPairs(e *enrichednow.WebhookEndpoint, st *style) [][2]string {
	state := st.green("enabled")
	if !e.Enabled {
		state = st.red("disabled")
		if e.DisabledReason != nil {
			state += " (" + *e.DisabledReason + ")"
		}
	}
	return [][2]string{{"ID", e.ID}, {"URL", e.URL}, {"Events", strings.Join(e.Events, ", ")}, {"Description", e.Description},
		{"State", state}, {"Last success", fmtTimePtr(e.LastSuccessAt)}, {"Last failure", fmtTimePtr(e.LastFailureAt)},
		{"Created (UTC)", fmtTime(e.CreatedAt)}}
}

func (a *App) showSecret(w io.Writer, st *style, secret string) {
	fmt.Fprintf(w, "\n%s %s\n", st.bold("Signing secret:"), secret)
	a.note("It is shown this once: keep it where your code reads it. It checks each request (`enrichednow webhooks verify`).")
}

func splitEvents(value string) []string {
	var events []string
	for _, e := range strings.Split(value, ",") {
		if e = strings.TrimSpace(e); e != "" {
			events = append(events, e)
		}
	}
	return events
}

func (a *App) cmdWebhooksList() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Example: `  enrichednow webhooks list --json`,
		Aliases: []string{"ls"},
		Short:   "Your webhook endpoints",
		Args:    exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			list, err := client.Webhooks.List(cmd.Context())
			if err != nil {
				return err
			}
			return a.emit(result{doc: list.RawJSON(), rows: listOf(list.RawJSON(), "endpoints"), noun: "endpoints", human: func(w io.Writer, st *style, width int) {
				if len(list.Endpoints) == 0 {
					a.note("No endpoints. Add one with `enrichednow webhooks add <url>`.")
				} else {
					t := &table{head: []string{"ID", "URL", "EVENTS", "STATE", "LAST SUCCESS (UTC)"}}
					for _, e := range list.Endpoints {
						state := "enabled"
						if !e.Enabled {
							state = "disabled"
						}
						t.rows = append(t.rows, []string{e.ID, e.URL, strings.Join(e.Events, ","), state, fmtTimePtr(e.LastSuccessAt)})
					}
					t.render(w, st, width)
				}
				if list.RunSecretSet {
					a.note("The account has a run secret, for `enrichednow run --webhook-url`.")
				}
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdWebhooksAdd() *cobra.Command {
	var events, description string
	cmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Add an endpoint, and show its signing secret once",
		Long: `Add an https URL that is sent a signed POST when a run ends with one of --events (default: all of them).

The answer holds the endpoint's signing secret, shown this once.`,
		Example: `  enrichednow webhooks add https://example.com/hooks/enrichednow
  enrichednow webhooks add https://example.com/hooks/enrichednow --events run.succeeded,run.partial --description production`,
		Args: exactArgs("url"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			list := enrichednow.WebhookEvents
			if events != "" {
				list = splitEvents(events)
			}
			made, err := client.Webhooks.Create(cmd.Context(), enrichednow.WebhookEndpointParams{URL: args[0], Events: list, Description: description})
			if err != nil {
				return err
			}
			return a.emit(result{doc: made.RawJSON(), noun: "endpoint", human: func(w io.Writer, st *style, _ int) {
				pairs(w, st, endpointPairs(&made.WebhookEndpoint, st))
				a.showSecret(w, st, made.Secret)
			}})
		},
	}
	cmd.Flags().StringVar(&events, "events", "", "comma-separated `events`: "+strings.Join(enrichednow.WebhookEvents, ", ")+" (default all)")
	cmd.Flags().StringVar(&description, "description", "", "a `note` of yours on it")
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdWebhooksShow() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "show <endpoint-id>",
		Example: `  enrichednow webhooks show whk_1a2b3c`,
		Aliases: []string{"get"},
		Short:   "One endpoint",
		Args:    exactArgs("endpoint-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			e, err := client.Webhooks.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: e.RawJSON(), noun: "endpoint", human: func(w io.Writer, st *style, _ int) {
				pairs(w, st, endpointPairs(e, st))
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdWebhooksUpdate() *cobra.Command {
	var url, events, description string
	var enable, disable bool
	cmd := &cobra.Command{
		Use:   "update <endpoint-id>",
		Short: "Change an endpoint's URL, events or description, or switch it on or off",
		Example: `  enrichednow webhooks update whk_1a2b3c --events run.failed
  enrichednow webhooks update whk_1a2b3c --enable`,
		Args: exactArgs("endpoint-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			change := enrichednow.WebhookEndpointChange{}
			if cmd.Flags().Changed("url") {
				change.URL = &url
			}
			if cmd.Flags().Changed("events") {
				change.Events = splitEvents(events)
			}
			if cmd.Flags().Changed("description") {
				change.Description = &description
			}
			if enable && disable {
				return &cliError{code: "usage", msg: "--enable and --disable ask for two different things", exit: exitUsage}
			}
			if enable || disable {
				change.Enabled = &enable
			}
			if change.URL == nil && change.Events == nil && change.Description == nil && change.Enabled == nil {
				return &cliError{code: "usage", msg: "nothing to change", hint: "Give --url, --events, --description, --enable or --disable.", exit: exitUsage}
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			e, err := client.Webhooks.Update(cmd.Context(), args[0], change)
			if err != nil {
				return err
			}
			return a.emit(result{doc: e.RawJSON(), noun: "endpoint", human: func(w io.Writer, st *style, _ int) {
				pairs(w, st, endpointPairs(e, st))
			}})
		},
	}
	cmd.Flags().StringVar(&url, "url", "", "the new https `URL`")
	cmd.Flags().StringVar(&events, "events", "", "comma-separated `events` it is told about")
	cmd.Flags().StringVar(&description, "description", "", "a `note` of yours on it")
	cmd.Flags().BoolVar(&enable, "enable", false, "switch it back on")
	cmd.Flags().BoolVar(&disable, "disable", false, "switch it off")
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdWebhooksRemove() *cobra.Command {
	return &cobra.Command{
		Use:     "remove <endpoint-id>",
		Example: `  enrichednow webhooks remove whk_1a2b3c`,
		Aliases: []string{"rm", "delete"},
		Short:   "Remove an endpoint; its deliveries stay in the log",
		Args:    exactArgs("endpoint-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			if err := client.Webhooks.Delete(cmd.Context(), args[0]); err != nil {
				return err
			}
			a.note("Removed %s.", args[0])
			return nil
		},
	}
}

func (a *App) cmdWebhooksRotate() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "rotate-secret <endpoint-id>",
		Example: `  enrichednow webhooks rotate-secret whk_1a2b3c`,
		Short:   "Give an endpoint a new signing secret, shown once; the old one stops at once",
		Args:    exactArgs("endpoint-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			made, err := client.Webhooks.RotateSecret(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: made.RawJSON(), noun: "endpoint", human: func(w io.Writer, st *style, _ int) {
				pairs(w, st, endpointPairs(&made.WebhookEndpoint, st))
				a.showSecret(w, st, made.Secret)
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdWebhooksRunSecret() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "run-secret",
		Example: `  enrichednow webhooks run-secret`,
		Short:   "Make the account's secret for one run's --webhook-url, shown once (replaces the old one)",
		Args:    exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			secret, err := client.Webhooks.CreateRunSecret(cmd.Context())
			if err != nil {
				return err
			}
			return a.emit(result{doc: secret.RawJSON(), noun: "secret", human: func(w io.Writer, st *style, _ int) {
				a.showSecret(w, st, secret.Secret)
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdWebhooksDeliveries() *cobra.Command {
	var (
		endpoint, runID, status, cursor string
		limit                           int
		all                             bool
	)
	cmd := &cobra.Command{
		Use:   "deliveries",
		Short: "What was sent, newest first, with every attempt",
		Example: `  enrichednow webhooks deliveries --status failed
  enrichednow webhooks deliveries --run run_841742f37553189ff9c27ba1 --json`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			doc, rows, next, err := paged("deliveries", cursor, all, func(cursor string) (json.RawMessage, string, error) {
				page, err := client.Webhooks.Deliveries.List(cmd.Context(), &enrichednow.WebhookDeliveryListParams{
					Limit: limit, Cursor: cursor, EndpointID: endpoint, RunID: runID, Status: status})
				if err != nil {
					return nil, "", err
				}
				return page.RawJSON(), page.NextCursor, nil
			})
			if err != nil {
				return err
			}
			return a.emit(result{doc: doc, rows: rows, noun: "deliveries", human: func(w io.Writer, st *style, width int) {
				if len(rows) == 0 {
					a.note("No deliveries.")
					return
				}
				t := &table{head: []string{"ID", "EVENT", "RUN", "STATUS", "ATTEMPTS", "ANSWER", "CREATED (UTC)"},
					right: map[int]bool{4: true, 5: true}}
				for _, d := range decodeRows[enrichednow.WebhookDelivery](rows) {
					answer := ""
					if d.ResponseStatus != nil {
						answer = strconv.Itoa(*d.ResponseStatus)
					}
					t.rows = append(t.rows, []string{d.ID, d.Event, d.RunID, d.Status, fmt.Sprintf("%d/%d", d.Attempts, d.MaxAttempts), answer, fmtTime(d.CreatedAt)})
				}
				t.render(w, st, width)
				a.more(next)
			}})
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "only deliveries to this `endpoint-id`")
	cmd.Flags().StringVar(&runID, "run", "", "only deliveries about this `run-id`")
	cmd.Flags().StringVar(&status, "status", "", "only `pending`, succeeded or failed ones")
	cmd.Flags().StringVar(&cursor, "cursor", "", "start after this `cursor` (the next_cursor of an earlier page)")
	cmd.Flags().IntVar(&limit, "limit", 0, "deliveries a page, 1 to 100")
	cmd.Flags().BoolVar(&all, "all", false, "read every page")
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdWebhooksResend() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resend <delivery-id>",
		Example: `  enrichednow webhooks resend whd_1a2b3c`,
		Short:   "Send a delivery's event again, as a new delivery with the same event id",
		Args:    exactArgs("delivery-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			d, err := client.Webhooks.Deliveries.Resend(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: d.RawJSON(), noun: "delivery", human: func(w io.Writer, st *style, _ int) {
				fmt.Fprintf(w, "Sending %s again as %s (%s).\n", d.Event, d.ID, d.Status)
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdWebhooksVerify() *cobra.Command {
	var secret, timestamp, signature, file string
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Check a webhook request's signature, as your code should",
		Long: `Check that a request body was signed with the secret at the timestamp, and that the timestamp is within
5 minutes of now. The body is read from --file, or from standard input, exactly as it arrived.

The secret is read from --secret or ENRICHEDNOW_WEBHOOK_SECRET. Exit code 0: it verifies; 1: it does not.`,
		Example: `  enrichednow webhooks verify --timestamp 1763600000 --signature v1=5f2a… --file body.json`,
		Args:    exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if secret == "" {
				secret = os.Getenv("ENRICHEDNOW_WEBHOOK_SECRET")
			}
			if secret == "" || timestamp == "" || signature == "" {
				return &cliError{code: "usage", msg: "verify needs the secret, --timestamp and --signature",
					hint: "Give the secret with --secret or ENRICHEDNOW_WEBHOOK_SECRET.", exit: exitUsage}
			}
			var body []byte
			var err error
			if file == "" || file == "-" {
				body, err = io.ReadAll(a.In)
			} else {
				body, err = os.ReadFile(file)
			}
			if err != nil {
				return &cliError{code: "usage", msg: fmt.Sprintf("cannot read the body: %v", err), exit: exitUsage}
			}
			if err := enrichednow.VerifyWebhookSignature(secret, timestamp, body, signature, 0); err != nil {
				return &cliError{code: "not_verified", msg: "the request does not verify",
					hint: "Check the secret, and that the body is the exact bytes that arrived; a timestamp over 5 minutes old is refused.", exit: exitError}
			}
			fmt.Fprintln(a.Out, "Verified.")
			return nil
		},
	}
	cmd.Flags().StringVar(&secret, "secret", "", "the endpoint's signing `secret` (whsec_…); prefer ENRICHEDNOW_WEBHOOK_SECRET")
	cmd.Flags().StringVar(&timestamp, "timestamp", "", "the X-Webhook-Timestamp `header`")
	cmd.Flags().StringVar(&signature, "signature", "", "the X-Webhook-Signature `header`")
	cmd.Flags().StringVarP(&file, "file", "f", "", "read the body from this `file` (default standard input)")
	return cmd
}
