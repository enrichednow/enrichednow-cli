package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	enrichednow "github.com/enrichednow/enrichednow-go"
)

// Exit codes.
const (
	exitOK          = 0
	exitError       = 1
	exitUsage       = 2
	exitRunFailed   = 3
	exitAuth        = 4
	exitInterrupted = 130
)

// cliError is an error with everything needed to tell a person what happened and what to do next.
type cliError struct {
	code    string
	msg     string
	lines   []string // more to say, one line each
	hint    string
	exit    int
	details any // for --json
}

func (e *cliError) Error() string { return e.msg }

func errNoKey() *cliError {
	return &cliError{code: "no_api_key", msg: "no API key",
		hint: "Run `enrichednow login`, or set " + envAPIKey + ". Keys are created in the console, under API keys.", exit: exitAuth}
}

// runFailed is a run that ended failed, unsupported or cancelled.
func runFailed(run *enrichednow.Run) *cliError {
	e := &cliError{code: "run_" + string(run.Status), exit: exitRunFailed, details: map[string]any{"run": json.RawMessage(run.RawJSON())}}
	e.msg = fmt.Sprintf("run %s ended %s", run.ID, run.Status)
	if run.Reason != "" {
		e.msg += fmt.Sprintf(" (reason: %s)", run.Reason)
	}
	switch {
	case run.Status == enrichednow.RunCancelled && run.RecordCount > 0:
		e.lines = append(e.lines, fmt.Sprintf("Nothing was charged. The %d records found before it stopped are kept: enrichednow runs records %s", run.RecordCount, run.ID))
	default:
		e.lines = append(e.lines, "No records, and nothing was charged.")
	}
	e.hint = fmt.Sprintf("What each status and reason means: %s/results-and-statuses/", docsURL)
	return e
}

// classify turns any error into a cliError.
func (a *App) classify(err error) *cliError {
	var own *cliError
	if errors.As(err, &own) {
		return own
	}
	var api *enrichednow.APIError
	if errors.As(err, &api) {
		e := apiFailure(api)
		if errors.Is(api, enrichednow.ErrServer) && api.Method == "POST" && strings.HasSuffix(api.Path, "/runs") {
			e.hint = a.startHint()
		}
		return e
	}
	var failed *enrichednow.RunFailedError
	if errors.As(err, &failed) {
		return runFailed(failed.Run)
	}
	switch {
	case errors.Is(err, enrichednow.ErrMissingAPIKey):
		return errNoKey()
	case errors.Is(err, enrichednow.ErrRecordsExpired):
		return &cliError{code: "records_expired", msg: "the records of this run are gone: they are kept for 7 days after a run ends",
			hint: "Run the scraper again to get fresh records.", exit: exitError}
	case errors.Is(err, context.Canceled):
		return &cliError{code: "interrupted", msg: "interrupted", exit: exitInterrupted}
	}
	var conn *enrichednow.ConnectionError
	if errors.As(err, &conn) {
		e := &cliError{code: "connection_error", msg: "could not reach the API at " + a.baseURL(), lines: []string{conn.Err.Error()},
			hint: "Check your connection, then try again.", exit: exitError}
		if a.baseURL() != enrichednow.DefaultBaseURL {
			e.hint = "Check your connection, and --api-url / " + envAPIURL + ". Then try again."
		}
		if conn.Method == "POST" && strings.HasSuffix(conn.Path, "/runs") {
			e.hint = a.startHint()
		}
		return e
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return &cliError{code: "timeout", msg: "the time ran out", exit: exitError}
	}
	if !a.started { // cobra refused the command line before any command ran
		return &cliError{code: "usage", msg: err.Error(), hint: "See `enrichednow --help`.", exit: exitUsage}
	}
	msg := strings.TrimPrefix(err.Error(), "enrichednow: ")
	return &cliError{code: "error", msg: msg, exit: exitError}
}

// startHint is what to do when starting a run failed without a clear answer: the run may exist.
func (a *App) startHint() string {
	if a.runKey == "" {
		return "The run may have been created all the same: look at `enrichednow runs list` before starting it again."
	}
	return fmt.Sprintf("The run may have been created all the same. Run the same command with `--idempotency-key %s`: "+
		"it gives back that run if there is one, and starts one if there is not.", a.runKey)
}

