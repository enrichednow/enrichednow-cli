package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

// The formats rows are written in: a table, JSON or JSON lines here, or CSV and NDJSON as the API writes them.
var rowFormats = []string{"json", enrichednow.FormatCSV, enrichednow.FormatNDJSON}

const paramsHelp = `The params are put together from --params-json (a JSON object), then each --param name=value, which wins
for its name. A --param value is turned into what the formula's parameter asks for: a number, true/false, a list
(repeat the flag for each item, or give a,b,c), a range such as 11-5000, or JSON for anything else. See the
parameters with "enrichednow formulas get <formula-id>".`

const rowsHelp = `The table shows the formula's main columns, as many as fit; --output json and jsonl print every row as the API
gives it (its columns in "data", its score, and where each column came from in "evidence"). CSV and NDJSON are
written by the API, every row of the run: a CSV has a column for each of the formula's columns, in its order (or
for each of --fields), and its header row once. --out rows.csv or rows.ndjson picks the format by the extension.`

func (a *App) cmdFormulas() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "formulas",
		GroupID: "formulas",
		Short:   "Ready-made formulas: a few parameters in, a finished list of rows out",
		Long: `A formula turns a few parameters into a finished list of rows: companies hiring for your roles with the person
to contact, an account list researched, a directory turned into leads. A run reads its sources, matches and checks
what it found, and writes rows, best first.

"enrichednow formulas get <formula-id>" shows what one takes and returns, "enrichednow formulas estimate" what a
run would cost, for nothing. A run spends units up to its unit cap, which it never passes; a preview is at most 10
rows and 50 units. To run one again by hand or on a schedule, save it: "enrichednow saved create".`,
		Example: `  enrichednow formulas list --category recruiting
  enrichednow formulas get job-order-hunter
  enrichednow formulas run job-order-hunter --param job_titles="Java Developer" --param locations=London --preview
  enrichednow formulas rows frun_000000000000000000000002 --out leads.csv`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.cmdFormulasList(), a.cmdFormulasGet(), a.cmdFormulasEstimate(), a.cmdFormulasRun(), a.cmdFormulasWait(),
		a.cmdFormulasRows(), a.cmdFormulasCancel())
	return cmd
}

// completeFormulas completes a formula id from the catalogue.
func (a *App) completeFormulas(cmd *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	client, err := a.client(false)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	ctx, cancel := context.WithTimeout(cmd.Context(), 3*time.Second)
	defer cancel()
	formulas, err := client.Formulas.List(ctx, nil)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var out []string
	for _, f := range formulas {
		out = append(out, f.ID+"\t"+f.Title)
	}
	return out, cobra.ShellCompDirectiveNoFileComp
}

func (a *App) cmdFormulasList() *cobra.Command {
	var query, category, persona, status string
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "The formulas: what each is for",
		Long: `List the formulas. No API key is needed; with one, "available" in the JSON says whether each can run for your
account.`,
		Example: `  enrichednow formulas list
  enrichednow formulas list --query hiring --category recruiting
  enrichednow formulas list --json | jq -r '.formulas[].id'`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(false)
			if err != nil {
				return err
			}
			formulas, err := client.Formulas.List(cmd.Context(), &enrichednow.FormulaListParams{Query: query, Category: category, Persona: persona, Status: status})
			if err != nil {
				return err
			}
			if formulas == nil {
				formulas = []enrichednow.Formula{}
			}
			doc := mustJSON(map[string]any{"formulas": formulas})
			return a.emit(result{doc: doc, rows: listOf(doc, "formulas"), noun: "formulas", human: func(w io.Writer, st *style, width int) {
				if len(formulas) == 0 {
					a.note("No formulas match.")
					return
				}
				t := &table{head: []string{"ID", "TITLE", "CATEGORY", "STATUS", "SUMMARY"},
					paint: map[int]func(string) string{0: st.bold, 3: func(s string) string { return formulaStatus(st, s) }}}
				for _, f := range formulas {
					t.rows = append(t.rows, []string{f.ID, f.Title, f.Category, f.Status, f.Summary})
				}
				if width <= 0 {
					width = defaultWidth
				}
				t.render(w, st, width)
				a.note("See what one takes and returns: enrichednow formulas get <id>")
			}})
		},
	}
	cmd.Flags().StringVarP(&query, "query", "Q", "", "what you want, in your own `words`: the formulas that match come best first")
	cmd.Flags().StringVar(&category, "category", "", "only formulas of this `category`, e.g. recruiting")
	cmd.Flags().StringVar(&persona, "persona", "", "only formulas made for this `persona`, e.g. recruiter")
	cmd.Flags().StringVar(&status, "status", "", "only formulas with this `status`: beta or live")
	_ = cmd.RegisterFlagCompletionFunc("status", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{enrichednow.FormulaBeta, enrichednow.FormulaLive}, cobra.ShellCompDirectiveNoFileComp
	})
	a.addOutputFlags(cmd)
	return cmd
}

func formulaStatus(st *style, status string) string {
	switch strings.TrimSpace(status) {
	case enrichednow.FormulaLive:
		return st.green(status)
	case enrichednow.FormulaBeta:
		return st.cyan(status)
	case enrichednow.FormulaDeprecated:
		return st.yellow(status)
	}
	return status
}

