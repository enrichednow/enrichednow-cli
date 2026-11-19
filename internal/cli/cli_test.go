package cli

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/enrichednow/enrichednow-cli/internal/fakeapi"
	"github.com/spf13/cobra"
)

// harness runs the tool in this process against a fake API.
type harness struct {
	t      *testing.T
	api    *fakeapi.Server
	config string
	stdin  string
	tty    bool
	width  int
	ctx    context.Context
}

// newHarness starts a fake API and points the tool at it, with a key in the environment and an empty
// configuration directory.
func newHarness(t *testing.T) *harness {
	t.Helper()
	api := fakeapi.New()
	t.Cleanup(api.Close)
	h := &harness{t: t, api: api, config: filepath.Join(t.TempDir(), "enrichednow"), ctx: context.Background()}
	t.Setenv(envAPIURL, api.URL)
	t.Setenv(envConfigDir, h.config)
	t.Setenv(envAPIKey, fakeapi.Key)
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm")
	return h
}

// run runs one command line and returns what it printed and its exit code. Whatever the command, the key must
// not appear in its output.
func (h *harness) run(args ...string) (stdout, stderr string, code int) {
	h.t.Helper()
	var out, errs bytes.Buffer
	app := &App{In: strings.NewReader(h.stdin), Out: &out, Err: &errs, Build: BuildInfo{Version: "1.2.3"},
		OutTTY: h.tty, ErrTTY: false, Width: h.width, PollInterval: time.Millisecond}
	code = app.Run(h.ctx, args)
	for _, text := range []string{out.String(), errs.String()} {
		for _, key := range []string{fakeapi.Key, fakeapi.KeyWithoutData} {
			if strings.Contains(text, key) {
				h.t.Errorf("`enrichednow %s` printed the API key", strings.Join(args, " "))
			}
		}
	}
	return out.String(), errs.String(), code
}

// ok runs a command that must succeed.
func (h *harness) ok(args ...string) (stdout, stderr string) {
	h.t.Helper()
	stdout, stderr, code := h.run(args...)
	if code != 0 {
		h.t.Fatalf("`enrichednow %s` exited %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, stdout, stderr)
	}
	return stdout, stderr
}

func contains(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func lacks(t *testing.T, text string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(text, bad) {
			t.Errorf("unexpected %q in:\n%s", bad, text)
		}
	}
}

func lastRequest(t *testing.T, h *harness, method, path string) fakeapi.Request {
	t.Helper()
	requests := h.api.Requests()
	for i := len(requests) - 1; i >= 0; i-- {
		if requests[i].Method == method && requests[i].Path == path {
			return requests[i]
		}
	}
	t.Fatalf("no %s %s was sent; got %+v", method, path, requests)
	return fakeapi.Request{}
}

// --- the command line itself ----------------------------------------------------------------------------------------

func TestVersionAndHelp(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("--version")
	if out != "enrichednow 1.2.3\n" {
		t.Errorf("--version printed %q", out)
	}
	out, _ = h.ok("--help")
	contains(t, out, "run ", "scrapers", "runs", "data", "usage", "login", "logout", "completion", "--json", "--api-url", "--quiet", "--no-color", "Examples:")
	out, _ = h.ok()
	contains(t, out, "Usage:", "enrichednow [command]")
	if n := len(h.api.Requests()); n != 0 {
		t.Errorf("help should not call the API; %d requests", n)
	}
}

// Every command says what it does and shows an example.
func TestEveryCommandHasHelpWithExamples(t *testing.T) {
	root := (&App{}).rootCommand()
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		if cmd.Name() == "help" || cmd.Name() == "completion" || (cmd.HasParent() && cmd.Parent().Name() == "completion") {
			return
		}
		if cmd.Short == "" {
			t.Errorf("`%s` has no short description", cmd.CommandPath())
		}
		if cmd.Example == "" {
			t.Errorf("`%s` has no example", cmd.CommandPath())
		}
		for _, line := range strings.Split(cmd.Example, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.Contains(trimmed, "enrichednow") {
				t.Errorf("`%s`: example line %q does not show a command", cmd.CommandPath(), trimmed)
			}
		}
		for _, sub := range cmd.Commands() {
			walk(sub)
		}
	}
	walk(root)
}

func TestMistakesInTheCommandLineExitWith2(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"nonsense"}, `unknown command "nonsense"`},
		{[]string{"runs", "list", "--nope"}, "unknown flag: --nope"},
		{[]string{"runs", "get"}, "takes <run-id>"},
		{[]string{"run"}, "takes <scraper-id>"},
		{[]string{"runs", "list", "extra"}, "takes no arguments"},
		{[]string{"runs", "list", "--output", "xml"}, `"xml" is not a format`},
		{[]string{"runs", "list", "--json", "--output", "csv"}, "two different formats"},
		{[]string{"run", "ats.board", "--input", "board_url"}, "is not key=value"},
		{[]string{"run", "ats.board", "--input-json", "[1]"}, "is not a JSON object"},
		{[]string{"run", "ats.board", "--input", "board_url=https://x.example/instant", "--input", "max_detail_pages=many"}, "max_detail_pages takes integer"},
		{[]string{"data", "postings", "list", "--first-seen-from", "yesterday"}, "is not a time"},
	}
	for _, tc := range cases {
		out, errs, code := h.run(tc.args...)
		if code != exitUsage {
			t.Errorf("%v: exit %d, want 2", tc.args, code)
		}
		contains(t, errs, tc.want)
		if !strings.HasPrefix(errs, "Error: ") && !strings.HasPrefix(errs, `{"error":`) {
			t.Errorf("%v: the error should lead standard error, got %q", tc.args, errs)
		}
		if out != "" {
			t.Errorf("%v: nothing belongs on standard output, got %q", tc.args, out)
		}
	}
	if n := h.api.Count("POST", "/v1/scrapers/ats.board/runs"); n != 0 {
		t.Errorf("a mistake in the command line must not start a run; %d were started", n)
	}
}

