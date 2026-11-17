package cli

import (
	"fmt"
	"io"
	"strings"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

func (a *App) cmdScrapers() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "scrapers",
		GroupID: "core",
		Short:   "The catalogue: what can be run, what it takes and what it returns",
		Example: `  enrichednow scrapers list
  enrichednow scrapers show ats.board`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}
	cmd.AddCommand(a.cmdScrapersList(), a.cmdScrapersShow())
	return cmd
}

func billing(unit string) string {
	switch unit {
	case "request":
		return "1 unit a run"
	case "record":
		return "1 unit a record"
	}
	return unit
}

func (a *App) cmdScrapersList() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "Every scraper, with what a run of it costs",
		Long: `List the catalogue. No API key is needed.

A run is charged only when it ends ok or partial: 1 unit for the run, or 1 unit for each record, depending on
the scraper.`,
		Example: `  enrichednow scrapers list
  enrichednow scrapers list --json | jq -r '.scrapers[].id'`,
		Args: exactArgs(),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.client(false)
			if err != nil {
				return err
			}
			scrapers, err := client.Scrapers.List(cmd.Context())
			if err != nil {
				return err
			}
			if scrapers == nil {
				scrapers = []enrichednow.Scraper{}
			}
			doc := mustJSON(map[string]any{"scrapers": scrapers})
			return a.emit(result{doc: doc, rows: listOf(doc, "scrapers"), noun: "scrapers", human: func(w io.Writer, st *style, width int) {
				t := &table{head: []string{"ID", "TITLE", "CHARGED", "SUMMARY"}, paint: map[int]func(string) string{0: st.bold}}
				for _, s := range scrapers {
					t.rows = append(t.rows, []string{s.ID, s.Title, billing(s.BillingUnit), s.Summary})
				}
				if width <= 0 {
					width = defaultWidth
				}
				t.render(w, st, width)
				a.note("See what one takes and returns: enrichednow scrapers show <id>")
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

func (a *App) cmdScrapersShow() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "show <scraper-id>",
		Aliases: []string{"get"},
		Short:   "One scraper: its inputs, the fields of its records and how it is charged",
		Long: `Show one scraper: the input fields it takes (required or optional, with their defaults and limits), the
fields of the records it returns, and what a run costs. No API key is needed.

With --json the answer is the API's: the scraper with the JSON Schema of its input and of one record.`,
		Example: `  enrichednow scrapers show careers.company
  enrichednow scrapers show ats.board --json | jq .input_schema`,
		Args:              exactArgs("scraper-id"),
		ValidArgsFunction: a.completeScrapers,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.client(false)
			if err != nil {
				return err
			}
			s, err := client.Scrapers.Get(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return a.emit(result{doc: s.RawJSON(), noun: "scraper", human: func(w io.Writer, st *style, width int) {
				if width <= 0 {
					width = defaultWidth
				}
				fmt.Fprintf(w, "%s  %s\n%s\n\n", st.bold(s.ID), s.Title, s.Summary)
				pairs(w, st, [][2]string{
					{"Version", fmt.Sprint(s.Version)},
					{"Charged", billing(s.BillingUnit) + ", when the run ends ok or partial"},
					{"Mode", s.Mode},
				})

				inputs := schemaFields(s.InputSchema)
				fmt.Fprintf(w, "\n%s\n", st.bold("INPUT"))
				if len(inputs) == 0 {
					fmt.Fprintln(w, "  (none)")
				} else {
					t := &table{head: []string{"  FIELD", "TYPE", "REQUIRED", "DEFAULT", "DESCRIPTION"}}
					for _, f := range inputs {
						required := "optional"
						if f.Required {
							required = "required"
						}
						t.rows = append(t.rows, []string{"  " + f.Name, f.Type, required, f.Default, f.Description})
					}
					t.render(w, st, width)
				}

				fields := schemaFields(s.RecordSchema)
				fmt.Fprintf(w, "\n%s\n", st.bold("RECORD FIELDS"))
				if len(fields) == 0 {
					fmt.Fprintln(w, "  (see --json for the record schema)")
				} else {
					t := &table{head: []string{"  FIELD", "TYPE"}}
					described := false
					for _, f := range fields {
						described = described || f.Description != ""
					}
					if described {
						t.head = append(t.head, "DESCRIPTION")
					}
					for _, f := range fields {
						t.rows = append(t.rows, []string{"  " + f.Name, f.Type, f.Description})
					}
					t.render(w, st, width)
					fmt.Fprintln(w, st.dim("  A type that ends in ? can be null: the value is not known."))
				}

				fmt.Fprintf(w, "\n%s\n  %s\n", st.bold("RUN IT"), exampleRun(s.ID, inputs))
			}})
		},
	}
	a.addOutputFlags(cmd)
	return cmd
}

// exampleRun is a command line that runs the scraper, with a placeholder for every required field (or for the
// first field, when none is required).
func exampleRun(id string, inputs []schemaField) string {
	var b strings.Builder
	b.WriteString("enrichednow run " + id)
	shown := 0
	for _, f := range inputs {
		if f.Required {
			fmt.Fprintf(&b, " --input %s=<%s>", f.Name, f.Name)
			shown++
		}
	}
	if shown == 0 && len(inputs) > 0 {
		fmt.Fprintf(&b, " --input %s=<%s>", inputs[0].Name, inputs[0].Name)
	}
	return b.String()
}
