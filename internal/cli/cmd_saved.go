package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

var (
	cadences      = []string{enrichednow.CadenceHourly, enrichednow.CadenceDaily, enrichednow.CadenceWeekdays, enrichednow.CadenceWeekly, enrichednow.CadenceMonthly}
	notifications = []string{enrichednow.NotifyEachRun, enrichednow.NotifyDigest, enrichednow.NotifyNone}
	savedStatuses = []string{enrichednow.SavedFormulaActive, enrichednow.SavedFormulaPaused, enrichednow.SavedFormulaArchived}
	weekdays      = []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"}
)

func (a *App) cmdSaved() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "saved",
		GroupID: "formulas",
		Short:   "Saved formulas: params kept, run again by hand or on a schedule",
		Long: `A saved formula is a formula with its params and caps kept, to run again by hand or on a schedule. Its runs
deliver only rows it did not deliver before, so "enrichednow saved rows <id> --since 24h" is what is new.

A schedule needs a cadence the formula allows ("enrichednow formulas get" lists them) and the plan allows.`,
		Example: `  enrichednow saved create job-order-hunter -p job_titles="Java Developer" -p locations=London --schedule weekdays@07:30 --timezone Europe/London
  enrichednow saved list
  enrichednow saved rows sf_000000000000000000000002 --since 24h --out new-leads.csv`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.cmdSavedList(), a.cmdSavedCreate(), a.cmdSavedRun(), a.cmdSavedPause(), a.cmdSavedResume(), a.cmdSavedRows(), a.cmdSavedDuplicate())
	return cmd
}

// scheduleText is a schedule for a person: "weekdays at 07:30 Europe/London; next run 2026-11-26 07:30:00 UTC".
func scheduleText(s *enrichednow.Schedule) string {
	if s == nil {
		return "none: it runs only by hand"
	}
	when := s.Cadence + " at " + s.LocalTime
	switch {
	case s.Cadence == enrichednow.CadenceHourly && len(s.LocalTime) == 5:
		when = "hourly at :" + s.LocalTime[3:]
	case s.Cadence == enrichednow.CadenceWeekly && s.DayOfWeek != nil && *s.DayOfWeek >= 0 && *s.DayOfWeek < 7:
		day := weekdays[*s.DayOfWeek]
		when = "weekly on " + strings.ToUpper(day[:1]) + day[1:] + " at " + s.LocalTime
	case s.Cadence == enrichednow.CadenceMonthly && s.DayOfMonth != nil:
		when = fmt.Sprintf("monthly on day %d at %s", *s.DayOfMonth, s.LocalTime)
	}
	text := when + " " + s.Timezone
	if s.NextFireAt != nil {
		text += "; next run " + fmtTime(*s.NextFireAt) + " UTC"
	}
	return text
}

func savedPairs(st *style, s *enrichednow.SavedFormula) [][2]string {
	name := s.Name
	if s.NameAuto {
		name += st.dim(" (made from its params)")
	}
	status := s.Status
	switch s.Status {
	case enrichednow.SavedFormulaActive:
		status = st.green(status)
	case enrichednow.SavedFormulaPaused:
		status = st.yellow(status)
		if s.PausedReason != "" {
			status += " (" + s.PausedReason + ")"
		}
	case enrichednow.SavedFormulaArchived:
		status = st.dim(status)
	}
	version := s.Version
	if version == "" {
		version = "no longer offered"
	}
	monthly := "no monthly cap"
	if s.MonthlyUnitCap != nil {
		monthly = fmt.Sprintf("at most %d units a month", *s.MonthlyUnitCap)
	}
	items := [][2]string{
		{"Saved formula", s.ID},
		{"Name", name},
		{"Formula", fmt.Sprintf("%s (version %s)", s.FormulaID, version)},
		{"Status", status},
		{"Schedule", scheduleText(s.Schedule)},
		{"Notify", s.Notify},
		{"Caps", fmt.Sprintf("%d units and %d rows a run; %s", s.UnitCap, s.RowCap, monthly)},
	}
	if e := s.Estimate; e != nil {
		items = append(items, [2]string{"Estimate", fmt.Sprintf("about %d units a run, at most %d", e.ExpectedUnits, e.MaxUnits)})
	}
	if r := s.LastRun; r != nil {
		last := fmt.Sprintf("%s %s, %s, %s UTC", r.ID, st.status(string(r.Status)), plural(r.RowsNew, "new row", "new rows"), fmtTime(r.CreatedAt))
		items = append(items, [2]string{"Last run", last})
	}
	items = append(items, [][2]string{
		{"Units (30 days)", strconv.Itoa(s.Units30d)},
		{"Created", fmtTime(s.CreatedAt) + " UTC"},
	}...)
	if s.ArchivedAt != nil {
		items = append(items, [2]string{"Archived", fmtTime(*s.ArchivedAt) + " UTC"})
	}
	for _, note := range s.Notes {
		items = append(items, [2]string{"Note", note.Message})
	}
	return items
}