// --- the key ----------------------------------------------------------------------------------------------------------

func TestWithoutAKey(t *testing.T) {
	h := newHarness(t)
	t.Setenv(envAPIKey, "")
	out, errs, code := h.run("runs", "list")
	if code != exitAuth || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	contains(t, errs, "Error: no API key", "enrichednow login", "ENRICHEDNOW_API_KEY")
	if n := len(h.api.Requests()); n != 0 {
		t.Errorf("no request should be sent without a key; %d were", n)
	}
	// The catalogue is public: it works without a key, and no Authorization header is sent.
	out, _ = h.ok("scrapers", "list")
	contains(t, out, "ats.board")
	if auth := lastRequest(t, h, "GET", "/v1/scrapers").Authorization; auth != "" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestAKeyThatIsNotAccepted(t *testing.T) {
	h := newHarness(t)
	t.Setenv(envAPIKey, "en_live_wrongwrongwrongwrongwrongwrongwrong")
	_, errs, code := h.run("runs", "list")
	if code != exitAuth {
		t.Errorf("exit %d, want 4", code)
	}
	contains(t, errs, "Error: This API key is not valid.", "enrichednow login")
	lacks(t, errs, "wrongwrong")
}

func TestKeyPrecedence(t *testing.T) {
	h := newHarness(t)
	t.Setenv(envAPIKey, "en_live_fromtheenvironment0000000000000000")
	h.ok("runs", "list", "--api-key", fakeapi.Key)
	if got := lastRequest(t, h, "GET", "/v1/runs").Authorization; got != "Bearer "+fakeapi.Key {
		t.Error("--api-key should win over the environment")
	}
	if got := lastRequest(t, h, "GET", "/v1/runs").UserAgent; !strings.HasPrefix(got, "enrichednow-cli/1.2.3 enrichednow-go/") {
		t.Errorf("User-Agent = %q", got)
	}
}

func TestLoginAndLogout(t *testing.T) {
	h := newHarness(t)
	t.Setenv(envAPIKey, "")
	path := filepath.Join(h.config, "config.json")

	h.stdin = "not-a-key\n"
	_, errs, code := h.run("login")
	if code != exitUsage {
		t.Errorf("a string that is no key: exit %d, want 2", code)
	}
	contains(t, errs, "does not look like an API key", "en_live_")

	h.stdin = "en_live_revokedrevokedrevokedrevokedrevoked\n"
	_, errs, code = h.run("login")
	if code != exitAuth {
		t.Errorf("a key the API refuses: exit %d, want 4", code)
	}
	contains(t, errs, "did not accept that key", "nothing was saved")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a refused key must not be saved")
	}

	h.stdin = ""
	if _, errs, code = h.run("login"); code != exitUsage {
		t.Errorf("no key at all: exit %d, want 2", code)
	}
	contains(t, errs, "no key was given")

	h.stdin = fakeapi.Key + "\n"
	out, _ := h.ok("login")
	contains(t, out, "Logged in.", "en_live_Zk3uTq8v…", path)
	if req := lastRequest(t, h, "GET", "/v1/runs"); req.Query != "limit=1" || req.Authorization != "Bearer "+fakeapi.Key {
		t.Errorf("the key should be checked with one cheap call, got %+v", req.Query)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved map[string]string
	if json.Unmarshal(data, &saved) != nil || saved["api_key"] != fakeapi.Key {
		t.Errorf("the configuration file holds %s", data)
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(path)
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("the configuration file has mode %o, want 600", mode)
		}
		dir, _ := os.Stat(h.config)
		if dir.Mode().Perm()&0o077 != 0 {
			t.Errorf("the configuration directory has mode %o", dir.Mode().Perm())
		}
	}

	// The saved key is used when nothing else gives one.
	h.stdin = ""
	out, _ = h.ok("runs", "list")
	contains(t, out, "run_841742f37553189ff9c27ba1")

	out, _ = h.ok("logout")
	contains(t, out, "Logged out")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("logout should delete the saved key")
	}
	out, _ = h.ok("logout")
	contains(t, out, "no saved key")
	if _, _, code := h.run("runs", "list"); code != exitAuth {
		t.Errorf("after logout: exit %d, want 4", code)
	}
}

