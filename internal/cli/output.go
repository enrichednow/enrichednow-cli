package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"
)

var formats = []string{"table", "json", "jsonl", "csv"}

// style colours text, or leaves it alone when colours are off.
type style struct{ on bool }

func (s *style) wrap(code, text string) string {
	if s == nil || !s.on || text == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}
func (s *style) bold(t string) string   { return s.wrap("1", t) }
func (s *style) dim(t string) string    { return s.wrap("2", t) }
func (s *style) red(t string) string    { return s.wrap("31", t) }
func (s *style) green(t string) string  { return s.wrap("32", t) }
func (s *style) yellow(t string) string { return s.wrap("33", t) }
func (s *style) cyan(t string) string   { return s.wrap("36", t) }

// status colours a run or posting status.
func (s *style) status(text string) string {
	switch strings.TrimSpace(text) {
	case "ok", "open", "complete":
		return s.green(text)
	case "partial", "incomplete", "cancelled":
		return s.yellow(text)
	case "failed", "unsupported":
		return s.red(text)
	case "queued", "running":
		return s.cyan(text)
	}
	return text
}

// table is rows of text under headings, printed in aligned columns.
type table struct {
	head  []string
	rows  [][]string
	paint map[int]func(string) string // by column
	right map[int]bool                // right-aligned columns
}

const minColumn = 12

// render prints the table. With maxWidth above zero, the widest columns are cut down until the table fits.
func (t *table) render(w io.Writer, st *style, maxWidth int) {
	widths := make([]int, len(t.head))
	for i, h := range t.head {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); i < len(widths) && n > widths[i] {
				widths[i] = n
			}
		}
	}
	if maxWidth > 0 {
		total := func() int {
			sum := 2 * (len(widths) - 1)
			for _, width := range widths {
				sum += width
			}
			return sum
		}
		for total() > maxWidth {
			widest := 0
			for i, width := range widths {
				if width > widths[widest] {
					widest = i
				}
			}
			if widths[widest] <= minColumn {
				break
			}
			widths[widest] -= min(total()-maxWidth, widths[widest]-minColumn)
		}
	}
	line := func(cells []string, paint func(int, string) string) {
		var b strings.Builder
		for i := range t.head {
			cell := ""
			if i < len(cells) {
				cell = truncate(cells[i], widths[i])
			}
			pad := strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell))
			last := i == len(t.head)-1
			switch {
			case t.right[i]:
				b.WriteString(pad + paint(i, cell))
			case last:
				b.WriteString(paint(i, cell))
			default:
				b.WriteString(paint(i, cell) + pad)
			}
			if !last {
				b.WriteString("  ")
			}
		}
		fmt.Fprintln(w, strings.TrimRight(b.String(), " "))
	}
	line(t.head, func(_ int, s string) string { return st.bold(s) })
	for _, row := range t.rows {
		line(row, func(i int, s string) string {
			if p := t.paint[i]; p != nil {
				return p(s)
			}
			return s
		})
	}
}

// truncate cuts text to width characters, ending in an ellipsis when something was cut.
func truncate(text string, width int) string {
	if utf8.RuneCountInString(text) <= width {
		return text
	}
	if width <= 1 {
		return "…"
	}
	runes := []rune(text)
	return string(runes[:width-1]) + "…"
}

// pairs prints "Label  value" lines with the values aligned.
func pairs(w io.Writer, st *style, items [][2]string) {
	width := 0
	for _, item := range items {
		if n := utf8.RuneCountInString(item[0]); n > width {
			width = n
		}
	}
	for _, item := range items {
		fmt.Fprintf(w, "%s%s  %s\n", st.bold(item[0]), strings.Repeat(" ", width-utf8.RuneCountInString(item[0])), item[1])
	}
}

// --- JSON that keeps its order -------------------------------------------------------------------------------------

// field is one member of a JSON object.
type field struct {
	Key   string
	Value json.RawMessage
}

// objectFields returns the members of a JSON object in the order they were written; ok is false when raw is
// not an object.
func objectFields(raw json.RawMessage) (fields []field, ok bool) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, false
	}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, false
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, false
		}
		fields = append(fields, field{Key: key.(string), Value: value})
	}
	return fields, true
}

// cellText is a JSON value as one cell of a table or a CSV file: a string without its quotes, null as nothing,
// an object or a list as compact JSON.
func cellText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	var compact bytes.Buffer
	if json.Compact(&compact, raw) == nil {
		return compact.String()
	}
	return string(raw)
}