func (a *App) emitSaved(s *enrichednow.SavedFormula, then func()) error {
	return a.emit(result{doc: s.RawJSON(), noun: "saved formula", human: func(w io.Writer, st *style, _ int) {
		pairs(w, st, savedPairs(st, s))
		if then != nil {
			then()
		}
	}})
}

func (a *App) cmdSavedList() *cobra.Command {
	var (
		formula, status, cursor string
		limit                   int
		all                     bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Your saved formulas",
		Long:    `List the account's saved formulas: archived ones only with --status archived.`,
		Example: `  enrichednow saved list
  enrichednow saved list --formula job-order-hunter --status paused
  enrichednow saved list --json | jq -r '.saved_formulas[].id'`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			doc, rows, next, err := paged("saved_formulas", cursor, all, func(cursor string) (json.RawMessage, string, error) {
				page, err := client.SavedFormulas.List(cmd.Context(), &enrichednow.SavedFormulaListParams{FormulaID: formula, Status: status, Limit: limit, Cursor: cursor})
				if err != nil {
					return nil, "", err
				}
				return page.RawJSON(), page.NextCursor, nil
			})
			if err != nil {
				return err
			}
			return a.emit(result{doc: doc, rows: rows, noun: "saved formulas", human: func(w io.Writer, st *style, width int) {
				if len(rows) == 0 {
					a.note("No saved formulas. Save one with `enrichednow saved create <formula-id>`.")
					return
				}
				t := &table{head: []string{"ID", "NAME", "FORMULA", "STATUS", "SCHEDULE", "LAST RUN"}}
				for _, s := range decodeRows[enrichednow.SavedFormula](rows) {
					schedule := "by hand"
					if s.Schedule != nil {
						schedule = strings.SplitN(scheduleText(s.Schedule), ";", 2)[0]
					}
					last := ""
					if s.LastRun != nil {
						last = fmt.Sprintf("%s, %s", s.LastRun.Status, plural(s.LastRun.RowsNew, "new row", "new rows"))
					}
					t.rows = append(t.rows, []string{s.ID, s.Name, s.FormulaID, s.Status, schedule, last})
				}
				t.render(w, st, width)
				a.more(next)
			}})
		},
	}
	cmd.Flags().StringVar(&formula, "formula", "", "only saved formulas of this formula `id`")
	cmd.Flags().StringVar(&status, "status", "", "only those with this `status`: "+strings.Join(savedStatuses, ", "))
	cmd.Flags().IntVar(&limit, "limit", 0, "saved formulas per page: `n` from 1 to 100 (default 50)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "start at this `cursor`, the one a previous page gave")
	cmd.Flags().BoolVar(&all, "all", false, "read every page")
	_ = cmd.RegisterFlagCompletionFunc("status", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return savedStatuses, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("formula", func(cmd *cobra.Command, _ []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return a.completeFormulas(cmd, nil, toComplete)
	})
	a.addOutputFlags(cmd)
	return cmd
}

var scheduleTime = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)