func TestLoginOnATerminalAsksWithoutEcho(t *testing.T) {
	newHarness(t)
	t.Setenv(envAPIKey, "")
	var out, errs bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &errs, InTTY: true, Build: BuildInfo{Version: "1.2.3"},
		ReadSecret: func() (string, error) { return fakeapi.Key, nil }}
	if code := app.Run(context.Background(), []string{"login"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	contains(t, errs.String(), "API key (it is not shown):")
	lacks(t, out.String()+errs.String(), fakeapi.Key)
}

func TestProgressOnATerminal(t *testing.T) {
	newHarness(t)
	var out, errs bytes.Buffer
	app := &App{In: strings.NewReader(""), Out: &out, Err: &errs, ErrTTY: true, PollInterval: 20 * time.Millisecond, Build: BuildInfo{Version: "1.2.3"}}
	code := app.Run(context.Background(), []string{"run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/never-ends", "--timeout", "350ms"})
	if code != exitError {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	// A line that is redrawn in place, and wiped before anything else is printed.
	contains(t, errs.String(), "\r\x1b[K", "Run run_000000000000000000000004 running", "\r\x1b[K\x1b[31mError:\x1b[0m run run_000000000000000000000004 has not ended")
	if out.Len() != 0 {
		t.Errorf("progress belongs on standard error, standard output got %q", out.String())
	}

	// With --quiet there is none of it.
	errs.Reset()
	app = &App{In: strings.NewReader(""), Out: &out, Err: &errs, ErrTTY: true, PollInterval: time.Millisecond, Build: BuildInfo{Version: "1.2.3"}}
	if code := app.Run(context.Background(), []string{"run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/acme", "-q", "-o", "jsonl"}); code != 0 {
		t.Fatalf("exit %d: %s", code, errs.String())
	}
	if errs.Len() != 0 {
		t.Errorf("--quiet printed %q", errs.String())
	}
}

// --- scrapers ---------------------------------------------------------------------------------------------------------

func TestScrapersList(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("scrapers", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "ID ") {
		t.Fatalf("want a heading and three rows, got:\n%s", out)
	}
	contains(t, out, "TITLE", "CHARGED", "ats.board", "1 unit a run", "linkedin.jobs_search", "1 unit a record")
	// The columns line up.
	if strings.Index(lines[0], "TITLE") != strings.Index(lines[1], "ATS job board") {
		t.Errorf("the columns are not aligned:\n%s", out)
	}

	out, _ = h.ok("scrapers", "list", "--json")
	var doc struct {
		Scrapers []map[string]any `json:"scrapers"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Scrapers) != 3 || doc.Scrapers[0]["billing_unit"] != "request" {
		t.Errorf("--json: %v\n%s", err, out)
	}
	out, _ = h.ok("scrapers", "list", "-o", "jsonl")
	if n := strings.Count(out, "\n"); n != 3 {
		t.Errorf("jsonl: %d lines, want 3", n)
	}
}

func TestScrapersShow(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("scrapers", "show", "ats.board")
	contains(t, out, "ats.board  ATS job board", "1 unit a run, when the run ends ok or partial",
		"INPUT", "FIELD", "REQUIRED", "DEFAULT", "RECORD FIELDS", "enrichednow run ats.board --input board_url=<board_url>")
	for _, want := range [][]string{
		{"board_url", "string", "required", "The board's address", "(at most 2000 characters)"},
		{"company_name", "string?", "optional"},
		{"max_detail_pages", "integer", "optional", "30", "(0 to 200)"},
		{"location", "JobLocation?"},
		{"page_url", "string"},
	} {
		found := false
		for _, line := range strings.Split(out, "\n") {
			if fields := strings.Fields(line); len(fields) > 0 && fields[0] == want[0] {
				all := true
				for _, text := range want[1:] {
					all = all && strings.Contains(line, text)
				}
				found = found || all
			}
		}
		if !found {
			t.Errorf("no line for the field %s with %v in:\n%s", want[0], want[1:], out)
		}
	}
	// The fields come in the schema's order, not the alphabet's.
	records := out[strings.Index(out, "RECORD FIELDS"):]
	if strings.Index(out, "board_url") > strings.Index(out, "max_detail_pages") || strings.Index(records, "  title") > strings.Index(records, "  company_name") {
		t.Errorf("the fields are not in the schema's order:\n%s", out)
	}

	out, _ = h.ok("scrapers", "show", "ats.board", "--json")
	var doc map[string]any
	if json.Unmarshal([]byte(out), &doc) != nil || doc["input_schema"] == nil || doc["record_schema"] == nil {
		t.Errorf("--json should be the API's answer:\n%s", out)
	}

	_, errs, code := h.run("scrapers", "show", "no.such")
	if code != exitError {
		t.Errorf("exit %d, want 1", code)
	}
	contains(t, errs, "Error: There is no scraper 'no.such'.", "(not_found)", "enrichednow scrapers list")
}

// --- run ----------------------------------------------------------------------------------------------------------------

const acme = "board_url=https://boards.greenhouse.io/acme"

func TestRunWaitsAndPrintsTheRecords(t *testing.T) {
	h := newHarness(t)
	out, errs := h.ok("run", "ats.board", "--input", acme, "--input", "max_detail_pages=10", "--input", "company_name=007")
	contains(t, out, "TITLE", "Senior Software Engineer", "Product Designer", "Account Executive, Mid-Market", "COMPANY_NAME", "Acme")
	lacks(t, out, "COMPANY_DOMAIN", "VALID_THROUGH") // no record has a value for these
	contains(t, errs, "running …", "ended ok: 3 records, 1 unit, 885ms.")

	start := lastRequest(t, h, "POST", "/v1/scrapers/ats.board/runs")
	if start.Query != "wait=5" {
		t.Errorf("the start asked for ?%s", start.Query)
	}
	// The schema decides the types: a number field is a number, a string field stays a string.
	var body struct {
		Input map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(start.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Input["max_detail_pages"] != float64(10) || body.Input["company_name"] != "007" || body.Input["board_url"] != "https://boards.greenhouse.io/acme" {
		t.Errorf("input = %s", start.Body)
	}
	if n := h.api.Count("POST", "/v1/scrapers/ats.board/runs"); n != 1 {
		t.Errorf("%d runs were started, want 1", n)
	}
	if records := lastRequest(t, h, "GET", "/v1/runs/run_000000000000000000000004/records"); records.Query != "limit=1000" {
		t.Errorf("records were asked with ?%s", records.Query)
	}
}

func TestRunOutputFormats(t *testing.T) {
	h := newHarness(t)
	instant := "board_url=https://boards.greenhouse.io/instant"

	out, errs := h.ok("run", "ats.board", "-i", instant, "--json")
	var records []map[string]any
	if err := json.Unmarshal([]byte(out), &records); err != nil || len(records) != 3 || records[0]["title"] != "Senior Software Engineer" {
		t.Fatalf("--json: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "[\n  {\n    \"title\"") {
		t.Errorf("--json should be indented and keep the API's field order:\n%.80s", out)
	}
	contains(t, errs, "ended ok")

	out, errs = h.ok("run", "ats.board", "-i", instant, "-o", "jsonl", "--quiet")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("jsonl: %d lines", len(lines))
	}
	for _, line := range lines {
		if !json.Valid([]byte(line)) {
			t.Errorf("not JSON: %s", line)
		}
	}
	if errs != "" {
		t.Errorf("--quiet should print nothing on standard error, got %q", errs)
	}

	out, _ = h.ok("run", "ats.board", "-i", instant, "-o", "csv")
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil || len(rows) != 4 {
		t.Fatalf("csv: %v, %d rows", err, len(rows))
	}
	header := strings.Join(rows[0], ",")
	contains(t, header, "title,company_name,company_domain,location.raw,location.city", "salary.raw,salary.min", "fetched_at")
	lacks(t, header, ",salary,") // the null of one row does not make a column beside salary.min
	column := func(name string) int {
		for i, h := range rows[0] {
			if h == name {
				return i
			}
		}
		t.Fatalf("no column %s in %v", name, rows[0])
		return -1
	}
	if rows[2][column("salary.min")] != "120000" || rows[2][column("location.remote")] != "true" || rows[2][column("description")] != `Design the things, "carefully".` {
		t.Errorf("csv row = %v", rows[2])
	}
	if rows[1][column("company_domain")] != "" || rows[1][column("salary.min")] != "" {
		t.Errorf("a value that is not known should be empty: %v", rows[1])
	}

	out, _ = h.ok("run", "ats.board", "-i", instant, "--limit", "2", "-o", "jsonl")
	if strings.Count(out, "\n") != 2 {
		t.Errorf("--limit 2 printed:\n%s", out)
	}
}

func TestRunWritesToAFile(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	for name, check := range map[string]func(data string) bool{
		"jobs.csv":   func(d string) bool { return strings.HasPrefix(d, "title,company_name") && strings.Count(d, "\n") == 4 },
		"jobs.json":  func(d string) bool { return strings.HasPrefix(d, "[\n") && json.Valid([]byte(d)) },
		"jobs.jsonl": func(d string) bool { return strings.Count(d, "\n") == 3 && strings.HasPrefix(d, `{"title"`) },
		"jobs.out":   func(d string) bool { return strings.Count(d, "\n") == 3 }, // an unknown extension: JSON lines
	} {
		path := filepath.Join(dir, name)
		out, errs := h.ok("run", "ats.board", "-i", "board_url=https://boards.greenhouse.io/instant", "--out", path)
		if out != "" {
			t.Errorf("%s: with --out nothing goes to standard output, got %q", name, out)
		}
		contains(t, errs, "Wrote 3 records to "+path)
		data, err := os.ReadFile(path)
		if err != nil || !check(string(data)) {
			t.Errorf("%s: %v\n%s", name, err, data)
		}
	}
}

func TestRunInputSources(t *testing.T) {
	h := newHarness(t)
	sent := func() map[string]any {
		var body struct {
			Input map[string]any `json:"input"`
		}
		_ = json.Unmarshal([]byte(lastRequest(t, h, "POST", "/v1/scrapers/ats.board/runs").Body), &body)
		return body.Input
	}
	h.ok("run", "ats.board", "--input-json", `{"board_url": "https://boards.greenhouse.io/instant", "max_detail_pages": 5}`, "-q")
	if got := sent(); got["max_detail_pages"] != float64(5) || got["board_url"] != "https://boards.greenhouse.io/instant" {
		t.Errorf("--input-json sent %v", got)
	}
	file := filepath.Join(t.TempDir(), "input.json")
	if err := os.WriteFile(file, []byte(`{"board_url": "https://boards.greenhouse.io/instant", "company_name": "From the file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h.ok("run", "ats.board", "--file", file, "-q")
	if got := sent(); got["company_name"] != "From the file" {
		t.Errorf("--file sent %v", got)
	}
	// Later sources win, field by field: the file, then --input-json, then --input.
	h.ok("run", "ats.board", "--file", file, "--input-json", `{"company_name": "From the flag", "max_detail_pages": 1}`, "--input", "max_detail_pages=2", "-q")
	if got := sent(); got["company_name"] != "From the flag" || got["max_detail_pages"] != float64(2) || got["board_url"] == nil {
		t.Errorf("merged input = %v", got)
	}
	h.stdin = `{"board_url": "https://boards.greenhouse.io/instant", "company_name": "From a pipe"}`
	h.ok("run", "ats.board", "--file", "-", "-q")
	if got := sent(); got["company_name"] != "From a pipe" {
		t.Errorf("--file - sent %v", got)
	}
}

func TestRunThatDoesNotSucceedExitsWith3(t *testing.T) {
	h := newHarness(t)
	for _, tc := range []struct{ board, status, reason string }{
		{"https://boards.greenhouse.io/acme-board-that-does-not-exist", "failed", "adapter_failed"},
		{"https://jobs.dayforcehcm.com/en-US/acme/CANDIDATEPORTAL", "unsupported", "render_required"},
	} {
		out, errs, code := h.run("run", "ats.board", "--input", "board_url="+tc.board)
		if code != exitRunFailed {
			t.Errorf("%s: exit %d, want 3", tc.status, code)
		}
		if out != "" {
			t.Errorf("%s: no records belong on standard output, got %q", tc.status, out)
		}
		contains(t, errs, "Error: run run_", "ended "+tc.status+" (reason: "+tc.reason+")", "nothing was charged", "results-and-statuses")
	}

	// With --json the error is JSON too, and carries the run.
	_, errs, code := h.run("run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/acme-board-that-does-not-exist", "--json")
	var doc struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Details struct {
				Run map[string]any `json:"run"`
			} `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(errs), &doc); err != nil || code != exitRunFailed {
		t.Fatalf("exit %d, %v: %s", code, err, errs)
	}
	if doc.Error.Code != "run_failed" || doc.Error.Details.Run["reason"] != "adapter_failed" || doc.Error.Details.Run["status"] != "failed" {
		t.Errorf("error = %+v", doc.Error)
	}
}

func TestRunPartialSucceedsWithAWarning(t *testing.T) {
	h := newHarness(t)
	out, errs := h.ok("run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/partial", "-o", "jsonl")
	if strings.Count(out, "\n") != 2 {
		t.Errorf("records:\n%s", out)
	}
	contains(t, errs, "ended partial (reason: render_required): 2 records, 1 unit", "Partial: some of the target could not be read.")
}

func TestRunWithNoRecords(t *testing.T) {
	h := newHarness(t)
	out, errs := h.ok("run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/empty")
	if out != "" {
		t.Errorf("stdout = %q", out)
	}
	contains(t, errs, "ended ok: 0 records, 1 unit", "No records.")
	out, _ = h.ok("run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/empty", "--json")
	if strings.TrimSpace(out) != "[]" {
		t.Errorf("--json with no records should be an empty list, got %q", out)
	}
}

func TestRunNoWait(t *testing.T) {
	h := newHarness(t)
	out, errs := h.ok("run", "ats.board", "--input", acme, "--no-wait")
	if out != "run_000000000000000000000004\n" {
		t.Errorf("--no-wait should print the run id alone, got %q", out)
	}
	contains(t, errs, "enrichednow runs wait run_000000000000000000000004")
	if q := lastRequest(t, h, "POST", "/v1/scrapers/ats.board/runs").Query; q != "" {
		t.Errorf("--no-wait should not ask the API to wait, got ?%s", q)
	}
	if n := h.api.Count("GET", "/v1/runs/run_000000000000000000000004"); n != 0 {
		t.Errorf("--no-wait polled %d times", n)
	}
	out, _ = h.ok("run", "ats.board", "--input", acme, "--no-wait", "--json")
	var run map[string]any
	if json.Unmarshal([]byte(out), &run) != nil || run["status"] != "running" || run["reason"] != nil {
		t.Errorf("--no-wait --json should be the run as the API sent it:\n%s", out)
	}
}

func TestRunErrorsFromTheAPI(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		input []string
		code  int
		want  []string
	}{
		{[]string{"--input-json", `{"board": "x", "max_detail_pages": 500}`}, exitError,
			[]string{"Error: The input is not valid.", "(invalid_input)", "input.board_url: Field required", "input.board: Extra inputs are not permitted",
				"input.max_detail_pages: Input should be less than or equal to 200", "enrichednow scrapers show ats.board", "No run was created"}},
		{[]string{"--input", "board_url=https://x.example/no-allowance"}, exitError,
			[]string{"Error: This month's units are used up.", "(quota_exceeded)", "Used 1000 of 1000 units this month.", "enrichednow usage"}},
		{[]string{"--input", "board_url=https://x.example/too-many"}, exitError,
			[]string{"(too_many_runs)", "2 of your 2 concurrent runs are going. No run was created.", "enrichednow runs list --status running"}},
		{[]string{"--input", "board_url=https://x.example/server-error"}, exitError,
			[]string{"Error: Something went wrong on our side.", "(internal)", "The run may have been created all the same", "--idempotency-key cli-"}},
	}
	for _, tc := range cases {
		before := h.api.Count("POST", "/v1/scrapers/ats.board/runs")
		out, errs, code := h.run(append([]string{"run", "ats.board"}, tc.input...)...)
		if code != tc.code || out != "" {
			t.Errorf("%v: exit %d, stdout %q", tc.input, code, out)
		}
		contains(t, errs, tc.want...)
		// Starting a run is sent again only after a server error, and then with the same idempotency key: a
		// second attempt can never start a second run.
		want := 1
		if strings.Contains(strings.Join(tc.input, " "), "server-error") {
			want = 3
		}
		if n := h.api.Count("POST", "/v1/scrapers/ats.board/runs") - before; n != want {
			t.Errorf("%v: the start was sent %d times", tc.input, n)
		}
		keys := map[string]bool{}
		for _, r := range h.api.Requests()[len(h.api.Requests())-want:] {
			keys[r.IdempotencyKey] = true
		}
		if len(keys) != 1 || keys[""] {
			t.Errorf("%v: every attempt should carry one key, got %v", tc.input, keys)
		}
	}
	_, errs, code := h.run("run", "no.such", "--input", "x=1")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "There is no scraper 'no.such'.", "enrichednow scrapers list")

	_, errs, _ = h.run("run", "ats.board", "--input-json", `{"board": "x"}`, "--json")
	var doc map[string]map[string]any
	if json.Unmarshal([]byte(errs), &doc) != nil || doc["error"]["code"] != "invalid_input" || doc["error"]["details"] == nil {
		t.Errorf("--json error: %s", errs)
	}
}

func TestRunTimeout(t *testing.T) {
	h := newHarness(t)
	out, errs, code := h.run("run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/never-ends", "--timeout", "40ms")
	if code != exitError || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	contains(t, errs, "has not ended after 40ms; it goes on", "enrichednow runs wait run_", "enrichednow runs cancel run_")
}

func TestRunInterrupted(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(context.Background())
	h.ctx = ctx
	go func() {
		for h.api.Count("GET", "/v1/runs/run_000000000000000000000004") == 0 {
			time.Sleep(time.Millisecond)
		}
		cancel()
	}()
	_, errs, code := h.run("run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/never-ends")
	if code != exitInterrupted {
		t.Errorf("exit %d, want 130", code)
	}
	contains(t, errs, "interrupted; run run_000000000000000000000004 goes on", "enrichednow runs cancel run_000000000000000000000004")
}

// --- runs -------------------------------------------------------------------------------------------------------------

func TestRunsList(t *testing.T) {
	h := newHarness(t)
	out, errs := h.ok("runs", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Fatalf("want a heading and three runs:\n%s", out)
	}
	contains(t, lines[0], "ID", "SCRAPER", "STATUS", "REASON", "RECORDS", "UNITS", "CREATED (UTC)")
	contains(t, lines[1], "run_841742f37553189ff9c27ba1", "ats.board", "ok", "2026-11-16 22:37:50") // newest first
	contains(t, lines[3], "unsupported", "render_required")
	if errs != "" {
		t.Errorf("stderr = %q", errs)
	}

	out, errs = h.ok("runs", "list", "--limit", "2")
	if n := strings.Count(out, "run_"); n != 2 {
		t.Errorf("--limit 2 printed %d runs", n)
	}
	contains(t, errs, "There are more: add --cursor Mg")
	out, _ = h.ok("runs", "list", "--limit", "2", "--cursor", "Mg")
	contains(t, out, "run_25d40cfbc237d0caee2fc583")
	lacks(t, out, "run_841742f37553189ff9c27ba1")

	out, _ = h.ok("runs", "list", "--limit", "2", "--all", "--json")
	var doc struct {
		Runs       []map[string]any `json:"runs"`
		NextCursor *string          `json:"next_cursor"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Runs) != 3 || doc.NextCursor != nil {
		t.Errorf("--all --json: %v\n%s", err, out)
	}
	if doc.Runs[0]["reason"] != nil || doc.Runs[1]["reason"] != "adapter_failed" {
		t.Errorf("the runs should be as the API sent them: %v", doc.Runs)
	}

	out, _ = h.ok("runs", "list", "--limit", "2", "--json")
	if json.Unmarshal([]byte(out), &doc) != nil || len(doc.Runs) != 2 || doc.NextCursor == nil || *doc.NextCursor != "Mg" {
		t.Errorf("one page as JSON should carry next_cursor:\n%s", out)
	}

	out, _ = h.ok("runs", "list", "--status", "failed", "--scraper", "ats.board")
	contains(t, out, "run_b18944845743ccb58e932c28")
	lacks(t, out, "run_841742f37553189ff9c27ba1")
	if q := lastRequest(t, h, "GET", "/v1/runs").Query; q != "scraper_id=ats.board&status=failed" {
		t.Errorf("query = %s", q)
	}

	_, errs = h.ok("runs", "list", "--status", "queued")
	contains(t, errs, "No runs.")

	out, _ = h.ok("runs", "list", "-o", "csv")
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil || len(rows) != 4 || rows[0][0] != "id" || rows[0][3] != "status" {
		t.Errorf("csv: %v\n%s", err, out)
	}
	contains(t, strings.Join(rows[0], ","), "input.board_url", "diagnostics.facts.ats")
}

func TestRunsGet(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("runs", "get", "run_841742f37553189ff9c27ba1")
	contains(t, out, "Run       run_841742f37553189ff9c27ba1", "Scraper   ats.board (version 1)", "Status    ok", "Records   3", "Units     1",
		"Created   2026-11-16 22:37:50 UTC", "Took      885ms", `Input     {"board_url":"https://boards.greenhouse.io/acme"}`, "Requests  1", "ats", "greenhouse")
	out, _ = h.ok("runs", "get", "run_b18944845743ccb58e932c28")
	contains(t, out, "Status    failed", "Reason    adapter_failed")

	out, _ = h.ok("runs", "get", "run_841742f37553189ff9c27ba1", "--json")
	var run map[string]any
	if json.Unmarshal([]byte(out), &run) != nil || run["id"] != "run_841742f37553189ff9c27ba1" || run["reason"] != nil || run["record_count"] != float64(3) {
		t.Errorf("--json:\n%s", out)
	}

	_, errs, code := h.run("runs", "get", "run_nope")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "Error: There is no run 'run_nope'.", "(not_found)", "enrichednow runs list")
}

func TestRunsRecords(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("runs", "records", "run_841742f37553189ff9c27ba1")
	contains(t, out, "TITLE", "Senior Software Engineer", "Account Executive, Mid-Market")
	out, errs := h.ok("runs", "records", "run_841742f37553189ff9c27ba1", "--limit", "1", "-o", "jsonl")
	if strings.Count(out, "\n") != 1 {
		t.Errorf("--limit 1:\n%s", out)
	}
	contains(t, errs, "Showing the first 1 record;")
	if q := lastRequest(t, h, "GET", "/v1/runs/run_841742f37553189ff9c27ba1/records").Query; q != "limit=1" {
		t.Errorf("query = %s", q)
	}
	_, errs = h.ok("runs", "records", "run_b18944845743ccb58e932c28")
	contains(t, errs, "No records.")
}

func TestRecordsTableFitsTheTerminal(t *testing.T) {
	h := newHarness(t)
	h.tty, h.width = true, 80
	out, errs := h.ok("runs", "records", "run_841742f37553189ff9c27ba1", "--no-color")
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if n := len([]rune(line)); n > 80 {
			t.Errorf("a line is %d characters wide, the terminal 80:\n%s", n, line)
		}
	}
	contains(t, errs, "more fields are not shown in the table; use --output json, jsonl or csv")
}

func TestRunsCancelAndWait(t *testing.T) {
	h := newHarness(t)
	id, _ := h.ok("run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/never-ends", "--no-wait", "-q")
	id = strings.TrimSpace(id)

	out, errs := h.ok("runs", "cancel", id)
	contains(t, out, "Run "+id+" was asked to stop; it is still running.")
	contains(t, errs, "enrichednow runs wait "+id)

	// It ends cancelled, with the record it had found: exit 3, and the way to those records.
	out, errs, code := h.run("runs", "wait", id)
	if code != exitRunFailed || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	contains(t, errs, "ended cancelled (reason: cancelled)", "Nothing was charged. The 1 records found before it stopped are kept: enrichednow runs records "+id)

	out, _ = h.ok("runs", "cancel", id)
	contains(t, out, "Run "+id+" is cancelled.")
	out, _ = h.ok("runs", "cancel", "run_841742f37553189ff9c27ba1")
	contains(t, out, "had already ended ok; nothing changed.")
	out, _ = h.ok("runs", "cancel", "run_841742f37553189ff9c27ba1", "--json")
	if !json.Valid([]byte(out)) || !strings.Contains(out, `"status": "ok"`) {
		t.Errorf("--json:\n%s", out)
	}

	id, _ = h.ok("run", "ats.board", "--input", acme, "--no-wait", "-q")
	out, errs = h.ok("runs", "wait", strings.TrimSpace(id))
	contains(t, out, "Status    ok", "Records   3")
	contains(t, errs, "running …")
}

// --- usage and data ---------------------------------------------------------------------------------------------------

func TestUsage(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("usage")
	contains(t, out, "2026-11-01 to 2026-11-03: 13 units used; the monthly allowance is 1000.", "DAY", "UNITS", "CHARGED", "2026-11-03")
	out, _ = h.ok("usage", "--group-by", "scraper", "--from", "2026-10-01", "--to", "2026-10-31", "--product", "data")
	contains(t, out, "SCRAPER", "data.postings.list", "13 data units used; the monthly allowance is 5000.", "2026-10-01 to 2026-10-31")
	if q := lastRequest(t, h, "GET", "/v1/usage").Query; q != "from=2026-10-01&group_by=scraper&product=data&to=2026-10-31" {
		t.Errorf("query = %s", q)
	}
	out, _ = h.ok("usage", "--json")
	var usage map[string]any
	if json.Unmarshal([]byte(out), &usage) != nil || usage["total_units"] != float64(13) || usage["period"] == nil {
		t.Errorf("--json:\n%s", out)
	}
	_, errs, code := h.run("usage", "--from", "2026-12-01", "--to", "2026-11-01")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "The period starts after it ends.", "from: The period starts after it ends.")
}

func TestData(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("data", "companies", "list", "--query", "figma", "--ats", "greenhouse")
	contains(t, out, "DOMAIN", "figma.com", "Figma", "greenhouse", "140")
	if q := lastRequest(t, h, "GET", "/v1/data/companies").Query; q != "ats=greenhouse&q=figma" {
		t.Errorf("query = %s", q)
	}
	_, errs := h.ok("data", "companies", "list", "--query", "nobody")
	contains(t, errs, "No companies match.")

	out, _ = h.ok("data", "companies", "get", "figma.com")
	contains(t, out, "Domain        figma.com", "Careers page  https://boards.greenhouse.io/figma", "Postings      140 open, 0 closed", "complete")
	_, errs, code := h.run("data", "companies", "get", "nobody.example")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "(not_found)")

	out, _ = h.ok("data", "companies", "postings", "figma.com", "--title", "engineer", "--status", "open")
	contains(t, out, "job_0c1d2e3f4a5b6c7d8e9f0a1b", "Data Engineer")
	lacks(t, out, "Account Executive")
	if q := lastRequest(t, h, "GET", "/v1/data/companies/figma.com/postings").Query; q != "status=open&title=engineer" {
		t.Errorf("query = %s", q)
	}

	out, errs = h.ok("data", "postings", "list", "--limit", "2")
	contains(t, out, "ID", "TITLE", "COMPANY", "LOCATION", "STATUS", "POSTED", "Voice of the Customer Program Manager", "Figma", "San Francisco, CA", "open", "2026-06-04")
	contains(t, errs, "There are more: add --cursor Mg")
	out, _ = h.ok("data", "postings", "list", "--limit", "2", "--all", "-o", "jsonl")
	if strings.Count(out, "\n") != 3 {
		t.Errorf("--all:\n%s", out)
	}

	h.ok("data", "postings", "list", "--company", "figma.com", "--title", "data engineer", "--location", "New York", "--country", "US",
		"--remote=false", "--employment-type", "full_time", "--seniority", "senior", "--function", "data", "--status", "open", "--source", "greenhouse",
		"--provenance", "feed,page", "--unmatched", "--posted-from", "2026-06-01", "--posted-to", "2026-06-30", "--first-seen-from", "2026-11-01",
		"--last-seen-to", "2026-11-16T12:00:00Z", "--sort", "last_seen_at", "--limit", "10")
	want := "company_domain=figma.com&country=US&employment_type=full_time&first_seen_from=2026-11-01T00%3A00%3A00Z&function=data" +
		"&last_seen_to=2026-11-16T12%3A00%3A00Z&limit=10&location=New+York&posted_from=2026-06-01&posted_to=2026-06-30&provenance=feed%2Cpage" +
		"&remote=false&seniority=senior&sort=last_seen_at&source=greenhouse&status=open&title=data+engineer&unmatched=true"
	if q := lastRequest(t, h, "GET", "/v1/data/postings").Query; q != want {
		t.Errorf("query = %s\n want   %s", q, want)
	}
	h.ok("data", "postings", "list")
	if q := lastRequest(t, h, "GET", "/v1/data/postings").Query; q != "" {
		t.Errorf("without filters nothing should be sent, got ?%s", q)
	}

	out, _ = h.ok("data", "postings", "get", "job_2a8c2e32f38fa96099cc53b2")
	contains(t, out, "Posting     job_2a8c2e32f38fa96099cc53b2", "Title       Voice of the Customer Program Manager", "Seniority   manager",
		"HISTORY", "reopened", "seen 3 times, reopened 1 times", "DESCRIPTION", "Figma is growing")
	lacks(t, out, "Salary", "Closed")
	out, _ = h.ok("data", "postings", "get", "job_2a8c2e32f38fa96099cc53b2", "--json")
	var posting map[string]any
	if json.Unmarshal([]byte(out), &posting) != nil || posting["history"] == nil || posting["salary"] != nil {
		t.Errorf("--json:\n%s", out)
	}
}

