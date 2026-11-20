package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

// firstWait is how long the request that starts a run is held open. A quick run comes back finished in that one
// request; a longer one gives its id soon enough to be shown, and is then polled.
const firstWait = 5 * time.Second

func (a *App) cmdRun() *cobra.Command {
	var (
		inputs    []string
		inputJSON string
		file      string
		noWait    bool
		timeout   time.Duration
		limit     int
		key       string
		hook      string
		written   string
	)
	cmd := &cobra.Command{
		Use:     "run <scraper-id>",
		GroupID: "core",
		Short:   "Run a scraper, wait for it and print its records",
		Long: `Start a run of a scraper, show its progress, wait until it has ended and print the records it found.

The input is put together from, in this order (later ones win, field by field):
  --file         a JSON object in a file; "-" reads it from standard input
  --input-json   a JSON object on the command line
  --input        one field as key=value, as often as needed

A value given with --input is turned into what the scraper's input schema asks for: a number for a number
field, true/false for a boolean, a,b,c for a list — and a string field stays a string. See the fields with
"enrichednow scrapers show <scraper-id>".

Progress and the summary go to standard error and the records to standard output, so the output can be piped.
The exit code is 0 when the run ended ok or partial, and 3 when it ended failed, unsupported or cancelled.

The request that starts a run carries an idempotency key, so it is sent again after a connection error or a
server error without ever starting a second run. Give your own with --idempotency-key to make the command safe to
repeat: for 24 hours the same key and the same input give back the run the first command started. When the start
fails all the same, the error says the key to try again with.

--format csv or ndjson prints the records as the API writes them, a page of 1000 at a time: a CSV has a column
for each field of the scraper's record schema, in its order. --output formats the records here instead.

Interrupting the wait (Ctrl-C) does not stop the run; "enrichednow runs cancel <id>" does.`,
		Example: `  # Job openings from an applicant-tracking-system board, as a table
  enrichednow run ats.board --input board_url=https://boards.greenhouse.io/acme

  # Several fields; save the records as CSV
  enrichednow run careers.company --input domain=acme.com --input max_detail_pages=10 --out jobs.csv

  # The input as JSON, on the command line or from a file or a pipe
  enrichednow run careers.company --input-json '{"domain": "acme.com"}'
  enrichednow run careers.company --file input.json
  echo '{"domain": "acme.com"}' | enrichednow run careers.company --file -

  # In a script: records as JSON lines, nothing else
  enrichednow run ats.board --input board_url=https://boards.greenhouse.io/acme -o jsonl --quiet

  # Start it and come back later
  id=$(enrichednow run careers.company --input domain=acme.com --no-wait)
  enrichednow runs wait "$id" && enrichednow runs records "$id" -o jsonl

  # Safe to repeat from a scheduled job: the same key never starts a second run
  enrichednow run careers.company --input domain=acme.com --idempotency-key acme-2026-11-20 --format csv > jobs.csv

  # Be told when it ends
  enrichednow run careers.company --input domain=acme.com --no-wait --webhook-url https://example.com/hooks/enrichednow`,
		Args:              exactArgs("scraper-id"),
		ValidArgsFunction: a.completeScrapers,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, scraperID := cmd.Context(), args[0]
			if _, err := a.outputFormat(); err != nil {
				return err
			}
			if err := a.checkWritten(written); err != nil {
				return err
			}
			if key != "" && !validKey(key) {
				return &cliError{code: "usage", msg: "--idempotency-key must be 1 to 255 visible characters, without spaces", exit: exitUsage}
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			input, err := a.buildInput(ctx, client, scraperID, file, inputJSON, inputs)
			if err != nil {
				return err
			}
			progress := a.newProgress("Starting " + scraperID)
			if key == "" {
				key = newKey()
			}
			a.runKey = key
			params := &enrichednow.RunParams{Wait: firstWait, IdempotencyKey: key, WebhookURL: hook}
			if noWait {
				params.Wait = 0
			}
			run, err := client.Scrapers.Run(ctx, scraperID, input, params)
			progress.stop()
			if err != nil {
				return err
			}
			if noWait {
				if format, _ := a.outputFormat(); format != "table" {
					return a.emit(result{doc: run.RawJSON(), noun: "run"})
				}
				fmt.Fprintln(a.Out, run.ID)
				a.note("Run %s is %s. Follow it with `enrichednow runs wait %s`.", run.ID, run.Status, run.ID)
				return nil
			}
			run, err = a.follow(ctx, client, run, timeout)
			if err != nil {
				return err
			}
			if err := a.summary(run); err != nil {
				return err
			}
			if written != "" {
				return a.writeExport(ctx, client, run.ID, written)
			}
			return a.printRecords(ctx, client, run.ID, limit)
		},
	}
	cmd.Flags().StringVar(&key, "idempotency-key", "", "name this start (any `text`, no spaces): repeated with the same key and input, it gives back the same run")
	cmd.Flags().StringVar(&hook, "webhook-url", "", "an https `URL` that is sent a signed POST when this run ends (needs `enrichednow webhooks run-secret` once)")
	a.addFormatFlag(cmd, &written)
	cmd.Flags().StringArrayVarP(&inputs, "input", "i", nil, "one input field as `key=value`; repeat it for more")
	cmd.Flags().StringVar(&inputJSON, "input-json", "", "the input as a JSON `object`")
	cmd.Flags().StringVarP(&file, "file", "f", "", "read the input, a JSON object, from this `file` (\"-\" for standard input)")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the run, print its id and return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait for the run to end; the run goes on when the time is up")
	cmd.Flags().IntVar(&limit, "limit", 0, "print at most this many records (0: all of them)")
	a.addOutputFlags(cmd)
	return cmd
}