// flatten spreads an object over columns: a nested object becomes parent.child columns; a list stays one cell.
func flatten(prefix string, raw json.RawMessage, out *[]field) {
	fields, ok := objectFields(raw)
	if !ok {
		*out = append(*out, field{Key: prefix, Value: raw})
		return
	}
	for _, f := range fields {
		key := f.Key
		if prefix != "" {
			key = prefix + "." + f.Key
		}
		flatten(key, f.Value, out)
	}
}

// listOf returns the list under key in a JSON object, each item as it was written.
func listOf(doc json.RawMessage, key string) []json.RawMessage {
	fields, _ := objectFields(doc)
	for _, f := range fields {
		if f.Key == key {
			var items []json.RawMessage
			_ = json.Unmarshal(f.Value, &items)
			return items
		}
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

func decodeRows[T any](rows []json.RawMessage) []T {
	out := make([]T, len(rows))
	for i, row := range rows {
		_ = json.Unmarshal(row, &out[i])
	}
	return out
}

// --- writing a result ---------------------------------------------------------------------------------------------

// result is what a command has to show.
type result struct {
	// doc is what --output json prints.
	doc json.RawMessage
	// rows are what jsonl and csv print, one per line. Nil means: the doc itself, as one row.
	rows []json.RawMessage
	// human prints the table view. Nil means a table of the rows.
	human func(w io.Writer, st *style, width int)
	// noun says what the rows are, for "Wrote 21 records to jobs.csv".
	noun string
}

func (a *App) outputFormat() (string, error) {
	format := strings.ToLower(a.format)
	if a.jsonOut {
		if format != "" && format != "json" {
			return "", &cliError{code: "usage", msg: "--json and --output " + format + " ask for two different formats", exit: exitUsage}
		}
		format = "json"
	}
	if format == "" && a.outFile != "" {
		switch strings.ToLower(filepath.Ext(a.outFile)) {
		case ".json":
			format = "json"
		case ".csv":
			format = "csv"
		case ".txt":
			format = "table"
		default:
			format = "jsonl"
		}
	}
	if format == "" {
		format = "table"
	}
	for _, known := range formats {
		if format == known {
			return format, nil
		}
	}
	return "", &cliError{code: "usage", msg: fmt.Sprintf("--output %q is not a format", a.format), hint: "Formats: " + strings.Join(formats, ", ") + ".", exit: exitUsage}
}

// emit writes a result in the format the flags ask for, to standard output or to the --out file.
func (a *App) emit(r result) error {
	format, err := a.outputFormat()
	if err != nil {
		return err
	}
	rows := r.rows
	if rows == nil {
		rows = []json.RawMessage{r.doc}
	}
	var buf bytes.Buffer
	st, width := a.stdout, a.Width
	if a.outFile != "" {
		st, width = &style{}, 0
	}
	switch format {
	case "json":
		if err := json.Indent(&buf, r.doc, "", "  "); err != nil {
			return err
		}
		buf.WriteByte('\n')
	case "jsonl":
		for _, row := range rows {
			if err := json.Compact(&buf, row); err != nil {
				return err
			}
			buf.WriteByte('\n')
		}
	case "csv":
		if err := writeCSV(&buf, rows); err != nil {
			return err
		}
	default:
		if r.human != nil {
			r.human(&buf, st, width)
		} else {
			a.genericTable(&buf, st, width, rows)
		}
	}
	if a.outFile == "" {
		_, err := a.Out.Write(buf.Bytes())
		return err
	}
	if err := os.WriteFile(a.outFile, buf.Bytes(), 0o644); err != nil {
		return &cliError{code: "error", msg: fmt.Sprintf("cannot write %s: %v", a.outFile, err), exit: exitError}
	}
	noun := r.noun
	if noun == "" {
		noun = "rows"
	}
	a.note("Wrote %d %s to %s (%s).", len(rows), noun, a.outFile, format)
	return nil
}

// writeCSV writes rows as CSV: one column per field, nested objects as parent.child, in the order first seen. A
// text cell that a spreadsheet would run as a formula starts with an apostrophe, as the API and the console write it.
func writeCSV(w io.Writer, rows []json.RawMessage) error {
	var header []string
	seen := map[string]int{}
	flat := make([][]field, len(rows))
	for i, row := range rows {
		flatten("", row, &flat[i])
		for _, f := range flat[i] {
			key := f.Key
			if key == "" {
				key = "value"
			}
			if _, ok := seen[key]; !ok {
				seen[key] = len(header)
				header = append(header, key)
			}
		}
	}
	// A field that is an object in one row and null in another has parent.child columns already: leave out the
	// empty column of the parent itself.
	filled := map[string]bool{}
	for _, fields := range flat {
		for _, f := range fields {
			if cellText(f.Value) != "" {
				filled[f.Key] = true
			}
		}
	}
	var columns []string
	position := map[string]int{}
	for _, key := range header {
		parent := false
		for _, other := range header {
			parent = parent || strings.HasPrefix(other, key+".")
		}
		if parent && !filled[key] {
			continue
		}
		position[key] = len(columns)
		columns = append(columns, key)
	}
	out := csv.NewWriter(w)
	out.UseCRLF = true // as the API and the console write CSV
	if len(columns) > 0 {
		if err := out.Write(columns); err != nil {
			return err
		}
	}
	for _, fields := range flat {
		line := make([]string, len(columns))
		for _, f := range fields {
			key := f.Key
			if key == "" {
				key = "value"
			}
			if i, ok := position[key]; ok {
				line[i] = cellText(f.Value)
				if isJSONString(f.Value) {
					line[i] = guardFormula(line[i])
				}
			}
		}
		if err := out.Write(line); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}

const (
	maxCell      = 48
	defaultWidth = 160
)

// genericTable prints rows whose shape is not known ahead (a scraper's records): one column per top-level
// field, as many as fit, skipping the fields no row has a value for.
func (a *App) genericTable(w io.Writer, st *style, width int, rows []json.RawMessage) {
	if len(rows) == 0 {
		return
	}
	if width <= 0 {
		width = defaultWidth
	}
	var keys []string
	index := map[string]int{}
	hasValue := map[string]bool{}
	parsed := make([][]field, len(rows))
	for i, row := range rows {
		fields, ok := objectFields(row)
		if !ok {
			fields = []field{{Key: "value", Value: row}}
		}
		parsed[i] = fields
		for _, f := range fields {
			if _, ok := index[f.Key]; !ok {
				index[f.Key] = len(keys)
				keys = append(keys, f.Key)
			}
			if tableCell(f.Value) != "" {
				hasValue[f.Key] = true
			}
		}
	}
	var shown []string
	used := 0
	for _, key := range keys {
		if !hasValue[key] {
			continue
		}
		longest := utf8.RuneCountInString(key)
		for _, fields := range parsed {
			for _, f := range fields {
				if f.Key == key {
					longest = max(longest, min(maxCell, utf8.RuneCountInString(tableCell(f.Value))))
				}
			}
		}
		if len(shown) > 0 && used+longest+2 > width {
			break
		}
		used += longest + 2
		shown = append(shown, key)
	}
	t := &table{}
	column := map[string]int{}
	for i, key := range shown {
		column[key] = i
		t.head = append(t.head, strings.ToUpper(key))
	}
	for _, fields := range parsed {
		line := make([]string, len(shown))
		for _, f := range fields {
			if i, ok := column[f.Key]; ok {
				line[i] = truncate(tableCell(f.Value), maxCell)
			}
		}
		t.rows = append(t.rows, line)
	}
	t.render(w, st, width)
	if hidden := len(keys) - len(shown); hidden > 0 {
		a.note("%d more fields are not shown in the table; use --output json, jsonl or csv for all of them.", hidden)
	}
}

// tableCell is a JSON value in a table of records. An object that carries its own text under "raw" (a location,
// a salary) is shown as that text; json, jsonl and csv have all of it.
func tableCell(raw json.RawMessage) string {
	if fields, ok := objectFields(raw); ok {
		for _, f := range fields {
			if f.Key == "raw" {
				if text := cellText(f.Value); text != "" && bytes.HasPrefix(bytes.TrimSpace(f.Value), []byte(`"`)) {
					return oneLine(text)
				}
			}
		}
	}
	return oneLine(cellText(raw))
}

func oneLine(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// --- small formatters ------------------------------------------------------------------------------------------------

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return ""
	}
	return fmtTime(*t)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// fmtDuration is a duration for a person: 885ms, 3.2s, 2m05s.
func fmtDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
}