func TestDataErrors(t *testing.T) {
	h := newHarness(t)
	t.Setenv(envAPIKey, fakeapi.KeyWithoutData)
	_, errs, code := h.run("data", "postings", "list")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "(forbidden)", "early access", "datasets/#early-access")

	t.Setenv(envAPIKey, fakeapi.Key)
	before := len(h.api.Requests())
	_, errs, code = h.run("data", "postings", "list", "--title", "unavailable")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "(data_unavailable)", "Try again in a moment.")
	// A read is safe to repeat: it was tried three times before giving up.
	if n := len(h.api.Requests()) - before; n != 3 {
		t.Errorf("the read was sent %d times, want 3", n)
	}
}

func TestHealthAndConnectionErrors(t *testing.T) {
	h := newHarness(t)
	t.Setenv(envAPIKey, "")
	out, _ := h.ok("health")
	contains(t, out, h.api.URL+" is ok.")
	out, _ = h.ok("health", "--json")
	if strings.Join(strings.Fields(out), "") != `{"status":"ok"}` {
		t.Errorf("--json: %s", out)
	}

	_, errs, code := h.run("health", "--api-url", "http://127.0.0.1:1")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "Error: could not reach the API at http://127.0.0.1:1", "--api-url")
	_, errs, code = h.run("health", "--api-url", "not a url")
	if code != exitUsage {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "is not an http(s) address")
}