// buildInput puts the run's input together from the file, the JSON flag and the key=value flags.
func (a *App) buildInput(ctx context.Context, client *enrichednow.Client, scraperID, file, inputJSON string, pairs []string) (map[string]any, error) {
	input := map[string]any{}
	merge := func(source string, data []byte) error {
		var object map[string]any
		dec := json.NewDecoder(strings.NewReader(string(data)))
		dec.UseNumber()
		if err := dec.Decode(&object); err != nil || object == nil {
			return &cliError{code: "usage", msg: source + " is not a JSON object", hint: `It should look like {"domain": "acme.com"}.`, exit: exitUsage}
		}
		for key, value := range object {
			input[key] = value
		}
		return nil
	}
	if file != "" {
		var data []byte
		var err error
		source := file
		if file == "-" {
			source = "standard input"
			data, err = io.ReadAll(a.In)
		} else {
			data, err = os.ReadFile(file)
		}
		if err != nil {
			return nil, &cliError{code: "usage", msg: fmt.Sprintf("cannot read %s: %v", source, err), exit: exitUsage}
		}
		if err := merge(source, data); err != nil {
			return nil, err
		}
	}
	if inputJSON != "" {
		if err := merge("--input-json", []byte(inputJSON)); err != nil {
			return nil, err
		}
	}
	if len(pairs) == 0 {
		return input, nil
	}
	// The scraper's schema says what type each field is, so that 02139 can stay a string and 10 become a number.
	properties := map[string]any{}
	scraper, err := client.Scrapers.Get(ctx, scraperID)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if json.Unmarshal(scraper.InputSchema, &schema) == nil {
		properties, _ = schema["properties"].(map[string]any)
	}
	for _, pair := range pairs {
		key, value, ok := strings.Cut(pair, "=")
		if key = strings.TrimSpace(key); !ok || key == "" {
			return nil, &cliError{code: "usage", msg: fmt.Sprintf("--input %q is not key=value", pair), hint: "For example: --input domain=acme.com", exit: exitUsage}
		}
		node, _ := properties[key].(map[string]any)
		coerced, err := coerce(key, value, node)
		if err != nil {
			return nil, err
		}
		input[key] = coerced
	}
	return input, nil
}

// follow waits for a run to end, showing how it is doing.
func (a *App) follow(ctx context.Context, client *enrichednow.Client, run *enrichednow.Run, timeout time.Duration) (*enrichednow.Run, error) {
	if run.Status.IsFinal() {
		return run, nil
	}
	progress := a.newProgress(fmt.Sprintf("Run %s %s", run.ID, run.Status))
	defer progress.stop()
	waitCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	last := run.Status
	ended, err := client.Runs.Wait(waitCtx, run.ID, &enrichednow.WaitParams{PollInterval: a.PollInterval, OnPoll: func(r *enrichednow.Run) {
		if r.Status != last && !r.Status.IsFinal() {
			last = r.Status
			progress.set(fmt.Sprintf("Run %s %s", r.ID, r.Status))
		}
	}})
	progress.stop()
	switch {
	case err == nil:
		return ended, nil
	case ctx.Err() != nil:
		return nil, &cliError{code: "interrupted", msg: "interrupted; run " + run.ID + " goes on", exit: exitInterrupted,
			hint: fmt.Sprintf("Follow it with `enrichednow runs wait %s`, or stop it with `enrichednow runs cancel %s`.", run.ID, run.ID)}
	case errors.Is(err, context.DeadlineExceeded) && waitCtx.Err() != nil:
		return nil, &cliError{code: "timeout", msg: fmt.Sprintf("run %s has not ended after %s; it goes on", run.ID, timeout), exit: exitError,
			hint: fmt.Sprintf("Follow it with `enrichednow runs wait %s`, or stop it with `enrichednow runs cancel %s`.", run.ID, run.ID)}
	}
	return nil, err
}

