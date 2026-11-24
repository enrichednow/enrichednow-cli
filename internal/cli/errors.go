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

// formulaRunFailed is a formula run that ended failed, unsupported or cancelled.
func formulaRunFailed(run *enrichednow.FormulaRun) *cliError {
	e := &cliError{code: "run_" + string(run.Status), exit: exitRunFailed, details: map[string]any{"run": json.RawMessage(run.RawJSON())}}
	e.msg = fmt.Sprintf("formula run %s ended %s", run.ID, run.Status)
	if run.Reason != "" {
		e.msg += fmt.Sprintf(" (reason: %s)", run.Reason)
	}
	if run.RowsTotal > 0 {
		e.lines = append(e.lines, fmt.Sprintf("The %s it wrote are kept: enrichednow formulas rows %s", plural(run.RowsTotal, "row", "rows"), run.ID))
	} else {
		e.lines = append(e.lines, "No rows.")
	}
	e.lines = append(e.lines, reasonLines(run)...)
	if run.UnitsSpent > 0 {
		e.lines = append(e.lines, fmt.Sprintf("%s spent on what it did.", plural(run.UnitsSpent, "unit was", "units were")))
	}
	e.hint = fmt.Sprintf("See what it needs with `enrichednow formulas get %s`, and try a --preview.", run.FormulaID)
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
	var formulaFailed *enrichednow.FormulaRunFailedError
	if errors.As(err, &formulaFailed) {
		return formulaRunFailed(formulaFailed.Run)
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
	case errors.Is(api, enrichednow.ErrPlanLimitReached): // before ErrPermission, which it matches too
		switch api.Details["limit"] {
		case "min_interval_minutes":
			e.lines = append(e.lines, fmt.Sprintf("The plan runs a saved formula every %s at the shortest; this asked for every %s.",
				minutes(api.Details["allowed"]), minutes(api.Details["asked"])))
		case "schedules":
			e.lines = append(e.lines, fmt.Sprintf("The plan allows %s active schedules at once, and %s are active.", number("allowed"), number("current")))
		case "destinations":
			e.lines = append(e.lines, fmt.Sprintf("The plan allows %s destinations for a saved formula.", number("allowed")))
		}
		e.hint = "Choose what the plan allows, or change the plan in the console, on the Billing page."
	case errors.Is(api, enrichednow.ErrPermission):
		if api.Details["reason"] == "email_not_verified" {
			e.hint = "Confirm your email address with the link we mailed you, then try again."
		} else if strings.HasPrefix(api.Path, "/v1/data") {
			e.hint = fmt.Sprintf("Datasets is in early access: %s/datasets/#early-access", docsURL)
		}
	case errors.Is(api, enrichednow.ErrAttestationRequired):
		e.lines = append(e.lines, fmt.Sprintf("The terms: %v, version %v. Read them in the console, on the Formulas page.", api.Details["kind"], api.Details["text_version"]))
		e.hint = "An owner or an admin of the account accepts them in the console, on the Formulas page; an API key cannot. Then run it again."
	case errors.Is(api, enrichednow.ErrRequirementMissing):
		for _, missing := range api.Missing() {
			what := missing
			switch {
			case missing == "product:data":
				what += ": Datasets, which this account has not been given (early access)"
			case strings.HasPrefix(missing, "product:"):
				what += ": a product this account does not have"
			case strings.HasPrefix(missing, "capability:"):
				what += ": connect a provider for it in the console"
			}
			e.lines = append(e.lines, "Missing "+what+".")
		}
		e.hint = "See what the formula needs with `enrichednow formulas get " + formulaOf(api.Path) + "`."
	case errors.Is(api, enrichednow.ErrFormulaUnavailable):
		if status, ok := api.Details["status"].(string); ok {
			e.lines = append(e.lines, "The formula is "+status+".")
		}
		e.hint = "List the formulas you can run with `enrichednow formulas list`."
	case errors.Is(api, enrichednow.ErrSavedFormulaArchived):
		e.hint = "An archived saved formula cannot be changed or run. Duplicate it to use it again: `enrichednow saved duplicate " + savedOf(api.Path) + "`."
	case errors.Is(api, enrichednow.ErrNotFound):
		switch {
		case strings.HasPrefix(api.Path, "/v1/scrapers"):
			e.hint = "List the scrapers with `enrichednow scrapers list`."
		case strings.HasPrefix(api.Path, "/v1/runs"):
			e.hint = "List your runs with `enrichednow runs list`."
		case strings.HasPrefix(api.Path, "/v1/formulas"):
			e.hint = "List the formulas with `enrichednow formulas list`."
		case strings.HasPrefix(api.Path, "/v1/saved-formulas"):
			e.hint = "List your saved formulas with `enrichednow saved list`."
		case strings.HasPrefix(api.Path, "/v1/formula-runs"):
			e.hint = "Check the run id: `enrichednow formulas run` prints it."
		}
	case errors.Is(api, enrichednow.ErrValidation):
		switch {
		case api.Code == enrichednow.CodeUnitCapTooLow:
			if least, ok := api.MinUnitCap(); ok {
				e.lines = append(e.lines, fmt.Sprintf("The least unit cap it takes is %d.", least))
			}
			e.hint = "Give a higher --unit-cap, or leave it out for the formula's own. No run was created."
		case strings.HasPrefix(api.Path, "/v1/scrapers/") && strings.HasSuffix(api.Path, "/runs"):
			id := strings.TrimSuffix(strings.TrimPrefix(api.Path, "/v1/scrapers/"), "/runs")
			e.hint = fmt.Sprintf("See what it takes with `enrichednow scrapers show %s`. No run was created.", id)
		case strings.HasPrefix(api.Path, "/v1/formulas/"):
			e.hint = fmt.Sprintf("See its parameters with `enrichednow formulas get %s`.", formulaOf(api.Path))
		}
	case errors.Is(api, enrichednow.ErrLimitReached): // a limit of the account's own, of the key or of a saved formula: not the plan
		whose, hint := "The account's monthly spend limit", "An owner, an admin or a billing user can raise or remove it in the console, on the Usage page."
		switch api.Details["scope"] {
		case "key":
			whose, hint = "This API key's monthly limit", "Raise or remove the key's limit in the console, under API keys, or use another key."
		case "saved_formula":
			whose, hint = "This saved formula's monthly unit cap", "Raise or remove its monthly cap in the console, or wait for next month."
		case "formula_run":
			whose, hint = "This formula run's limit", "Give the run a higher --unit-cap."
		}
		e.lines = append(e.lines, fmt.Sprintf("%s: %s of %s units used this month; this run needs %s.", whose, number("used_units"), number("limit_units"), number("units_needed")))
		e.hint = hint
	case errors.Is(api, enrichednow.ErrQuotaExceeded):
		e.lines = append(e.lines, fmt.Sprintf("Used %s of %s units this month.", number("used_units"), number("allowance_units")))
		if _, ok := api.Details["units_needed"]; ok {
			e.lines = append(e.lines, fmt.Sprintf("This run needs %s units; %s top-up units are left.", number("units_needed"), number("topup_units")))
		}
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

// minutes is an interval of the plan for a person: "hour", "day", "30 minutes".
func minutes(v any) string {
	n, _ := v.(float64)
	switch {
	case n == 60:
		return "hour"
	case n == 1440:
		return "day"
	case n > 0 && int(n)%1440 == 0:
		return fmt.Sprintf("%d days", int(n)/1440)
	}
	return fmt.Sprintf("%.0f minutes", n)
}

// formulaOf is the formula id in the path of a request, or a placeholder.
func formulaOf(path string) string {
	if parts := strings.Split(strings.Trim(path, "/"), "/"); len(parts) >= 3 && parts[1] == "formulas" {
		return parts[2]
	}
	return "<formula-id>"
}

// savedOf is the saved formula id in the path of a request, or a placeholder.
func savedOf(path string) string {
	if parts := strings.Split(strings.Trim(path, "/"), "/"); len(parts) >= 3 && parts[1] == "saved-formulas" {
		return parts[2]
	}
	return "<saved-formula-id>"
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