// --- colours, completion ----------------------------------------------------------------------------------------------

func TestColours(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("runs", "list")
	lacks(t, out, "\x1b[") // not a terminal: plain

	h.tty = true
	out, _ = h.ok("runs", "list")
	contains(t, out, "\x1b[32mok", "\x1b[31mfailed", "\x1b[1mID")
	out, _ = h.ok("runs", "list", "--no-color")
	lacks(t, out, "\x1b[")
	t.Setenv("NO_COLOR", "1")
	out, _ = h.ok("runs", "list")
	lacks(t, out, "\x1b[")
	// JSON is never coloured.
	t.Setenv("NO_COLOR", "")
	out, _ = h.ok("runs", "list", "--json")
	lacks(t, out, "\x1b[")
}

func TestCompletion(t *testing.T) {
	h := newHarness(t)
	for shell, marker := range map[string]string{"bash": "__start_enrichednow", "zsh": "#compdef enrichednow", "fish": "complete -c enrichednow", "powershell": "Register-ArgumentCompleter"} {
		out, _ := h.ok("completion", shell)
		contains(t, out, marker)
	}
	// Scraper ids are completed from the catalogue, statuses and formats from their lists.
	out, _ := h.ok("__complete", "run", "")
	contains(t, out, "ats.board\tATS job board", "careers.company")
	out, _ = h.ok("__complete", "runs", "list", "--status", "")
	contains(t, out, "partial", "cancelled")
	out, _ = h.ok("__complete", "runs", "records", "x", "--output", "")
	contains(t, out, "jsonl", "csv")
}