// parseSchedule reads --schedule CADENCE@HH:MM, with --timezone and --day.
func parseSchedule(value, timezone, day string) (*enrichednow.ScheduleParams, error) {
	bad := func(msg string) error {
		return &cliError{code: "usage", msg: msg, exit: exitUsage,
			hint: "For example: --schedule weekdays@07:30 --timezone Europe/London. Cadences: " + strings.Join(cadences, ", ") + "."}
	}
	cadence, at, ok := strings.Cut(strings.TrimSpace(value), "@")
	cadence = strings.ToLower(cadence)
	known := false
	for _, c := range cadences {
		known = known || c == cadence
	}
	if !ok || !known {
		return nil, bad(fmt.Sprintf("--schedule %q is not a cadence and a time", value))
	}
	m := scheduleTime.FindStringSubmatch(at)
	hour, _ := strconv.Atoi(firstOf(m, 1))
	minute, _ := strconv.Atoi(firstOf(m, 2))
	if m == nil || hour > 23 || minute > 59 {
		return nil, bad(fmt.Sprintf("--schedule %q: %q is not a time of day (HH:MM, 24-hour)", value, at))
	}
	s := &enrichednow.ScheduleParams{Cadence: cadence, LocalTime: fmt.Sprintf("%02d:%02d", hour, minute), Timezone: timezone}
	if s.Timezone == "" {
		s.Timezone = "UTC"
	}
	if day == "" {
		return s, nil
	}
	switch cadence {
	case enrichednow.CadenceWeekly:
		for i, name := range weekdays {
			if strings.EqualFold(day, name) || strings.EqualFold(day, name[:3]) {
				s.DayOfWeek = enrichednow.Ptr(i)
			}
		}
		if s.DayOfWeek == nil {
			return nil, bad(fmt.Sprintf("--day %q is not a day of the week (monday … sunday)", day))
		}
	case enrichednow.CadenceMonthly:
		n, err := strconv.Atoi(day)
		if err != nil || n < 1 || n > 31 {
			return nil, bad(fmt.Sprintf("--day %q is not a day of the month (1 to 31)", day))
		}
		s.DayOfMonth = enrichednow.Ptr(n)
	default:
		return nil, bad("--day is for a weekly or a monthly schedule")
	}
	return s, nil
}

func firstOf(m []string, i int) string {
	if i < len(m) {
		return m[i]
	}
	return ""
}