// summary says on standard error how a run ended. A run that did not end ok or partial is an error.
func (a *App) summary(run *enrichednow.Run) error {
	if !run.Status.IsSuccess() {
		return runFailed(run)
	}
	charged := plural(run.Units, "unit", "units")
	if run.UnitsPerRecord != nil {
		charged += " (" + runRate(run) + ")"
	}
	parts := []string{plural(run.RecordCount, "record", "records"), charged}
	if d := runDuration(run); d > 0 {
		parts = append(parts, fmtDuration(d))
	}
	status := a.stderr.status(string(run.Status))
	if run.Reason != "" {
		status += " (reason: " + run.Reason + ")"
	}
	a.note("Run %s ended %s: %s.", run.ID, status, strings.Join(parts, ", "))
	if run.Status == enrichednow.RunPartial {
		a.note("Partial: some of the target could not be read. %s/results-and-statuses/", docsURL)
	}
	return nil
}

func runDuration(run *enrichednow.Run) time.Duration {
	if run.Diagnostics != nil && run.Diagnostics.WallMS != nil {
		return time.Duration(*run.Diagnostics.WallMS) * time.Millisecond
	}
	if run.StartedAt != nil && run.FinishedAt != nil {
		return run.FinishedAt.Sub(*run.StartedAt)
	}
	return 0
}

// printRecords fetches a run's records, page after page, and prints them.
func (a *App) printRecords(ctx context.Context, client *enrichednow.Client, runID string, limit int) error {
	var records []json.RawMessage
	cursor := ""
	for {
		size := 1000
		if limit > 0 {
			size = min(size, limit-len(records))
		}
		page, err := client.Runs.RecordsPage(ctx, runID, &enrichednow.RecordListParams{Limit: size, Cursor: cursor})
		if err != nil {
			return err
		}
		if page.Expired {
			return enrichednow.ErrRecordsExpired
		}
		records = append(records, page.Records...)
		cursor = page.NextCursor
		if cursor == "" || (limit > 0 && len(records) >= limit) {
			break
		}
	}
	if records == nil {
		records = []json.RawMessage{}
	}
	if format, _ := a.outputFormat(); len(records) == 0 && format == "table" && a.outFile == "" {
		a.note("No records.")
		return nil
	}
	if cursor != "" {
		a.note("Showing the first %s; there are more (--limit).", plural(len(records), "record", "records"))
	}
	return a.emit(result{doc: mustJSON(records), rows: records, noun: "records"})
}

// progress shows on standard error that something is going on: a line that updates in place on a terminal, a
// line per change otherwise. With --quiet it shows nothing.
type progress struct {
	app   *App
	mu    sync.Mutex
	label string
	done  chan struct{}
	wg    sync.WaitGroup
	once  sync.Once
}

func (a *App) newProgress(label string) *progress {
	p := &progress{app: a, label: label, done: make(chan struct{})}
	if a.quiet {
		return p
	}
	if !a.ErrTTY {
		if !strings.HasPrefix(label, "Starting") {
			fmt.Fprintln(a.Err, label+" …")
		}
		return p
	}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
		start := time.Now()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			p.mu.Lock()
			text := p.label
			p.mu.Unlock()
			fmt.Fprintf(a.Err, "\r\x1b[K%s %s %s", a.stderr.cyan(frames[i%len(frames)]), text, a.stderr.dim(fmt.Sprintf("%ds", int(time.Since(start).Seconds()))))
			select {
			case <-p.done:
				fmt.Fprint(a.Err, "\r\x1b[K")
				return
			case <-ticker.C:
			}
		}
	}()
	return p
}

func (p *progress) set(label string) {
	p.mu.Lock()
	p.label = label
	p.mu.Unlock()
	if !p.app.quiet && !p.app.ErrTTY {
		fmt.Fprintln(p.app.Err, label+" …")
	}
}

func (p *progress) stop() {
	p.once.Do(func() {
		close(p.done)
		p.wg.Wait()
	})
}