// --- the pieces -------------------------------------------------------------------------------------------------------

func TestCoerce(t *testing.T) {
	node := func(schema string) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(schema), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	cases := []struct {
		schema, value string
		want          string // as JSON
	}{
		{`{"type":"string"}`, "02139", `"02139"`},
		{`{"type":"string"}`, "true", `"true"`},
		{`{"anyOf":[{"type":"string"},{"type":"null"}]}`, "123", `"123"`},
		{`{"type":"integer"}`, "10", `10`},
		{`{"type":"number"}`, "0.5", `0.5`},
		{`{"type":"boolean"}`, "true", `true`},
		{`{"anyOf":[{"type":"boolean"},{"type":"null"}]}`, "false", `false`},
		{`{"type":"array","items":{"type":"string"}}`, "a, b,c", `["a","b","c"]`},
		{`{"type":"array","items":{"type":"string"}}`, `["a","b"]`, `["a","b"]`},
		{`{"type":"array","items":{"type":"integer"}}`, "1,2", `[1,2]`},
		{`{"type":"object"}`, `{"a":1}`, `{"a":1}`},
		{`{"enum":["a","b"]}`, "a", `"a"`},
		{`null`, "5", `5`}, // a field the schema does not have: what it reads as
		{`null`, "acme.com", `"acme.com"`},
		{`null`, "null", `null`},
	}
	for _, tc := range cases {
		got, err := coerce("field", tc.value, node(tc.schema))
		if err != nil {
			t.Errorf("%s %q: %v", tc.schema, tc.value, err)
			continue
		}
		if encoded := string(mustJSON(got)); encoded != tc.want {
			t.Errorf("%s %q: got %s, want %s", tc.schema, tc.value, encoded, tc.want)
		}
	}
	for _, tc := range [][2]string{{`{"type":"integer"}`, "many"}, {`{"type":"boolean"}`, "maybe"}, {`{"type":"object"}`, "x"}} {
		if _, err := coerce("field", tc[1], node(tc[0])); err == nil {
			t.Errorf("%s %q should be refused", tc[0], tc[1])
		}
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	tbl := &table{head: []string{"NAME", "N", "NOTE"}, right: map[int]bool{1: true},
		rows: [][]string{{"a", "1", "short"}, {"longer name", "200", strings.Repeat("x", 100)}, {"ünï", "3", ""}}}
	tbl.render(&buf, &style{}, 40)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	want := []string{
		"NAME           N  NOTE",
		"a              1  short",
		"longer name  200  " + strings.Repeat("x", 21) + "…",
		"ünï            3",
	}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", buf.String(), strings.Join(want, "\n"))
	}
	for _, line := range lines {
		if n := len([]rune(line)); n > 40 {
			t.Errorf("line of %d characters: %q", n, line)
		}
	}
}