func (a *App) cmdFormulasGet() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get <formula-id>",
		Aliases: []string{"show"},
		Short:   "One formula: what it does, its parameters, its columns and what it needs",
		Long: `Show one formula: what it does, the parameters it takes (required or optional, with their defaults, limits and
help), the columns of its rows, the cadences it can be scheduled at, and whether its terms must be accepted first.
No API key is needed.

With --json the answer is the API's: the formula with the JSON Schema of its parameters, its presets, steps,
columns, limits and sample rows.`,
		Example: `  enrichednow formulas get job-order-hunter
  enrichednow formulas get job-order-hunter --json | jq .parameters`,
		Args:              exactArgs("formula-id"),
		ValidArgsFunction: a.completeFormulas,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(false)
			if err != nil {
				return err
			}
			f, err := client.Formulas.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: f.RawJSON(), noun: "formula", human: func(w io.Writer, st *style, width int) {
				if width <= 0 {
					width = defaultWidth
				}
				printFormula(w, st, width, f)
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func printFormula(w io.Writer, st *style, width int, f *enrichednow.FormulaDetails) {
	fmt.Fprintf(w, "%s  %s\n%s\n\n", st.bold(f.ID), f.Title, f.Summary)
	schedules := "none: it runs once"
	if len(f.Schedules) > 0 {
		schedules = strings.Join(f.Schedules, ", ") + " (recommended: " + f.RecommendedCadence + ")"
	}
	items := [][2]string{
		{"Version", f.Version + ", " + formulaStatus(st, f.Status)},
		{"Category", f.Category},
		{"For", strings.Join(f.Personas, ", ")},
		{"Use when", f.UseWhen},
		{"Schedules", schedules},
	}
	if f.NeedsAttestation {
		items = append(items, [2]string{"Terms", st.yellow("must be accepted first") + ": it returns people. An owner or an admin accepts them in the console, on the Formulas page."})
	}
	if f.NeedsProvider {
		items = append(items, [2]string{"Contacts", "the contact columns stay empty until a people provider is connected, in the console"})
	}
	switch {
	case len(f.Requirements.Missing) > 0:
		items = append(items, [2]string{"Can run", st.red("no") + ": missing " + strings.Join(f.Requirements.Missing, ", ")})
	case !f.Available:
		items = append(items, [2]string{"Can run", st.red("not now")})
	}
	if n := len(f.Requirements.NotOffered); n > 0 {
		items = append(items, [2]string{"Not offered", strings.Join(f.Requirements.NotOffered, ", ") + map[bool]string{true: ": runs go on without it", false: ": runs go on without them"}[n == 1]})
	}
	if l := f.Limits; len(l) > 0 {
		items = append(items, [2]string{"Limits", fmt.Sprintf("%d rows a run by default, at most %d; a unit cap of %d by default, %d to %d",
			l["default_rows"], l["max_rows"], l["default_unit_cap"], l["min_unit_cap"], l["max_unit_cap"])})
	}
	pairs(w, st, items)
	if f.Description != "" {
		fmt.Fprintf(w, "\n%s\n", wrap(f.Description, min(width, 100)))
	}

	params := schemaFields(f.Parameters)
	fmt.Fprintf(w, "\n%s\n", st.bold("PARAMETERS"))
	if len(params) == 0 {
		fmt.Fprintln(w, "  (none)")
	} else {
		t := &table{head: []string{"  PARAMETER", "TYPE", "REQUIRED", "DEFAULT", "HELP"}}
		for _, p := range params {
			required := "optional"
			if p.Required {
				required = "required"
			}
			kind, help := p.Type, p.Description
			if at := strings.Index(kind, "one of "); at >= 0 { // the choices go with the help, where there is room for them
				help = strings.TrimSpace(help + " Choices: " + kind[at+len("one of "):] + ".")
				kind = map[bool]string{true: "list of choices", false: "choice"}[strings.HasPrefix(kind, "list")]
			}
			t.rows = append(t.rows, []string{"  " + p.Name, kind, required, p.Default, help})
		}
		t.render(w, st, width)
	}

	fmt.Fprintf(w, "\n%s\n", st.bold("COLUMNS"))
	t := &table{head: []string{"  COLUMN", "LABEL", "TYPE", "IN THE TABLE"}}
	for _, c := range f.Columns {
		shown := "no"
		if c.DefaultVisible {
			shown = "yes"
		}
		t.rows = append(t.rows, []string{"  " + c.Key, c.Label, c.Type, shown})
	}
	t.render(w, st, width)
	if f.CompanyView != nil {
		fmt.Fprintf(w, "  %s\n", st.dim("The rows can also be read one row per company: --view company."))
	}

	if len(f.Presets) > 0 {
		fmt.Fprintf(w, "\n%s\n", st.bold("PRESETS"))
		var presets [][2]string
		for _, p := range f.Presets {
			presets = append(presets, [2]string{"  " + p.ID, p.Label + "  " + st.dim(cellText(p.Params))})
		}
		pairs(w, st, presets)
	}

	fmt.Fprintf(w, "\n%s\n  %s\n", st.bold("RUN IT"), exampleFormulaRun(f.ID, params))
}

// exampleFormulaRun is a command line that runs a formula as a preview, with a placeholder for every required
// parameter.
func exampleFormulaRun(id string, params []schemaField) string {
	var b strings.Builder
	b.WriteString("enrichednow formulas run " + id)
	for _, p := range params {
		if p.Required {
			fmt.Fprintf(&b, " --param %s=<%s>", p.Name, p.Name)
		}
	}
	b.WriteString(" --preview")
	return b.String()
}

// --- the params -------------------------------------------------------------------------------------------------------

// paramFlags are the flags that give a formula its params.
type paramFlags struct {
	pairs []string
	json  string
}

func (p *paramFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringArrayVarP(&p.pairs, "param", "p", nil, "one parameter as `name=value`; repeat it for more, and for each item of a list")
	cmd.Flags().StringVar(&p.json, "params-json", "", "the params as a JSON `object`; a --param given too wins for its name")
}

// buildParams puts a run's params together: --params-json, then each --param, turned into what the formula's
// parameter schema asks for. It returns the formula too when it had to read it.
func (a *App) buildParams(ctx context.Context, client *enrichednow.Client, formulaID string, p paramFlags) (map[string]any, *enrichednow.FormulaDetails, error) {
	params := map[string]any{}
	if p.json != "" {
		dec := json.NewDecoder(strings.NewReader(p.json))
		dec.UseNumber()
		var object map[string]any
		if err := dec.Decode(&object); err != nil || object == nil {
			return nil, nil, &cliError{code: "usage", msg: "--params-json is not a JSON object", exit: exitUsage,
				hint: `It should look like {"job_titles": ["Java Developer"], "locations": ["London"]}.`}
		}
		params = object
	}
	if len(p.pairs) == 0 {
		return params, nil, nil
	}
	// The parameter schema says what type each one is, so that 02139 can stay a string and 10 become a number.
	details, err := client.Formulas.Get(ctx, formulaID)
	if err != nil {
		return nil, nil, err
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
	}
	_ = json.Unmarshal(details.Parameters, &schema)
	given := map[string]bool{}
	for _, pair := range p.pairs {
		name, value, ok := strings.Cut(pair, "=")
		if name = strings.TrimSpace(name); !ok || name == "" {
			return nil, nil, &cliError{code: "usage", msg: fmt.Sprintf("--param %q is not name=value", pair), hint: "For example: --param locations=London", exit: exitUsage}
		}
		node, known := schema.Properties[name]
		if !known && len(schema.Properties) > 0 {
			return nil, nil, &cliError{code: "usage", msg: fmt.Sprintf("--param %s: the formula %s has no parameter %q", pair, formulaID, name), exit: exitUsage,
				hint: fmt.Sprintf("See its parameters with `enrichednow formulas get %s`.", formulaID)}
		}
		v, err := coerceParam(name, value, node)
		if err != nil {
			return nil, nil, err
		}
		if items, isList := v.([]any); isList && given[name] {
			earlier, _ := params[name].([]any)
			v = append(earlier, items...) // a list parameter given again: one more item
		}
		params[name] = v
		given[name] = true
	}
	return params, details, nil
}

var rangePattern = regexp.MustCompile(`^\s*(\d+)\s*(?:-|\.\.|to)\s*(\d+)\s*$`)

// coerceParam turns the text of `--param name=value` into the JSON value the parameter's schema asks for: as
// --input does for a scraper, and a range such as 11-5000 for a parameter that is a min and a max.
func coerceParam(name, value string, node map[string]any) (any, error) {
	if props, _ := node["properties"].(map[string]any); props["min"] != nil && props["max"] != nil {
		if m := rangePattern.FindStringSubmatch(value); m != nil {
			low, _ := strconv.Atoi(m[1])
			high, _ := strconv.Atoi(m[2])
			return map[string]any{"min": low, "max": high}, nil
		}
	}
	v, err := coerce(name, value, node)
	var e *cliError
	if errors.As(err, &e) {
		e.msg = strings.Replace(e.msg, "--input ", "--param ", 1)
	}
	return v, err
}

// --- estimating ----------------------------------------------------------------------------------------------------------

func (a *App) cmdFormulasEstimate() *cobra.Command {
	var (
		p               paramFlags
		unitCap, rowCap int
		preview         bool
	)
	cmd := &cobra.Command{
		Use:   "estimate <formula-id>",
		Short: "What a run would cost, and how many rows it would give; spends nothing",
		Long: `Ask what a run with these params would cost, and how many rows it would give. It spends nothing.

` + paramsHelp + `

Params that cannot run are listed, one problem a line, and the exit code is 1. The units are an estimate: the most
it can cost is the only promise, and a run never passes its unit cap.`,
		Example: `  enrichednow formulas estimate job-order-hunter --param job_titles="Java Developer" --param locations=London
  enrichednow formulas estimate job-order-hunter --params-json '{"job_titles": ["Nurse"], "locations": ["Texas"]}' --unit-cap 2000`,
		Args:              exactArgs("formula-id"),
		ValidArgsFunction: a.completeFormulas,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, formulaID := cmd.Context(), args[0]
			if _, err := a.outputFormat(); err != nil {
				return err
			}
			if err := checkCaps(unitCap, rowCap); err != nil {
				return err
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			params, _, err := a.buildParams(ctx, client, formulaID, p)
			if err != nil {
				return err
			}
			e, err := client.Formulas.Estimate(ctx, formulaID, params, &enrichednow.FormulaEstimateParams{Preview: preview, UnitCap: unitCap, RowCap: rowCap})
			if err != nil {
				return err
			}
			if !e.Valid {
				if format, _ := a.outputFormat(); format != "table" {
					if err := a.emit(result{doc: e.RawJSON(), noun: "estimate"}); err != nil {
						return err
					}
				}
				invalid := &cliError{code: "invalid_params", msg: "these params cannot run", exit: exitError, details: map[string]any{"errors": e.Errors},
					hint: fmt.Sprintf("See its parameters with `enrichednow formulas get %s`.", formulaID)}
				for _, problem := range e.Errors {
					invalid.lines = append(invalid.lines, problem.Path+": "+problem.Message)
				}
				return invalid
			}
			return a.emit(result{doc: e.RawJSON(), noun: "estimate", human: func(w io.Writer, st *style, _ int) {
				if est := e.Estimate; est != nil {
					fmt.Fprintf(w, "%s About %s (at least %d, at most %d), and about %s.\n", st.green("Valid."), plural(est.ExpectedUnits, "unit", "units"),
						est.MinUnits, est.MaxUnits, plural(est.ExpectedRows, "row", "rows"))
					fmt.Fprintln(w, st.dim(est.Basis))
				}
				if len(e.Missing) > 0 {
					fmt.Fprintf(w, "%s %s: the run would be refused.\n", st.red("Missing:"), strings.Join(e.Missing, ", "))
				}
				if len(e.NotOffered) > 0 {
					fmt.Fprintf(w, "Not offered here: %s. The run goes on without it.\n", strings.Join(e.NotOffered, ", "))
				}
			}})
		},
	}
	p.register(cmd)
	cmd.Flags().IntVar(&unitCap, "unit-cap", 0, "the most the run would spend, in `units` (default: the formula's)")
	cmd.Flags().IntVar(&rowCap, "row-cap", 0, "the most `rows` it would produce (default: the formula's)")
	cmd.Flags().BoolVar(&preview, "preview", false, "estimate a preview: at most 10 rows and 50 units")
	a.addOutputFlags(cmd)
	return cmd
}

func checkCaps(caps ...int) error {
	for _, n := range caps {
		if n < 0 {
			return &cliError{code: "usage", msg: "a cap is a number of 1 or more", exit: exitUsage}
		}
	}
	return nil
}

// --- running -------------------------------------------------------------------------------------------------------------

// rowFlags are the flags that narrow and limit the rows a command prints.
type rowFlags struct {
	fields   string
	minScore float64
	view     string
	limit    int
	written  string
	cmd      *cobra.Command
}

func (r *rowFlags) register(cmd *cobra.Command, withView bool) {
	r.cmd = cmd
	cmd.Flags().StringVar(&r.fields, "fields", "", "only these `columns`, comma-separated, in this order")
	cmd.Flags().Float64Var(&r.minScore, "min-score", 0, "only rows scored at least `n`")
	if withView {
		cmd.Flags().StringVar(&r.view, "view", "", "rows (the default), or company: one row per company, for a formula that has it")
		_ = cmd.RegisterFlagCompletionFunc("view", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return []string{enrichednow.ViewRows, enrichednow.ViewCompany}, cobra.ShellCompDirectiveNoFileComp
		})
	}
	cmd.Flags().IntVar(&r.limit, "limit", 0, "print at most this many rows (0: all of them; csv and ndjson are always all of them)")
	cmd.Flags().StringVar(&r.written, "format", "", "json, or csv or ndjson as the API writes them (instead of --output)")
	_ = cmd.RegisterFlagCompletionFunc("format", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return rowFormats, cobra.ShellCompDirectiveNoFileComp
	})
}