func apiFailure(api *enrichednow.APIError) *cliError {
	e := &cliError{code: api.Code, msg: api.Message, exit: exitError, details: api.Details}
	if e.code == "" {
		e.code = fmt.Sprintf("http_%d", api.StatusCode)
		e.msg = fmt.Sprintf("the API answered %d %s", api.StatusCode, api.Message)
	}
	for _, field := range api.Fields() {
		e.lines = append(e.lines, fmt.Sprintf("%s: %s", field.Path, field.Message))
	}
	number := func(name string) string {
		if v, ok := api.Details[name].(float64); ok {
			return fmt.Sprintf("%.0f", v)
		}
		return "?"
	}
	switch {
	case errors.Is(api, enrichednow.ErrAuthentication):
		e.exit = exitAuth
		e.hint = "Check the key: run `enrichednow login` again, or set " + envAPIKey + ". A revoked key cannot be used again; create a new one in the console."
	case errors.Is(api, enrichednow.ErrPermission):
		if api.Details["reason"] == "email_not_verified" {
			e.hint = "Confirm your email address with the link we mailed you, then try again."
		} else if strings.HasPrefix(api.Path, "/v1/data") {
			e.hint = fmt.Sprintf("Datasets is in early access: %s/datasets/#early-access", docsURL)
		}
	case errors.Is(api, enrichednow.ErrNotFound):
		switch {
		case strings.HasPrefix(api.Path, "/v1/scrapers"):
			e.hint = "List the scrapers with `enrichednow scrapers list`."
		case strings.HasPrefix(api.Path, "/v1/runs"):
			e.hint = "List your runs with `enrichednow runs list`."
		}
	case errors.Is(api, enrichednow.ErrValidation):
		if strings.HasPrefix(api.Path, "/v1/scrapers/") && strings.HasSuffix(api.Path, "/runs") {
			id := strings.TrimSuffix(strings.TrimPrefix(api.Path, "/v1/scrapers/"), "/runs")
			e.hint = fmt.Sprintf("See what it takes with `enrichednow scrapers show %s`. No run was created.", id)
		}
	case errors.Is(api, enrichednow.ErrQuotaExceeded):
		e.lines = append(e.lines, fmt.Sprintf("Used %s of %s units this month.", number("used_units"), number("allowance_units")))
		e.hint = "The allowance renews with the calendar month (UTC). See `enrichednow usage`."
	case errors.Is(api, enrichednow.ErrTooManyRuns):
		e.lines = append(e.lines, fmt.Sprintf("%s of your %s concurrent runs are going. No run was created.", number("active_runs"), number("max_concurrent_runs")))
		e.hint = "Wait for one to end (`enrichednow runs list --status running`), then try again."
	case errors.Is(api, enrichednow.ErrRateLimit):
		if api.RetryAfter > 0 {
			e.hint = fmt.Sprintf("Try again in %s.", api.RetryAfter)
		}
	case errors.Is(api, enrichednow.ErrServer):
		e.hint = "Try again in a moment."
		if api.Method == "POST" && strings.HasSuffix(api.Path, "/runs") {
			e.hint = "The run may have been created all the same: look at `enrichednow runs list` before starting it again."
		}
	}
	if api.RequestID != "" {
		e.lines = append(e.lines, "Request id: "+api.RequestID)
	}
	return e
}

// report prints an error and returns the exit code.
func (a *App) report(err error) int {
	e := a.classify(err)
	if a.jsonOut || a.format == "json" {
		details := e.details
		if details == nil {
			details = map[string]any{}
		}
		body, _ := json.Marshal(map[string]any{"error": map[string]any{"code": e.code, "message": e.msg, "details": details}})
		fmt.Fprintln(a.Err, string(body))
		return e.exit
	}
	code := ""
	if e.code != "" && e.details != nil && !strings.HasPrefix(e.code, "run_") {
		code = a.stderr.dim(" (" + e.code + ")")
	}
	fmt.Fprintf(a.Err, "%s %s%s\n", a.stderr.red("Error:"), e.msg, code)
	for _, line := range e.lines {
		fmt.Fprintf(a.Err, "  %s\n", line)
	}
	if e.hint != "" {
		fmt.Fprintf(a.Err, "%s\n", e.hint)
	}
	return e.exit
}