func (a *App) cmdSavedCreate() *cobra.Command {
	var (
		p                               paramFlags
		name, schedule, timezone, day   string
		notify                          string
		unitCap, rowCap, monthlyUnitCap int
	)
	cmd := &cobra.Command{
		Use:   "create <formula-id>",
		Short: "Save a formula with its params, to run by hand or on a schedule",
		Long: `Save a formula with its params and caps.

` + paramsHelp + `

--schedule is a cadence and a time of day: hourly, daily, weekdays, weekly or monthly, then @ and HH:MM (24-hour),
in --timezone (an IANA time zone such as Europe/London; default UTC). For hourly only the minute counts. A weekly
schedule runs on --day (monday … sunday, default monday), a monthly one on --day of the month (default 1). The
formula and the plan say which cadences it may have.

Scheduled runs send mail about new rows: --notify each_run (the default), digest (once a day) or none.`,
		Example: `  # Every weekday at 07:30 London time, new rows in a daily mail
  enrichednow saved create job-order-hunter -p job_titles="Java Developer" -p locations=London --schedule weekdays@07:30 --timezone Europe/London --notify digest

  # Every Monday, with a name and at most 20000 units a month
  enrichednow saved create job-order-hunter -p job_titles=Nurse -p locations=Texas --schedule weekly@08:00 --day mon --timezone America/Chicago --monthly-unit-cap 20000 --name "Nurses in Texas"

  # Only by hand
  enrichednow saved create job-order-hunter --params-json '{"job_titles": ["Nurse"], "locations": ["Texas"]}'`,
		Args:              exactArgs("formula-id"),
		ValidArgsFunction: a.completeFormulas,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, formulaID := cmd.Context(), args[0]
			if _, err := a.outputFormat(); err != nil {
				return err
			}
			if err := checkCaps(unitCap, rowCap, monthlyUnitCap); err != nil {
				return err
			}
			opts := &enrichednow.SavedFormulaCreateParams{Name: name, UnitCap: unitCap, RowCap: rowCap, MonthlyUnitCap: monthlyUnitCap, Notify: notify}
			if schedule != "" {
				s, err := parseSchedule(schedule, timezone, day)
				if err != nil {
					return err
				}
				opts.Schedule = s
			} else if timezone != "" || day != "" {
				return &cliError{code: "usage", msg: "--timezone and --day go with --schedule", exit: exitUsage}
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			params, _, err := a.buildParams(ctx, client, formulaID, p)
			if err != nil {
				return err
			}
			saved, err := client.SavedFormulas.Create(ctx, formulaID, params, opts)
			if err != nil {
				return err
			}
			return a.emitSaved(saved, func() {
				a.note("Run it now with `enrichednow saved run %s`; read what it delivers with `enrichednow saved rows %s`.", saved.ID, saved.ID)
			})
		},
	}
	p.register(cmd)
	cmd.Flags().StringVar(&name, "name", "", "its `name` (default: one made from its params, which follows them)")
	cmd.Flags().StringVar(&schedule, "schedule", "", "when it runs: `cadence@HH:MM`, e.g. weekdays@07:30 (default: only by hand)")
	cmd.Flags().StringVar(&timezone, "timezone", "", "the IANA time `zone` of --schedule, e.g. Europe/London (default UTC)")
	cmd.Flags().StringVar(&day, "day", "", "the `day` of a weekly schedule (monday … sunday) or of a monthly one (1 to 31)")
	cmd.Flags().IntVar(&unitCap, "unit-cap", 0, "the most one run may spend, in `units` (default: the formula's)")
	cmd.Flags().IntVar(&rowCap, "row-cap", 0, "the most `rows` one run may produce (default: the formula's)")
	cmd.Flags().IntVar(&monthlyUnitCap, "monthly-unit-cap", 0, "the most its runs may spend in a calendar month, in `units` (default: no cap)")
	cmd.Flags().StringVar(&notify, "notify", "", "mail about new rows: `each_run` (the default), digest or none")
	_ = cmd.RegisterFlagCompletionFunc("notify", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return notifications, cobra.ShellCompDirectiveNoFileComp
	})
	_ = cmd.RegisterFlagCompletionFunc("schedule", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		var out []string
		for _, c := range cadences {
			out = append(out, c+"@")
		}
		return out, cobra.ShellCompDirectiveNoFileComp | cobra.ShellCompDirectiveNoSpace
	})
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdSavedRun() *cobra.Command {
	var (
		r                            rowFlags
		preview, includeSeen, noWait bool
		timeout                      time.Duration
		key                          string
	)
	cmd := &cobra.Command{
		Use:   "run <saved-formula-id>",
		Short: "Run a saved formula now, wait for it and print what it delivered",
		Long: `Run a saved formula now, with its params and caps, wait until the run has ended and print the rows it delivered:
only rows it had not delivered before, unless --include-seen. A paused saved formula can still be run by hand.

` + rowsHelp,
		Example: `  enrichednow saved run sf_000000000000000000000002
  enrichednow saved run sf_000000000000000000000002 --include-seen --out all-leads.csv
  enrichednow saved run sf_000000000000000000000002 --no-wait`,
		Args: exactArgs("saved-formula-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			format, err := a.rowsFormat(r.written)
			if err != nil {
				return err
			}
			if key != "" && !validKey(key) {
				return &cliError{code: "usage", msg: "--idempotency-key must be 1 to 255 visible characters, without spaces", exit: exitUsage}
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			progress := a.newProgress("Starting " + args[0])
			if key == "" {
				key = newKey()
			}
			a.runKey = key
			opts := &enrichednow.SavedFormulaRunParams{Preview: preview, IncludeSeen: includeSeen, Wait: firstWait, IdempotencyKey: key}
			if noWait {
				opts.Wait = 0
			}
			run, err := client.SavedFormulas.Run(ctx, args[0], opts)
			progress.stop()
			if err != nil {
				return err
			}
			return a.formulaRunOutput(ctx, client, run, nil, noWait, timeout, &r, format)
		},
	}
	cmd.Flags().BoolVar(&preview, "preview", false, "a preview: at most 10 rows and 50 units, nothing remembered as delivered")
	cmd.Flags().BoolVar(&includeSeen, "include-seen", false, "deliver rows already delivered too")
	cmd.Flags().StringVar(&key, "idempotency-key", "", "name this start (any `text`, no spaces): repeated with the same key, it gives back the same run")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the run, print its id and return")
	cmd.Flags().DurationVar(&timeout, "timeout", 10*time.Minute, "how long to wait for the run to end; the run goes on when the time is up")
	r.register(cmd, false)
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdSavedPause() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "pause <saved-formula-id>",
		Short:   "Pause a saved formula's schedule",
		Long:    `Pause a saved formula's schedule. It can still be run by hand; "enrichednow saved resume" starts the schedule again.`,
		Example: `  enrichednow saved pause sf_000000000000000000000002`,
		Args:    exactArgs("saved-formula-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			saved, err := client.SavedFormulas.Pause(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emitSaved(saved, func() { a.note("Its schedule is paused. Start it again with `enrichednow saved resume %s`.", saved.ID) })
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdSavedResume() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "resume <saved-formula-id>",
		Short:   "Start a paused saved formula's schedule again",
		Example: `  enrichednow saved resume sf_000000000000000000000002`,
		Args:    exactArgs("saved-formula-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			saved, err := client.SavedFormulas.Resume(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emitSaved(saved, nil)
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

var durationAgo = regexp.MustCompile(`^(\d+)d$`)

// parseSince reads --since: a time, a day, or how long ago (24h, 90m, 7d).
func parseSince(value string, now time.Time) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	if m := durationAgo.FindStringSubmatch(value); m != nil {
		days, _ := strconv.Atoi(m[1])
		return now.AddDate(0, 0, -days), nil
	}
	if d, err := time.ParseDuration(value); err == nil && d > 0 {
		return now.Add(-d), nil
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, &cliError{code: "usage", msg: fmt.Sprintf("--since %q is not a time", value), exit: exitUsage,
		hint: "Write a time (2026-11-25T00:00:00Z), a day (2026-11-25), or how long ago (24h, 7d)."}
}

func (a *App) cmdSavedRows() *cobra.Command {
	var (
		r            rowFlags
		since, order string
	)
	cmd := &cobra.Command{
		Use:   "rows <saved-formula-id>",
		Short: "The rows a saved formula delivered, across its runs",
		Long: `Print the rows a saved formula delivered, across its runs, in the order they were written (--order desc: newest
first). --since keeps the rows first seen from then on: a time, a day, or how long ago (24h, 7d). Rows the
suppression list now holds are left out, and so are a preview's.

` + rowsHelp,
		Example: `  enrichednow saved rows sf_000000000000000000000002 --since 24h
  enrichednow saved rows sf_000000000000000000000002 --since 2026-11-25T00:00:00Z --out new-leads.csv
  enrichednow saved rows sf_000000000000000000000002 --fields company_name,contact_email --format ndjson`,
		Args: exactArgs("saved-formula-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := a.rowsFormat(r.written)
			if err != nil {
				return err
			}
			from, err := parseSince(since, time.Now())
			if err != nil {
				return err
			}
			client, err := a.client(true)
			if err != nil {
				return err
			}
			id := args[0]
			src := rowSource{
				page: func(ctx context.Context, limit int, cursor string) (*enrichednow.FormulaRowPage, error) {
					return client.SavedFormulas.RowsPage(ctx, id, &enrichednow.SavedFormulaRowListParams{Since: from, Order: order, Fields: r.fieldList(),
						MinScore: r.min(), Limit: limit, Cursor: cursor})
				},
				exportAll: func(ctx context.Context, format string, w io.Writer) error {
					return client.SavedFormulas.ExportAll(ctx, id, enrichednow.SavedFormulaExportParams{Format: format, Since: from, Order: order,
						Fields: r.fieldList(), MinScore: r.min()}, w)
				},
				formulaID: func(ctx context.Context) string {
					if saved, err := client.SavedFormulas.Get(ctx, id); err == nil {
						return saved.FormulaID
					}
					return ""
				},
			}
			return a.writeRows(cmd.Context(), client, src, nil, &r, format)
		},
	}
	cmd.Flags().StringVar(&since, "since", "", "only rows first seen from this `time` on: 2026-11-25T00:00:00Z, 2026-11-25, 24h or 7d")
	cmd.Flags().StringVar(&order, "order", "", "asc (the default: in the order they were written) or desc (newest first)")
	_ = cmd.RegisterFlagCompletionFunc("order", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"asc", "desc"}, cobra.ShellCompDirectiveNoFileComp
	})
	r.register(cmd, false)
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdSavedDuplicate() *cobra.Command {
	var (
		name     string
		copySeen bool
	)
	cmd := &cobra.Command{
		Use:   "duplicate <saved-formula-id>",
		Short: "Make a copy of a saved formula, archived or not",
		Long: `Make a copy of a saved formula: the same formula, params, caps and notices, with no schedule. An archived saved
formula cannot be changed or run, but it can be copied. The copy starts with nothing delivered unless --copy-seen.`,
		Example: `  enrichednow saved duplicate sf_000000000000000000000002 --name "Java in London (copy)" --copy-seen`,
		Args:    exactArgs("saved-formula-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			copied, err := client.SavedFormulas.Duplicate(cmd.Context(), args[0], &enrichednow.SavedFormulaDuplicateParams{Name: name, CopySeen: copySeen})
			if err != nil {
				return err
			}
			return a.emitSaved(copied, nil)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "the copy's `name` (default: one made from its params, or the original's with (copy))")
	cmd.Flags().BoolVar(&copySeen, "copy-seen", false, "start with what the original delivered, so the copy does not deliver it again")
	a.addOutputFlags(cmd)
	return cmd
}