func (r *rowFlags) fieldList() []string {
	var out []string
	for _, f := range strings.Split(r.fields, ",") {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (r *rowFlags) min() *float64 {
	if r.cmd != nil && r.cmd.Flags().Changed("min-score") {
		return &r.minScore
	}
	return nil
}

// rowsFormat says how rows are written: "table", "json" or "jsonl" here, or "csv" or "ndjson" as the API writes
// them. --format wins; then --output (or --json); then the extension of --out.
func (a *App) rowsFormat(written string) (string, error) {
	if written != "" {
		written = strings.ToLower(written)
		if written != "json" && written != enrichednow.FormatCSV && written != enrichednow.FormatNDJSON {
			return "", &cliError{code: "usage", msg: fmt.Sprintf("--format %q is not json, csv or ndjson", written), exit: exitUsage}
		}
		asked := strings.ToLower(a.format)
		if a.jsonOut {
			asked = "json"
		}
		if asked != "" && asked != written {
			return "", &cliError{code: "usage", msg: "--format and --output (or --json) ask for two different formats: give one of them", exit: exitUsage}
		}
		if written == "json" {
			a.format = "json"
		}
		return written, nil
	}
	if a.format == "" && !a.jsonOut && strings.EqualFold(filepath.Ext(a.outFile), ".ndjson") {
		return enrichednow.FormatNDJSON, nil
	}
	return a.outputFormat()
}

// rowSource is where rows come from: a formula run, or a saved formula across its runs.
type rowSource struct {
	page      func(ctx context.Context, limit int, cursor string) (*enrichednow.FormulaRowPage, error)
	exportAll func(ctx context.Context, format string, w io.Writer) error
	// formulaID tells the columns of the table; it may read the API.
	formulaID func(ctx context.Context) string
}

func runRows(client *enrichednow.Client, runID string, r *rowFlags) rowSource {
	return rowSource{
		page: func(ctx context.Context, limit int, cursor string) (*enrichednow.FormulaRowPage, error) {
			return client.FormulaRuns.RowsPage(ctx, runID, &enrichednow.FormulaRowListParams{Fields: r.fieldList(), MinScore: r.min(), View: r.view, Limit: limit, Cursor: cursor})
		},
		exportAll: func(ctx context.Context, format string, w io.Writer) error {
			return client.FormulaRuns.ExportAll(ctx, runID, enrichednow.FormulaExportParams{Format: format, Fields: r.fieldList(), MinScore: r.min(), View: r.view}, w)
		},
		formulaID: func(ctx context.Context) string {
			if run, err := client.FormulaRuns.Get(ctx, runID); err == nil {
				return run.FormulaID
			}
			return ""
		},
	}
}

// writeRows prints a source's rows in the format asked for.
func (a *App) writeRows(ctx context.Context, client *enrichednow.Client, src rowSource, details *enrichednow.FormulaDetails, r *rowFlags, format string) error {
	if format == enrichednow.FormatCSV || format == enrichednow.FormatNDJSON {
		if r.limit > 0 {
			a.note("--limit does not apply to %s: it is every row.", format)
		}
		return a.writeRowsExport(format, func(w io.Writer) error { return src.exportAll(ctx, format, w) })
	}
	var rows []json.RawMessage
	cursor := ""
	for {
		size := 1000
		if r.limit > 0 {
			size = min(size, r.limit-len(rows))
		}
		page, err := src.page(ctx, size, cursor)
		if err != nil {
			return err
		}
		rows = append(rows, listOf(page.RawJSON(), "rows")...)
		cursor = page.NextCursor
		if r.limit > 0 && len(rows) >= r.limit {
			if len(rows) > r.limit {
				rows, cursor = rows[:r.limit], "more"
			}
			break
		}
		if cursor == "" {
			break
		}
	}
	if rows == nil {
		rows = []json.RawMessage{}
	}
	if len(rows) == 0 && format == "table" && a.outFile == "" {
		a.note("No rows.")
		return nil
	}
	if cursor != "" {
		a.note("Showing the first %s; there are more (--limit).", plural(len(rows), "row", "rows"))
	}
	return a.emit(result{doc: mustJSON(rows), rows: rows, noun: "rows", human: func(w io.Writer, st *style, width int) {
		a.rowsTable(w, st, width, rows, a.rowColumns(ctx, client, src, details, r))
	}})
}

// rowColumns are the columns of the table: --fields, or the formula's main ones.
func (a *App) rowColumns(ctx context.Context, client *enrichednow.Client, src rowSource, details *enrichednow.FormulaDetails, r *rowFlags) []string {
	if fields := r.fieldList(); len(fields) > 0 {
		return fields
	}
	if details == nil {
		if id := src.formulaID(ctx); id != "" {
			details, _ = client.Formulas.Get(ctx, id)
		}
	}
	if details == nil {
		return nil
	}
	var columns []string
	for _, c := range details.Columns {
		if c.DefaultVisible {
			columns = append(columns, c.Key)
		}
	}
	if r.view == enrichednow.ViewCompany {
		columns = append(columns, "rows")
	}
	return columns
}

// rowsTable prints the columns of rows, in the order given (without one, the row's own), as many as fit. A list of
// plain values is one cell, its items joined with commas.
func (a *App) rowsTable(w io.Writer, st *style, width int, rows []json.RawMessage, columns []string) {
	shown := make([]json.RawMessage, 0, len(rows))
	for _, raw := range rows {
		var row struct {
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(raw, &row)
		fields, _ := objectFields(row.Data)
		values := map[string]json.RawMessage{}
		keys := columns
		for _, f := range fields {
			values[f.Key] = f.Value
			if len(columns) == 0 {
				keys = append(keys, f.Key)
			}
		}
		var b bytes.Buffer
		b.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			b.Write(mustJSON(key))
			b.WriteByte(':')
			b.Write(joinedList(values[key]))
		}
		b.WriteByte('}')
		shown = append(shown, b.Bytes())
	}
	a.genericTable(w, st, width, shown)
}

// joinedList is a list of plain values as one string, "a, b"; anything else as it is, and nothing as null.
func joinedList(raw json.RawMessage) json.RawMessage {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null")
	}
	var items []any
	if json.Unmarshal(raw, &items) != nil {
		return raw
	}
	texts := make([]string, 0, len(items))
	for _, item := range items {
		switch item.(type) {
		case map[string]any, []any:
			return raw
		case nil:
			continue
		}
		texts = append(texts, fmt.Sprint(item))
	}
	return mustJSON(strings.Join(texts, ", "))
}

// writeRowsExport writes rows as the API writes them, to standard output or to the --out file.
func (a *App) writeRowsExport(format string, export func(w io.Writer) error) error {
	var buf bytes.Buffer
	var w io.Writer = a.Out
	if a.outFile != "" {
		w = &buf
	}
	if err := export(w); err != nil {
		return err
	}
	if a.outFile == "" {
		return nil
	}
	if err := os.WriteFile(a.outFile, buf.Bytes(), 0o644); err != nil {
		return &cliError{code: "error", msg: fmt.Sprintf("cannot write %s: %v", a.outFile, err), exit: exitError}
	}
	count := bytes.Count(buf.Bytes(), []byte{'\n'})
	if format == enrichednow.FormatCSV {
		lines, err := csv.NewReader(bytes.NewReader(buf.Bytes())).ReadAll()
		count = max(0, len(lines)-1) // the header row
		if err != nil {
			count = 0
		}
	}
	a.note("Wrote %s to %s (%s, as the API writes it).", plural(count, "row", "rows"), a.outFile, format)
	return nil
}

func (a *App) cmdFormulasRun() *cobra.Command {
	var (
		p               paramFlags
		r               rowFlags
		preview, noWait bool
		unitCap, rowCap int
		timeout         time.Duration
		key             string
	)
	cmd := &cobra.Command{
		Use:   "run <formula-id>",
		Short: "Run a formula, wait for it and print its rows",
		Long: `Start a run of a formula, show its progress, wait until it has ended and print its rows, best first.

` + paramsHelp + `

--preview is at most 10 rows and 50 units, charged as usual, and nothing it returns is remembered as delivered.
--unit-cap is the most the run may spend: it never passes it, and stops at it with what it has (partial).

Progress and the summary go to standard error and the rows to standard output. The exit code is 0 when the run
ended ok or partial, and 3 when it ended failed, unsupported or cancelled.

` + rowsHelp + `

The request that starts a run carries an idempotency key, as "enrichednow run" does: --idempotency-key makes the
command safe to repeat. Interrupting the wait (Ctrl-C) does not stop the run; "enrichednow formulas cancel" does.`,
		Example: `  # A preview: what the rows look like, for at most 50 units
  enrichednow formulas run job-order-hunter --param job_titles="Java Developer" --param locations=London --preview

  # A list parameter takes the flag again, or a,b,c; a range takes 11-5000
  enrichednow formulas run job-order-hunter -p job_titles="Java Developer" -p job_titles=DevOps -p locations=London,Leeds -p company_size=11-5000

  # At most 2000 units
  enrichednow formulas run job-order-hunter -p job_titles=Nurse -p locations=Texas --unit-cap 2000

  # The whole run as a CSV file
  enrichednow formulas run job-order-hunter --params-json '{"job_titles": ["Nurse"], "locations": ["Texas"]}' --out leads.csv

  # Start it and come back later
  id=$(enrichednow formulas run job-order-hunter --param job_titles=Nurse --param locations=Texas --no-wait)
  enrichednow formulas wait "$id" && enrichednow formulas rows "$id" --out leads.csv`,
		Args:              exactArgs("formula-id"),
		ValidArgsFunction: a.completeFormulas,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, formulaID := cmd.Context(), args[0]
			format, err := a.rowsFormat(r.written)
			if err != nil {
				return err
			}
			if key != "" && !validKey(key) {
				return &cliError{code: "usage", msg: "--idempotency-key must be 1 to 255 visible characters, without spaces", exit: exitUsage}
			}
			if err := checkCaps(unitCap, rowCap); err != nil {
				return err
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			params, details, err := a.buildParams(ctx, client, formulaID, p)
			if err != nil {
				return err
			}
			progress := a.newProgress("Starting " + formulaID)
			if key == "" {
				key = newKey()
			}
			a.runKey = key
			opts := &enrichednow.FormulaRunParams{Preview: preview, UnitCap: unitCap, RowCap: rowCap, Wait: firstWait, IdempotencyKey: key}
			if noWait {
				opts.Wait = 0
			}
			run, err := client.Formulas.Run(ctx, formulaID, params, opts)
			progress.stop()
			if err != nil {
				return err
			}
			return a.formulaRunOutput(ctx, client, run, details, noWait, timeout, &r, format)
		},
	}
	p.register(cmd)
	cmd.Flags().BoolVar(&preview, "preview", false, "a preview: at most 10 rows and 50 units, nothing remembered as delivered")
	cmd.Flags().IntVar(&unitCap, "unit-cap", 0, "the most the run may spend, in `units` (default: the formula's)")
	cmd.Flags().IntVar(&rowCap, "row-cap", 0, "the most `rows` it may produce (default: its max_results, or the formula's)")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "name this start (any `text`, no spaces): repeated with the same key and params, it gives back the same run")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the run, print its id and return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait for the run to end; the run goes on when the time is up")
	r.register(cmd, true)
	a.addOutputFlags(cmd)
	return cmd
}

