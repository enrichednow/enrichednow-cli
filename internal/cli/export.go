package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"regexp"

	enrichednow "github.com/enrichednow/enrichednow-go"
	"github.com/spf13/cobra"
)

// The formats the API writes records in itself.
var writtenFormats = []string{enrichednow.FormatCSV, enrichednow.FormatNDJSON}

// addFormatFlag gives a command that prints a run's records the flag that asks the API to write them.
func (a *App) addFormatFlag(cmd *cobra.Command, format *string) {
	cmd.Flags().StringVar(format, "format", "", "records as the API writes them: csv or ndjson (instead of --output)")
	_ = cmd.RegisterFlagCompletionFunc("format", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return writtenFormats, cobra.ShellCompDirectiveNoFileComp
	})
}

// checkWritten refuses a --format the API does not write, and --format with --output or --json.
func (a *App) checkWritten(format string) error {
	if format == "" {
		return nil
	}
	if format != enrichednow.FormatCSV && format != enrichednow.FormatNDJSON {
		return &cliError{code: "usage", msg: fmt.Sprintf("--format %q is not csv or ndjson", format),
			hint: "For JSON or a table, use --output.", exit: exitUsage}
	}
	if a.format != "" || a.jsonOut {
		return &cliError{code: "usage", msg: "--format asks the API to write the records; --output and --json format them here: give one of them", exit: exitUsage}
	}
	return nil
}

// writeExport writes every record of a run as the API writes it, to standard output or to the --out file.
func (a *App) writeExport(ctx context.Context, client *enrichednow.Client, runID, format string) error {
	var buf bytes.Buffer
	var w io.Writer = a.Out
	if a.outFile != "" {
		w = &buf
	}
	counted := &lineCounter{w: w}
	if err := client.Runs.ExportAll(ctx, runID, format, counted); err != nil {
		return err
	}
	if a.outFile == "" {
		return nil
	}
	if err := os.WriteFile(a.outFile, buf.Bytes(), 0o644); err != nil {
		return &cliError{code: "error", msg: fmt.Sprintf("cannot write %s: %v", a.outFile, err), exit: exitError}
	}
	records := counted.lines
	if format == enrichednow.FormatCSV && records > 0 {
		records-- // the header row
	}
	a.note("Wrote %s to %s (%s, as the API writes it).", plural(records, "record", "records"), a.outFile, format)
	return nil
}

// lineCounter counts the lines written through it.
type lineCounter struct {
	w     io.Writer
	lines int
}

func (c *lineCounter) Write(p []byte) (int, error) {
	c.lines += bytes.Count(p, []byte{'\n'})
	return c.w.Write(p)
}

var keyPattern = regexp.MustCompile(`^[\x21-\x7e]{1,255}$`)

func validKey(key string) bool { return keyPattern.MatchString(key) }

// newKey is an idempotency key for one start: the tool makes its own, so that it can say it when the start failed.
func newKey() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return ""
	}
	return "cli-" + hex.EncodeToString(b[:])
}

// formulaStart is a text cell a spreadsheet would run as a formula: one that starts with = + - @ (after spaces), or
// with a tab or a carriage return. The API and the console write such a cell with a leading apostrophe; so does this.
var formulaStart = regexp.MustCompile(`^(\s*[=+\-@]|[\t\r])`)

func guardFormula(text string) string {
	if formulaStart.MatchString(text) {
		return "'" + text
	}
	return text
}

// isJSONString says whether a JSON value is a string, which is the only kind of cell the guard applies to: a number
// such as -5 is a number, not a formula.
func isJSONString(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) > 0 && raw[0] == '"'
}
