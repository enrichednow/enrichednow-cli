package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

var runStatuses = []string{"queued", "running", "ok", "partial", "failed", "unsupported", "cancelled"}

func (a *App) cmdRuns() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "runs",
		GroupID: "core",
		Short:   "List, inspect, wait for and cancel runs, and read their records",
		Example: `  enrichednow runs list --status failed
  enrichednow runs get run_841742f37553189ff9c27ba1
  enrichednow runs records run_841742f37553189ff9c27ba1 --out jobs.csv`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.cmdRunsList(), a.cmdRunsGet(), a.cmdRunsWait(), a.cmdRunsCancel(), a.cmdRunsRecords())
	return cmd
}

func (a *App) cmdRunsList() *cobra.Command {
	var (
		scraper, status, cursor string
		limit                   int
		all                     bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Your runs, newest first",
		Long: `List the account's runs, newest first.

One page is printed: 20 runs unless --limit says otherwise. When there are more, the cursor of the next page is
shown (and is "next_cursor" in the JSON); pass it as --cursor, or use --all to read every page.`,
		Example: `  enrichednow runs list
  enrichednow runs list --scraper careers.company --status partial --limit 50
  enrichednow runs list --all --output csv --out runs.csv
  enrichednow runs list --json | jq -r '.runs[] | select(.status == "failed") | .id'`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			doc, rows, next, err := paged("runs", cursor, all, func(cursor string) (json.RawMessage, string, error) {
				page, err := client.Runs.List(cmd.Context(), &enrichednow.RunListParams{Limit: limit, Cursor: cursor, ScraperID: scraper, Status: enrichednow.RunStatus(status)})
				if err != nil {
					return nil, "", err
				}
				return page.RawJSON(), page.NextCursor, nil
			})
			if err != nil {
				return err
			}
			return a.emit(result{doc: doc, rows: rows, noun: "runs", human: func(w io.Writer, st *style, width int) {
				if len(rows) == 0 {
					a.note("No runs.")
					return
				}
				t := &table{head: []string{"ID", "SCRAPER", "STATUS", "REASON", "RECORDS", "UNITS", "CREATED (UTC)"},
					paint: map[int]func(string) string{2: st.status}, right: map[int]bool{4: true, 5: true}}
				for _, run := range decodeRows[enrichednow.Run](rows) {
					t.rows = append(t.rows, []string{run.ID, run.ScraperID, string(run.Status), run.Reason, strconv.Itoa(run.RecordCount), strconv.Itoa(run.Units), fmtTime(run.CreatedAt)})
				}
				t.render(w, st, width)
				a.more(next)
			}})
		},
	}
	cmd.Flags().StringVar(&scraper, "scraper", "", "only runs of this scraper `id`")
	cmd.Flags().StringVar(&status, "status", "", "only runs with this `status`: "+strings.Join(runStatuses, ", "))
	cmd.Flags().IntVar(&limit, "limit", 0, "runs per page: `n` from 1 to 100 (default 20)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "start at this `cursor`, the one a previous page gave")
	cmd.Flags().BoolVar(&all, "all", false, "read every page")
	_ = cmd.RegisterFlagCompletionFunc("status", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return runStatuses, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("scraper", func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return a.completeScrapers(cmd, nil, toComplete)
	})
	a.addOutputFlags(cmd)
	return cmd
}

// more tells the person at the terminal that a list goes on.
func (a *App) more(next string) {
	if next != "" {
		a.note("There are more: add --cursor %s for the next page, or --all for every page.", next)
	}
}

// paged reads one page of a list, or with all every page, keeping the JSON as the API wrote it. The doc it
// returns has the API's own shape: {"<key>": [...], "next_cursor": ...}.
func paged(key, cursor string, all bool, fetch func(cursor string) (json.RawMessage, string, error)) (doc json.RawMessage, rows []json.RawMessage, next string, err error) {
	rows = []json.RawMessage{}
	for {
		raw, nextCursor, err := fetch(cursor)
		if err != nil {
			return nil, nil, "", err
		}
		rows = append(rows, listOf(raw, key)...)
		if !all {
			return raw, rows, nextCursor, nil
		}
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}
	return mustJSON(map[string]any{key: rows, "next_cursor": nil}), rows, "", nil
}

func (a *App) cmdRunsGet() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get <run-id>",
		Aliases: []string{"show"},
		Short:   "One run: its status, its reason and what it did",
		Example: `  enrichednow runs get run_841742f37553189ff9c27ba1
  enrichednow runs get run_841742f37553189ff9c27ba1 --json | jq .status`,
		Args: exactArgs("run-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			run, err := client.Runs.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emitRun(run)
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) emitRun(run *enrichednow.Run) error {
	return a.emit(result{doc: run.RawJSON(), noun: "run", human: func(w io.Writer, st *style, _ int) { printRun(w, st, run) }})
}