// formulaRunOutput follows a run that was just started, and prints how it ended and its rows.
func (a *App) formulaRunOutput(ctx context.Context, client *enrichednow.Client, run *enrichednow.FormulaRun, details *enrichednow.FormulaDetails,
	noWait bool, timeout time.Duration, r *rowFlags, format string) error {
	if noWait {
		if format == "json" || format == "jsonl" {
			return a.emit(result{doc: run.RawJSON(), noun: "formula run"})
		}
		fmt.Fprintln(a.Out, run.ID)
		a.note("Formula run %s is %s. Follow it with `enrichednow formulas wait %s`; its rows can be read as it goes: `enrichednow formulas rows %s`.",
			run.ID, run.Status, run.ID, run.ID)
		return nil
	}
	run, err := a.followFormula(ctx, client, run, timeout)
	if err != nil {
		return err
	}
	if err := a.formulaSummary(run); err != nil {
		return err
	}
	src := runRows(client, run.ID, r)
	formulaID := run.FormulaID
	src.formulaID = func(context.Context) string { return formulaID }
	return a.writeRows(ctx, client, src, details, r, format)
}

// followFormula waits for a formula run to end, showing how it is doing.
func (a *App) followFormula(ctx context.Context, client *enrichednow.Client, run *enrichednow.FormulaRun, timeout time.Duration) (*enrichednow.FormulaRun, error) {
	if run.Status.IsFinal() {
		return run, nil
	}
	label := func(r *enrichednow.FormulaRun) string {
		return fmt.Sprintf("Formula run %s %s: %s so far", r.ID, r.Status, plural(r.RowsTotal, "row", "rows"))
	}
	last := label(run)
	progress := a.newProgress(last)
	defer progress.stop()
	ended, err := client.FormulaRuns.Wait(ctx, run.ID, &enrichednow.FormulaWaitParams{Timeout: timeout, PollInterval: a.PollInterval,
		OnPoll: func(r *enrichednow.FormulaRun) {
			if text := label(r); text != last && !r.Status.IsFinal() {
				last = text
				progress.set(text)
			}
		}})
	progress.stop()
	follow := fmt.Sprintf("Follow it with `enrichednow formulas wait %s`, or stop it with `enrichednow formulas cancel %s`.", run.ID, run.ID)
	switch {
	case err == nil:
		return ended, nil
	case ctx.Err() != nil:
		return nil, &cliError{code: "interrupted", msg: "interrupted; formula run " + run.ID + " goes on", exit: exitInterrupted, hint: follow}
	case errors.Is(err, context.DeadlineExceeded):
		return nil, &cliError{code: "timeout", msg: fmt.Sprintf("formula run %s has not ended after %s; it goes on", run.ID, timeout), exit: exitError, hint: follow}
	}
	return nil, err
}

