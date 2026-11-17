package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

const dataUnitsNote = `Every record a data request returns costs one data unit, so --all can use many: narrow the search first.`

func (a *App) cmdData() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "data",
		GroupID: "data",
		Short:   "Read the datasets: companies and their job postings",
		Long: `Read the datasets: companies, and the job postings seen at them over time.

The account must have been given Datasets (early access). A data request is charged one data unit for each
record it returns, against the data allowance — not the one runs draw on.`,
		Example: `  enrichednow data postings list --title "data engineer" --country US --status open
  enrichednow data companies get figma.com
  enrichednow data companies postings figma.com --status open`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	companies := &cobra.Command{
		Use:     "companies",
		Short:   "Companies of the dataset",
		Example: "  enrichednow data companies list --query figma\n  enrichednow data companies get figma.com\n  enrichednow data companies postings figma.com",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	companies.AddCommand(a.cmdCompaniesList(), a.cmdCompaniesGet(), a.cmdCompaniesPostings())
	postings := &cobra.Command{
		Use:     "postings",
		Short:   "Job postings of the dataset",
		Example: "  enrichednow data postings list --title \"data engineer\" --remote\n  enrichednow data postings get job_2a8c2e32f38fa96099cc53b2",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	postings.AddCommand(a.cmdPostingsList(), a.cmdPostingsGet())
	cmd.AddCommand(companies, postings)
	return cmd
}

func (a *App) cmdCompaniesList() *cobra.Command {
	var (
		query, ats, cursor string
		limit              int
		all                bool
	)
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "search"},
		Short:   "List or search companies",
		Long:    "List the companies of the dataset, one page at a time (50 unless --limit says otherwise).\n\n" + dataUnitsNote,
		Example: `  enrichednow data companies list --query figma
  enrichednow data companies list --ats greenhouse --limit 100
  enrichednow data companies list --ats lever --all --out companies.csv`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			doc, rows, next, err := paged("companies", cursor, all, func(cursor string) (json.RawMessage, string, error) {
				page, err := client.Data.Companies.List(cmd.Context(), &enrichednow.CompanyListParams{Query: query, ATS: ats, Limit: limit, Cursor: cursor})
				if err != nil {
					return nil, "", err
				}
				return page.RawJSON(), page.NextCursor, nil
			})
			if err != nil {
				return err
			}
			return a.emit(result{doc: doc, rows: rows, noun: "companies", human: func(w io.Writer, st *style, width int) {
				if len(rows) == 0 {
					a.note("No companies match.")
					return
				}
				t := &table{head: []string{"DOMAIN", "NAME", "ATS", "OPEN", "CLOSED", "LAST SEEN (UTC)"}, right: map[int]bool{3: true, 4: true}}
				for _, c := range decodeRows[enrichednow.Company](rows) {
					t.rows = append(t.rows, []string{c.Domain, deref(c.Name), deref(c.ATS), strconv.Itoa(c.Postings.Open), strconv.Itoa(c.Postings.Closed), fmtTime(c.LastSeenAt)})
				}
				t.render(w, st, width)
				a.more(next)
			}})
		},
	}
	cmd.Flags().StringVarP(&query, "query", "Q", "", "`text` that must appear in the domain or in a name the company was seen under")
	cmd.Flags().StringVar(&ats, "ats", "", "only companies with this applicant tracking `system`, e.g. greenhouse")
	cmd.Flags().IntVar(&limit, "limit", 0, "companies per page: `n` from 1 to 200 (default 50)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "start at this `cursor`, the one a previous page gave")
	cmd.Flags().BoolVar(&all, "all", false, "read every page")
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdCompaniesGet() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get <domain>",
		Aliases: []string{"show"},
		Short:   "One company, by its domain",
		Example: `  enrichednow data companies get figma.com`,
		Args:    exactArgs("domain"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			c, err := client.Data.Companies.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: c.RawJSON(), noun: "company", human: func(w io.Writer, st *style, _ int) {
				var names []string
				for _, n := range c.Names {
					names = append(names, n.Name)
				}
				pairs(w, st, [][2]string{
					{"Domain", c.Domain},
					{"Name", deref(c.Name)},
					{"Also seen as", strings.Join(names, ", ")},
					{"Careers page", deref(c.CareersURL)},
					{"ATS", deref(c.ATS)},
					{"Postings", fmt.Sprintf("%d open, %d closed", c.Postings.Open, c.Postings.Closed)},
					{"First seen", fmtTime(c.FirstSeenAt) + " UTC"},
					{"Last seen", fmtTime(c.LastSeenAt) + " UTC"},
					{"Last read", lastRead(st, c.LastRead)},
				})
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func lastRead(st *style, read enrichednow.LastRead) string {
	if read.At == nil {
		return "never"
	}
	return strings.TrimSpace(fmtTime(*read.At) + " UTC  " + st.status(deref(read.Status)))
}

// postingFlags are the filters the two lists of postings share.
type postingFlags struct {
	company, title, location, country      string
	remote                                 bool
	employmentType, seniority, function    string
	status, source, provenance             string
	unmatched                              bool
	postedFrom, postedTo                   string
	firstSeenFrom, firstSeenTo             string
	lastSeenFrom, lastSeenTo, sort, cursor string
	limit                                  int
	all                                    bool
}

func (f *postingFlags) register(cmd *cobra.Command, withCompany bool) {
	flags := cmd.Flags()
	if withCompany {
		flags.StringVar(&f.company, "company", "", "only postings of the company with this `domain`")
	}
	flags.StringVar(&f.title, "title", "", "`words` that must all appear in the title, in any order")
	flags.StringVar(&f.location, "location", "", "`text` that must appear in the location")
	flags.StringVar(&f.country, "country", "", "a country `code` (US, DE, …) or name")
	flags.BoolVar(&f.remote, "remote", false, "only postings marked remote; --remote=false for those marked not remote")
	flags.StringVar(&f.employmentType, "employment-type", "", "a `type`: full_time, part_time, contract, temporary, internship or volunteer")
	flags.StringVar(&f.seniority, "seniority", "", "a `level`: c_level, vp, director, manager, lead, senior, junior or intern")
	flags.StringVar(&f.function, "function", "", "a job `function`, e.g. engineering, sales, data, design, marketing")
	flags.StringVar(&f.status, "status", "", "a `status`: open, closed or all (default all)")
	flags.StringVar(&f.source, "source", "", "the source's `name`: an ATS such as greenhouse, a job board, or careers_page")
	flags.StringVar(&f.provenance, "provenance", "", "which provenances, as a comma-separated `list` of feed, page, board; or all (default feed)")
	flags.BoolVar(&f.unmatched, "unmatched", false, "also postings not matched to a company")
	flags.StringVar(&f.postedFrom, "posted-from", "", "posted on or after this `day` (YYYY-MM-DD)")
	flags.StringVar(&f.postedTo, "posted-to", "", "posted on or before this `day` (YYYY-MM-DD)")
	flags.StringVar(&f.firstSeenFrom, "first-seen-from", "", "first seen at or after this `time` (a day, or 2026-11-01T12:00:00Z)")
	flags.StringVar(&f.firstSeenTo, "first-seen-to", "", "first seen before this `time`")
	flags.StringVar(&f.lastSeenFrom, "last-seen-from", "", "last seen at or after this `time`")
	flags.StringVar(&f.lastSeenTo, "last-seen-to", "", "last seen before this `time`")
	flags.StringVar(&f.sort, "sort", "", "newest first by this `field`: first_seen_at (the default) or last_seen_at")
	flags.IntVar(&f.limit, "limit", 0, "postings per page: `n` from 1 to 200 (default 50)")
	flags.StringVar(&f.cursor, "cursor", "", "start at this `cursor`, the one a previous page gave with the same filters")
	flags.BoolVar(&f.all, "all", false, "read every page")
	choices := map[string][]string{
		"employment-type": {"full_time", "part_time", "contract", "temporary", "internship", "volunteer"},
		"seniority":       {"c_level", "vp", "director", "manager", "lead", "senior", "junior", "intern"},
		"function": {"data", "sales", "customer_support", "product", "design", "marketing", "finance", "legal", "people", "it",
			"healthcare", "education", "operations", "engineering", "skilled_trades", "retail_hospitality"},
		"status": {"open", "closed", "all"},
		"sort":   {"first_seen_at", "last_seen_at"},
	}
	for name, values := range choices {
		values := values
		_ = cmd.RegisterFlagCompletionFunc(name, func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return values, cobra.ShellCompDirectiveNoFileComp
		})
	}
}

// parseWhen reads a day (its start, UTC) or a full time.
func parseWhen(flag, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, value); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, &cliError{code: "usage", msg: fmt.Sprintf("--%s %q is not a time", flag, value), exit: exitUsage,
		hint: "Write a day (2026-11-01) or a time (2026-11-01T12:00:00Z)."}
}

func (f *postingFlags) params(cmd *cobra.Command) (*enrichednow.PostingListParams, error) {
	p := &enrichednow.PostingListParams{CompanyDomain: f.company, Title: f.title, Location: f.location, Country: f.country,
		EmploymentType: f.employmentType, Seniority: f.seniority, Function: f.function, Status: f.status, Source: f.source,
		Provenance: f.provenance, Unmatched: f.unmatched, PostedFrom: f.postedFrom, PostedTo: f.postedTo, Sort: f.sort, Limit: f.limit}
	if cmd.Flags().Changed("remote") {
		p.Remote = &f.remote
	}
	var err error
	if p.FirstSeenFrom, err = parseWhen("first-seen-from", f.firstSeenFrom); err != nil {
		return nil, err
	}
	if p.FirstSeenTo, err = parseWhen("first-seen-to", f.firstSeenTo); err != nil {
		return nil, err
	}
	if p.LastSeenFrom, err = parseWhen("last-seen-from", f.lastSeenFrom); err != nil {
		return nil, err
	}
	if p.LastSeenTo, err = parseWhen("last-seen-to", f.lastSeenTo); err != nil {
		return nil, err
	}
	return p, nil
}

func (a *App) emitPostings(doc json.RawMessage, rows []json.RawMessage, next string) error {
	return a.emit(result{doc: doc, rows: rows, noun: "postings", human: func(w io.Writer, st *style, width int) {
		if len(rows) == 0 {
			a.note("No postings match.")
			return
		}
		t := &table{head: []string{"ID", "TITLE", "COMPANY", "LOCATION", "STATUS", "POSTED", "FIRST SEEN (UTC)"}, paint: map[int]func(string) string{4: st.status}}
		for _, p := range decodeRows[enrichednow.Posting](rows) {
			company := deref(p.Company.Name)
			if company == "" {
				company = deref(p.Company.Domain)
			}
			t.rows = append(t.rows, []string{p.ID, truncate(p.Title, maxCell), truncate(company, 30), truncate(deref(p.Location.Raw), 30), p.Status, deref(p.PostedAt), fmtTime(p.FirstSeenAt)})
		}
		t.render(w, st, width)
		a.more(next)
	}})
}

func (a *App) cmdPostingsList() *cobra.Command {
	var f postingFlags
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "search"},
		Short:   "Search job postings",
		Long: "Search the job postings of the dataset, newest first, one page at a time (50 unless --limit says otherwise).\n\n" +
			"Only postings read from a source's own feed are returned unless --provenance asks for more.\n\n" + dataUnitsNote,
		Example: `  enrichednow data postings list --title "data engineer" --country US --status open
  enrichednow data postings list --company figma.com --remote
  enrichednow data postings list --function engineering --seniority senior --first-seen-from 2026-11-01
  enrichednow data postings list --source greenhouse --limit 200 --output jsonl > postings.jsonl`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			params, err := f.params(cmd)
			if err != nil {
				return err
			}
			doc, rows, next, err := paged("postings", f.cursor, f.all, func(cursor string) (json.RawMessage, string, error) {
				params.Cursor = cursor
				page, err := client.Data.Postings.List(cmd.Context(), params)
				if err != nil {
					return nil, "", err
				}
				return page.RawJSON(), page.NextCursor, nil
			})
			if err != nil {
				return err
			}
			return a.emitPostings(doc, rows, next)
		},
	}
	f.register(cmd, true)
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdCompaniesPostings() *cobra.Command {
	var f postingFlags
	cmd := &cobra.Command{
		Use:   "postings <domain>",
		Short: "A company's job postings",
		Long:  "List the job postings of one company, newest first, with the same filters as `enrichednow data postings list`.\n\n" + dataUnitsNote,
		Example: `  enrichednow data companies postings figma.com
  enrichednow data companies postings figma.com --status open --title engineer`,
		Args: exactArgs("domain"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			params, err := f.params(cmd)
			if err != nil {
				return err
			}
			doc, rows, next, err := paged("postings", f.cursor, f.all, func(cursor string) (json.RawMessage, string, error) {
				params.Cursor = cursor
				page, err := client.Data.Companies.Postings(cmd.Context(), args[0], params)
				if err != nil {
					return nil, "", err
				}
				return page.RawJSON(), page.NextCursor, nil
			})
			if err != nil {
				return err
			}
			return a.emitPostings(doc, rows, next)
		},
	}
	f.register(cmd, false)
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdPostingsGet() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "get <posting-id>",
		Aliases: []string{"show"},
		Short:   "One posting, with its description and its history",
		Example: `  enrichednow data postings get job_2a8c2e32f38fa96099cc53b2
  enrichednow data postings get job_2a8c2e32f38fa96099cc53b2 --json | jq .history`,
		Args: exactArgs("posting-id"),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(true)
			if err != nil {
				return err
			}
			p, err := client.Data.Postings.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: p.RawJSON(), noun: "posting", human: func(w io.Writer, st *style, width int) {
				remote := ""
				if p.Location.Remote != nil {
					remote = map[bool]string{true: "  (remote)", false: "  (not remote)"}[*p.Location.Remote]
				}
				salary := ""
				if p.Salary != nil {
					salary = deref(p.Salary.Raw)
				}
				items := [][2]string{
					{"Posting", p.ID},
					{"Title", p.Title},
					{"Company", strings.TrimSpace(deref(p.Company.Name) + "  " + deref(p.Company.Domain))},
					{"Location", deref(p.Location.Raw) + remote},
					{"Status", st.status(p.Status)},
					{"Employment", deref(p.EmploymentType)},
					{"Seniority", deref(p.Seniority)},
					{"Function", deref(p.Function)},
					{"Salary", salary},
					{"Posted", deref(p.PostedAt)},
					{"Source", fmt.Sprintf("%s (%s)", p.Source.Name, p.Source.Provenance)},
					{"URL", p.URL},
					{"Apply", deref(p.ApplyURL)},
					{"First seen", fmtTime(p.FirstSeenAt) + " UTC"},
					{"Last seen", fmtTime(p.LastSeenAt) + " UTC"},
				}
				if p.ClosedAt != nil {
					items = append(items, [2]string{"Closed", fmtTime(*p.ClosedAt) + " UTC"})
				}
				var shown [][2]string
				for _, item := range items {
					if strings.TrimSpace(item[1]) != "" {
						shown = append(shown, item)
					}
				}
				pairs(w, st, shown)
				if len(p.History.Events) > 0 {
					fmt.Fprintf(w, "\n%s\n", st.bold("HISTORY"))
					for _, e := range p.History.Events {
						fmt.Fprintf(w, "  %s  %s\n", fmtTime(e.At), e.Kind)
					}
					fmt.Fprintf(w, "  seen %d times, reopened %d times\n", p.History.Sightings.Count, p.History.ReopenedCount)
				}
				if d := deref(p.Description); d != "" {
					if width <= 0 {
						width = 100
					}
					fmt.Fprintf(w, "\n%s\n%s\n", st.bold("DESCRIPTION"), wrap(d, min(width, 100)))
				}
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

// wrap breaks text into lines of at most width characters, keeping its paragraphs.
func wrap(text string, width int) string {
	var out []string
	for _, paragraph := range strings.Split(text, "\n") {
		line := ""
		for _, word := range strings.Fields(paragraph) {
			if line != "" && len(line)+1+len(word) > width {
				out = append(out, line)
				line = ""
			}
			if line != "" {
				line += " "
			}
			line += word
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