func printRun(w io.Writer, st *style, run *enrichednow.Run) {
	dash := func(s string) string {
		if s == "" {
			return st.dim("-")
		}
		return s
	}
	when := func(t *time.Time) string {
		if t == nil {
			return st.dim("-")
		}
		return fmtTime(*t) + " UTC"
	}
	items := [][2]string{
		{"Run", run.ID},
		{"Scraper", fmt.Sprintf("%s (version %d)", run.ScraperID, run.ScraperVersion)},
		{"Status", st.status(string(run.Status))},
		{"Reason", dash(run.Reason)},
		{"Records", strconv.Itoa(run.RecordCount)},
		{"Units", strconv.Itoa(run.Units)},
		{"Created", fmtTime(run.CreatedAt) + " UTC"},
		{"Started", when(run.StartedAt)},
		{"Finished", when(run.FinishedAt)},
	}
	if d := runDuration(run); d > 0 {
		items = append(items, [2]string{"Took", fmtDuration(d)})
	}
	items = append(items, [2]string{"Input", cellText(run.Input)})
	if d := run.Diagnostics; d != nil {
		items = append(items, [2]string{"Requests", strconv.Itoa(d.Requests)})
		if d.Bytes != nil {
			items = append(items, [2]string{"Bytes", strconv.FormatInt(*d.Bytes, 10)})
		}
	}
	pairs(w, st, items)
	if d := run.Diagnostics; d != nil && len(d.Facts) > 0 {
		keys := make([]string, 0, len(d.Facts))
		for key := range d.Facts {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var facts [][2]string
		for _, key := range keys {
			facts = append(facts, [2]string{"  " + key, cellText(mustJSON(d.Facts[key]))})
		}
		fmt.Fprintf(w, "\n%s\n", st.bold("FACTS"))
		pairs(w, st, facts)
	}
}

func (a *App) cmdRunsWait() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait <run-id>",
		Short: "Wait until a run has ended",
		Long: `Wait until a run has ended, then print it.

The exit code is 0 when the run ended ok or partial, and 3 when it ended failed, unsupported or cancelled.`,
		Example: `  id=$(enrichednow run careers.company --input domain=acme.com --no-wait)
  enrichednow runs wait "$id" && enrichednow runs records "$id" --out jobs.csv`,
		Args: exactArgs("run-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			run, err := client.Runs.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if run, err = a.follow(cmd.Context(), client, run, timeout); err != nil {
				return err
			}
			if !run.Status.IsSuccess() {
				return runFailed(run)
			}
			return a.emitRun(run)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait; the run goes on when the time is up")
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdRunsCancel() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Ask a run to stop",
		Long: `Ask a run to stop, and print it as it is at that moment.

A queued run ends cancelled at once. A running one stops at its next safe point, so it usually still says
running: "enrichednow runs wait <run-id>" shows how it ended. A run that was already finishing may still
complete, and is then charged as usual. A cancelled run costs nothing; the records it had found are kept.`,
		Example: `  enrichednow runs cancel run_841742f37553189ff9c27ba1`,
		Args:    exactArgs("run-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			run, err := client.Runs.Cancel(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: run.RawJSON(), noun: "run", human: func(w io.Writer, st *style, _ int) {
				switch {
				case run.Status == enrichednow.RunCancelled:
					fmt.Fprintf(w, "Run %s is %s.\n", run.ID, st.status("cancelled"))
				case run.Status.IsFinal():
					fmt.Fprintf(w, "Run %s had already ended %s; nothing changed.\n", run.ID, st.status(string(run.Status)))
				default:
					fmt.Fprintf(w, "Run %s was asked to stop; it is still %s.\n", run.ID, st.status(string(run.Status)))
					a.note("See how it ends with `enrichednow runs wait %s`.", run.ID)
				}
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdRunsRecords() *cobra.Command {
	var limit int
	var written string
	cmd := &cobra.Command{
		Use:   "records <run-id>",
		Short: "The records a run found",
		Long: `Print the records of a run, in the order the run stored them.

Records exist for runs that ended ok or partial, and for a cancelled run that had found some. They are kept for
7 days after the run ended.

The table shows as many fields as fit; json, jsonl and csv have all of them. In CSV a nested field becomes
parent.child columns.

--format csv or ndjson prints them as the API writes them instead, a page of 1000 at a time: a CSV has a column
for each field of the scraper's record schema, in its order, so every run of a scraper has the same columns.`,
		Example: `  enrichednow runs records run_841742f37553189ff9c27ba1
  enrichednow runs records run_841742f37553189ff9c27ba1 --output jsonl | jq -r .title
  enrichednow runs records run_841742f37553189ff9c27ba1 --out jobs.csv
  enrichednow runs records run_841742f37553189ff9c27ba1 --format csv --out jobs.csv`,
		Args: exactArgs("run-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := a.outputFormat(); err != nil {
				return err
			}
			if err := a.checkWritten(written); err != nil {
				return err
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			if written != "" {
				return a.writeExport(cmd.Context(), client, args[0], written)
			}
			return a.printRecords(cmd.Context(), client, args[0], limit)
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "print at most this many records (0: all of them)")
	a.addFormatFlag(cmd, &written)
	a.addOutputFlags(cmd)
	return cmd
}