// formulaSummary says on standard error how a formula run ended. A run that did not end ok or partial is an error.
func (a *App) formulaSummary(run *enrichednow.FormulaRun) error {
	if !run.Status.IsSuccess() {
		return formulaRunFailed(run)
	}
	parts := []string{plural(run.RowsTotal, "row", "rows"), plural(run.UnitsSpent, "unit", "units")}
	if d := formulaDuration(run); d > 0 {
		parts = append(parts, fmtDuration(d))
	}
	status := a.stderr.status(string(run.Status))
	if run.Reason != "" {
		status += " (reason: " + run.Reason + ")"
	}
	a.note("Formula run %s ended %s: %s.", run.ID, status, strings.Join(parts, ", "))
	for _, line := range reasonLines(run) {
		a.note("  %s", line)
	}
	if run.Mode == enrichednow.ModePreview {
		a.note("A preview: at most 10 rows and 50 units, and nothing it returned is remembered as delivered.")
	}
	return nil
}

func formulaDuration(run *enrichednow.FormulaRun) time.Duration {
	if run.StartedAt != nil && run.FinishedAt != nil {
		return run.FinishedAt.Sub(*run.StartedAt)
	}
	return 0
}

// reasonLines say what each of a run's reasons means.
func reasonLines(run *enrichednow.FormulaRun) []string {
	var out []string
	for _, reason := range run.Reasons {
		scraper, _ := reason.Fields["scraper"].(string)
		var text string
		switch {
		case reason.Code == "contacts_not_connected":
			text = "the contact columns stay empty until a people provider is connected, in the console"
		case reason.Code == "source_not_offered" && scraper != "":
			text = scraper + " is not offered here; the run went on without it"
		case reason.Code == "unit_cap_reached":
			text = fmt.Sprintf("it stopped at its unit cap of %d units, with what it had; a higher --unit-cap gives more", run.UnitCap)
		case reason.Code == "row_cap_reached":
			text = fmt.Sprintf("it stopped at %d rows, its row cap", run.RowCap)
		case strings.HasPrefix(reason.Code, "source_failed:"):
			text = strings.TrimPrefix(reason.Code, "source_failed:") + " failed; the run went on without it"
		default:
			var extra []string
			for _, name := range []string{"step", "scraper", "source", "reason"} {
				if v, ok := reason.Fields[name].(string); ok && v != "" {
					extra = append(extra, name+" "+v)
				}
			}
			text = strings.Join(extra, ", ")
		}
		if text == "" {
			out = append(out, reason.Code)
		} else {
			out = append(out, reason.Code+": "+text)
		}
	}
	return out
}