func TestCSVFlattensAndUnionsTheFields(t *testing.T) {
	var buf bytes.Buffer
	rows := []json.RawMessage{
		json.RawMessage(`{"a":1,"b":{"c":"x","d":null},"tags":["p","q"]}`),
		json.RawMessage(`{"a":2,"b":null,"e":"line\nbreak, \"quoted\""}`),
	}
	if err := writeCSV(&buf, rows); err != nil {
		t.Fatal(err)
	}
	got, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"a", "b.c", "b.d", "tags", "e"}, {"1", "x", "", `["p","q"]`, ""}, {"2", "", "", "", "line\nbreak, \"quoted\""}}
	if len(got) != 3 || strings.Join(got[0], "|") != strings.Join(want[0], "|") || strings.Join(got[1], "|") != strings.Join(want[1], "|") || strings.Join(got[2], "|") != strings.Join(want[2], "|") {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestMaskKey(t *testing.T) {
	if got := maskKey(fakeapi.Key); got != "en_live_Zk3uTq8v…" {
		t.Errorf("maskKey = %q", got)
	}
	for _, short := range []string{"en_live_short", "something else entirely", ""} {
		if got := maskKey(short); got != "en_live_…" {
			t.Errorf("maskKey(%q) = %q: a short or odd key must not be shown at all", short, got)
		}
	}
}