// --- one run ----------------------------------------------------------------------------------------------------------------

func (a *App) emitFormulaRun(run *enrichednow.FormulaRun) error {
	return a.emit(result{doc: run.RawJSON(), noun: "formula run", human: func(w io.Writer, st *style, width int) { printFormulaRun(w, st, width, run) }})
}

func printFormulaRun(w io.Writer, st *style, width int, run *enrichednow.FormulaRun) {
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
		{"Formula run", run.ID},
		{"Formula", fmt.Sprintf("%s (version %s)", run.FormulaID, run.Version)},
	}
	if run.SavedFormulaID != "" {
		items = append(items, [2]string{"Saved formula", run.SavedFormulaID})
	}
	mode := run.Mode
	if run.Mode == enrichednow.ModePreview {
		mode += ": at most 10 rows and 50 units"
	}
	items = append(items, [][2]string{
		{"Status", st.status(string(run.Status))},
		{"Reason", dash(run.Reason)},
		{"Mode", mode},
		{"Rows", strconv.Itoa(run.RowsTotal)},
		{"Units", fmt.Sprintf("%d, of a cap of %d", run.UnitsSpent, run.UnitCap)},
		{"Created", fmtTime(run.CreatedAt) + " UTC"},
		{"Started", when(run.StartedAt)},
		{"Finished", when(run.FinishedAt)},
	}...)
	if d := formulaDuration(run); d > 0 {
		items = append(items, [2]string{"Took", fmtDuration(d)})
	}
	if e := run.Estimate; e != nil {
		items = append(items, [2]string{"Estimate", fmt.Sprintf("about %d units, at most %d; about %s", e.ExpectedUnits, e.MaxUnits, plural(e.ExpectedRows, "row", "rows"))})
	}
	items = append(items, [2]string{"Params", cellText(run.Params)})
	pairs(w, st, items)
	if lines := reasonLines(run); len(lines) > 0 {
		fmt.Fprintf(w, "\n%s\n", st.bold("REASONS"))
		for _, line := range lines {
			fmt.Fprintf(w, "  %s\n", line)
		}
	}
	var steps [][]string
	for _, s := range run.Steps {
		if s.Status != "skipped" {
			steps = append(steps, []string{"  " + s.ID, s.Kind, s.Status, strconv.Itoa(s.RecordsIn), strconv.Itoa(s.RecordsOut), strconv.Itoa(s.Calls), strconv.Itoa(s.Units)})
		}
	}
	if len(steps) > 0 {
		fmt.Fprintf(w, "\n%s\n", st.bold("STEPS"))
		t := &table{head: []string{"  STEP", "KIND", "STATUS", "IN", "OUT", "CALLS", "UNITS"}, rows: steps, right: map[int]bool{3: true, 4: true, 5: true, 6: true}}
		if width <= 0 {
			width = defaultWidth
		}
		t.render(w, st, width)
	}
}

func (a *App) cmdFormulasWait() *cobra.Command {
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "wait <run-id>",
		Short: "Wait until a formula run has ended",
		Long: `Wait until a formula run has ended, then print it: its status and reasons, its rows and units, and what each
step did.

The exit code is 0 when the run ended ok or partial, and 3 when it ended failed, unsupported or cancelled.`,
		Example: `  id=$(enrichednow formulas run job-order-hunter --param job_titles=Nurse --param locations=Texas --no-wait)
  enrichednow formulas wait "$id" && enrichednow formulas rows "$id" --out leads.csv`,
		Args: exactArgs("run-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := a.outputFormat(); err != nil {
				return err
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			run, err := client.FormulaRuns.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			if run, err = a.followFormula(cmd.Context(), client, run, timeout); err != nil {
				return err
			}
			if !run.Status.IsSuccess() {
				return formulaRunFailed(run)
			}
			return a.emitFormulaRun(run)
		},
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait; the run goes on when the time is up")
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdFormulasRows() *cobra.Command {
	var r rowFlags
	cmd := &cobra.Command{
		Use:   "rows <run-id>",
		Short: "The rows of a formula run, best first",
		Long: `Print the rows of a formula run, best first. They can be read while the run is still going, and are kept 90
days.

` + rowsHelp,
		Example: `  enrichednow formulas rows frun_000000000000000000000002
  enrichednow formulas rows frun_000000000000000000000002 --fields company_name,job_title,contact_email --min-score 30
  enrichednow formulas rows frun_000000000000000000000002 --view company
  enrichednow formulas rows frun_000000000000000000000002 --out leads.csv
  enrichednow formulas rows frun_000000000000000000000002 -o jsonl | jq -r .data.contact_email`,
		Args: exactArgs("run-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := a.rowsFormat(r.written)
			if err != nil {
				return err
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			return a.writeRows(cmd.Context(), client, runRows(client, args[0], &r), nil, &r, format)
		},
	}
	r.register(cmd, true)
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdFormulasCancel() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <run-id>",
		Short: "Ask a formula run to stop",
		Long: `Ask a formula run to stop, and print it as it is at that moment: usually still running, as the calls in flight
are asked to stop. "enrichednow formulas wait <run-id>" shows how it ended. Rows already written stay, and what was
already done stays charged.`,
		Example: `  enrichednow formulas cancel frun_000000000000000000000002`,
		Args:    exactArgs("run-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			run, err := client.FormulaRuns.Cancel(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: run.RawJSON(), noun: "formula run", human: func(w io.Writer, st *style, _ int) {
				switch {
				case run.Status == enrichednow.RunCancelled:
					fmt.Fprintf(w, "Formula run %s is %s.\n", run.ID, st.status("cancelled"))
				case run.Status.IsFinal():
					fmt.Fprintf(w, "Formula run %s had already ended %s; nothing changed.\n", run.ID, st.status(string(run.Status)))
				default:
					fmt.Fprintf(w, "Formula run %s was asked to stop; it is still %s.\n", run.ID, st.status(string(run.Status)))
					a.note("See how it ends with `enrichednow formulas wait %s`.", run.ID)
				}
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}
