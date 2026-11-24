package cli

import (
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/enrichednow/enrichednow-cli/internal/fakeapi"
)

const (
	queuedRunID  = "frun_000000000000000000000002"
	previewRunID = "frun_000000000000000000000003"
	savedID      = "sf_000000000000000000000002"
)

// recordedParams are the flags that give the params of the runs recorded from the real API.
var recordedParams = []string{"-p", "job_titles=Java Developer", "--param", "locations=London", "-p", "posted_within_days=2",
	"-p", "title_exclude_keywords=intern", "-p", "exclude_companies=Blocked By Us Inc", "-p", "company_size=11-5000"}

// recordedBody is the body of a request recorded from the real API, in testdata/formulas.
func recordedBody(t *testing.T, name string) any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "fakeapi", "testdata", "formulas", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var ex struct {
		Request struct {
			Body any `json:"body"`
		} `json:"request"`
	}
	if err := json.Unmarshal(data, &ex); err != nil {
		t.Fatal(err)
	}
	return ex.Request.Body
}

// firstOfLast is the first of the last n requests to a method and a path: the first page a command read.
func firstOfLast(t *testing.T, h *harness, n int, method, path string) fakeapi.Request {
	t.Helper()
	var matching []fakeapi.Request
	for _, r := range h.api.Requests() {
		if r.Method == method && r.Path == path {
			matching = append(matching, r)
		}
	}
	if len(matching) < n {
		t.Fatalf("%d requests to %s %s, want at least %d", len(matching), method, path, n)
	}
	return matching[len(matching)-n]
}

func sameJSON(t *testing.T, got string, want any) {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatalf("not JSON: %s", got)
	}
	if !reflect.DeepEqual(decoded, want) {
		wanted, _ := json.Marshal(want)
		t.Errorf("sent\n %s\nwant\n %s", got, wanted)
	}
}

func TestFormulasList(t *testing.T) {
	h := newHarness(t)
	t.Setenv(envAPIKey, "") // the catalogue needs no key
	out, errs := h.ok("formulas", "list")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "ID ") {
		t.Fatalf("want a heading and three formulas:\n%s", out)
	}
	contains(t, lines[0], "TITLE", "CATEGORY", "STATUS", "SUMMARY")
	contains(t, out, "job-order-hunter", "Job Order Hunter", "recruiting", "beta", "posting-watch")
	contains(t, errs, "enrichednow formulas get <id>")
	if auth := lastRequest(t, h, "GET", "/v1/formulas").Authorization; auth != "" {
		t.Errorf("Authorization = %q", auth)
	}

	out, _ = h.ok("formulas", "list", "--query", "hiring", "--category", "recruiting", "--persona", "recruiter", "--status", "beta")
	if q := lastRequest(t, h, "GET", "/v1/formulas").Query; q != "category=recruiting&persona=recruiter&query=hiring&status=beta" {
		t.Errorf("query = %s", q)
	}
	contains(t, out, "job-order-hunter")
	lacks(t, out, "posting-watch")
	_, errs = h.ok("formulas", "list", "--category", "nothing")
	contains(t, errs, "No formulas match.")

	out, _ = h.ok("formulas", "list", "--json")
	var doc struct {
		Formulas []map[string]any `json:"formulas"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc.Formulas) != 3 || doc.Formulas[1]["needs_attestation"] != true {
		t.Errorf("--json: %v\n%s", err, out)
	}
}

func TestFormulasGet(t *testing.T) {
	h := newHarness(t)
	h.width = 200
	out, _ := h.ok("formulas", "get", "job-order-hunter")
	contains(t, out, "job-order-hunter  Job Order Hunter", "Version      1.1.0, beta", "Schedules    daily, weekdays, weekly (recommended: weekdays)",
		"Terms        must be accepted first", "Not offered  google.jobs: runs go on without it", "a unit cap of 3000 by default, 20 to 50000",
		"PARAMETERS", "COLUMNS", "PRESETS", "it-contract-uk", "--view company",
		"RUN IT", "enrichednow formulas run job-order-hunter --param job_titles=<job_titles> --param locations=<locations> --preview")
	for _, want := range [][]string{
		{"job_titles", "list of string", "required", "The roles you recruit for", "(1 to 10 items"},
		{"posted_within_days", "integer", "optional", "1", "(1 to 30)"},
		{"company_size", "object", "optional", `{"max":5000,"min":11}`},
		{"company_name", "Company", "string", "yes"},
		{"headquarters", "Headquarters", "no"},
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
			t.Errorf("no line for %s with %v in:\n%s", want[0], want[1:], out)
		}
	}
	out, _ = h.ok("formulas", "get", "job-order-hunter", "--json")
	var doc map[string]any
	if json.Unmarshal([]byte(out), &doc) != nil || doc["parameters"] == nil || doc["columns"] == nil {
		t.Errorf("--json should be the API's answer:\n%.300s", out)
	}
	_, errs, code := h.run("formulas", "get", "no-such-formula")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "Error: There is no formula 'no-such-formula'.", "(formula_not_found)", "enrichednow formulas list")
}

func TestFormulasEstimate(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok(append([]string{"formulas", "estimate", "job-order-hunter", "--unit-cap", "2000"}, recordedParams...)...)
	contains(t, out, "Valid. About 63 units (at least 1, at most 1425), and about 1 row.", "2 searches across 2 sources",
		"Not offered here: google.jobs.")
	// The flags, turned by the parameter schema, make the very request recorded from the real API.
	sameJSON(t, lastRequest(t, h, "POST", "/v1/formulas/job-order-hunter/estimate").Body, recordedBody(t, "formulas_estimate"))
	if n := h.api.Count("GET", "/v1/formulas/job-order-hunter"); n != 1 {
		t.Errorf("the schema was read %d times", n)
	}

	out, errs, code := h.run("formulas", "estimate", "job-order-hunter", "-p", "locations=London")
	if code != exitError || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	contains(t, errs, "Error: these params cannot run", "params.job_titles: This is required.", "enrichednow formulas get job-order-hunter")
	sameJSON(t, lastRequest(t, h, "POST", "/v1/formulas/job-order-hunter/estimate").Body, recordedBody(t, "formulas_estimate_invalid"))

	out, errs, code = h.run("formulas", "estimate", "job-order-hunter", "--params-json", `{"locations": ["London"]}`, "--json")
	var answer map[string]any
	var failure map[string]map[string]any
	if code != exitError || json.Unmarshal([]byte(out), &answer) != nil || answer["valid"] != false ||
		json.Unmarshal([]byte(errs), &failure) != nil || failure["error"]["code"] != "invalid_params" {
		t.Errorf("--json: exit %d\n%s\n%s", code, out, errs)
	}
}

// --param is turned into what the parameter schema asks for; --params-json gives the rest.
func TestFormulaParamsFlags(t *testing.T) {
	h := newHarness(t)
	h.ok("formulas", "estimate", "job-order-hunter", "--params-json", `{"locations": ["Paris"], "sources": ["linkedin"], "max_results": 50}`,
		"-p", "job_titles=Java Developer", "-p", "job_titles=DevOps Engineer", "-p", "locations=London,Leeds", "-p", "title_synonyms=true",
		"-p", "results_per_search=10", "-p", "company_size=11..500", "-p", "employment_types=[\"contract\"]", "-p", "industries_include=Software, Health")
	var body struct {
		Params map[string]any `json:"params"`
	}
	if err := json.Unmarshal([]byte(lastRequest(t, h, "POST", "/v1/formulas/job-order-hunter/estimate").Body), &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"job_titles": []any{"Java Developer", "DevOps Engineer"}, "locations": []any{"London", "Leeds"}, "sources": []any{"linkedin"},
		"max_results": float64(50), "title_synonyms": true, "results_per_search": float64(10), "company_size": map[string]any{"min": float64(11), "max": float64(500)},
		"employment_types": []any{"contract"}, "industries_include": []any{"Software", "Health"},
	}
	if !reflect.DeepEqual(body.Params, want) {
		t.Errorf("params = %v\nwant     %v", body.Params, want)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"-p", "no_such=1"}, `the formula job-order-hunter has no parameter "no_such"`},
		{[]string{"-p", "results_per_search=many"}, "--param results_per_search=many: results_per_search takes integer"},
		{[]string{"-p", "locations"}, `--param "locations" is not name=value`},
		{[]string{"--params-json", "[1]"}, "--params-json is not a JSON object"},
	} {
		_, errs, code := h.run(append([]string{"formulas", "run", "job-order-hunter"}, tc.args...)...)
		if code != exitUsage {
			t.Errorf("%v: exit %d", tc.args, code)
		}
		contains(t, errs, tc.want)
	}
	if n := h.api.Count("POST", "/v1/formulas/job-order-hunter/runs"); n != 0 {
		t.Errorf("a mistake in the params must not start a run; %d were started", n)
	}
}

func TestFormulasRunWaitsAndPrintsTheRows(t *testing.T) {
	h := newHarness(t)
	h.width = 400
	out, errs := h.ok(append([]string{"formulas", "run", "job-order-hunter", "--unit-cap", "2000"}, recordedParams...)...)
	contains(t, out, "COMPANY_NAME", "JOB_TITLE", "CONTACT_EMAIL", "SCORE", "Corvid", "Java Developer (Remote)", "careers@corvid-demo.com", "Quiet Agencies Ltd")
	lacks(t, out, "HEADQUARTERS", "TECHNOLOGIES") // the formula does not show these in a table
	contains(t, errs, "Formula run "+queuedRunID+" running", "ended partial (reason: contacts_not_connected): 4 rows, 24 units, 1m22s.",
		"source_not_offered: google.jobs is not offered here; the run went on without it")

	start := lastRequest(t, h, "POST", "/v1/formulas/job-order-hunter/runs")
	sameJSON(t, start.Body, recordedBody(t, "formulas_run_queued"))
	if start.Query != "wait=5" || !strings.HasPrefix(start.IdempotencyKey, "cli-") {
		t.Errorf("the start: ?%s, key %q", start.Query, start.IdempotencyKey)
	}
	if get := lastRequest(t, h, "GET", "/v1/formula-runs/"+queuedRunID); get.Query != "wait=60" {
		t.Errorf("each look asks the API to wait: ?%s", get.Query)
	}
	var pages []string
	for _, r := range h.api.Requests() {
		if r.Path == "/v1/formula-runs/"+queuedRunID+"/rows" {
			pages = append(pages, r.Query)
		}
	}
	if strings.Join(pages, " ") != "limit=1000 cursor=Mg&limit=1000" {
		t.Errorf("rows were read with %v", pages)
	}
}

func TestFormulasRunOutputs(t *testing.T) {
	h := newHarness(t)
	run := append([]string{"formulas", "run", "job-order-hunter"}, recordedParams...)

	out, _ := h.ok(append(run, "--json")...)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 4 || rows[0]["data"] == nil || rows[0]["evidence"] == nil {
		t.Fatalf("--json: %v\n%.300s", err, out)
	}
	out, errs := h.ok(append(run, "-o", "jsonl", "--quiet")...)
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 4 || !json.Valid([]byte(lines[3])) || errs != "" {
		t.Errorf("jsonl:\n%s\n%s", out, errs)
	}

	// A CSV file is the API's, every page of it, the header row once.
	dir := t.TempDir()
	file := filepath.Join(dir, "leads.csv")
	out, errs = h.ok(append(run, "--out", file)...)
	data, _ := os.ReadFile(file)
	records, err := csv.NewReader(strings.NewReader(string(data))).ReadAll()
	if err != nil || len(records) != 5 || records[0][0] != "company_name" || records[4][0] != "Quiet Agencies Ltd" || out != "" {
		t.Fatalf("%v: %d records\n%s", err, len(records), data)
	}
	if strings.Count(string(data), "company_name,company_domain") != 1 {
		t.Errorf("the header row should be written once:\n%s", data)
	}
	contains(t, errs, "Wrote 4 rows to "+file+" (csv, as the API writes it).")
	var asked []string
	for _, r := range h.api.Requests() {
		if strings.HasSuffix(r.Path, "/rows") && strings.Contains(r.Query, "format=csv") {
			asked = append(asked, r.Query+" "+r.Accept)
		}
	}
	if strings.Join(asked, " | ") != "format=csv&limit=1000 text/csv | cursor=Mg&format=csv&limit=1000 text/csv" {
		t.Errorf("asked %v", asked)
	}

	out, _ = h.ok(append(run, "--format", "ndjson")...)
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 4 || !strings.HasPrefix(lines[0], `{"score":20`) {
		t.Errorf("ndjson:\n%s", out)
	}
	file = filepath.Join(dir, "leads.ndjson")
	_, errs = h.ok(append(run, "--out", file)...)
	contains(t, errs, "Wrote 4 rows to "+file+" (ndjson")
	out, _ = h.ok(append(run, "-o", "csv")...)
	if !strings.HasPrefix(out, "company_name,company_domain,industry") {
		t.Errorf("-o csv is the API's CSV:\n%.200s", out)
	}
	for _, bad := range [][]string{{"--format", "xml"}, {"--format", "csv", "-o", "json"}, {"--format", "ndjson", "--json"}} {
		if _, _, code := h.run(append(run, bad...)...); code != exitUsage {
			t.Errorf("%v: exit %d", bad, code)
		}
	}
}

func TestFormulasRunPreviewAndNoWait(t *testing.T) {
	h := newHarness(t)
	out, errs := h.ok(append([]string{"formulas", "run", "job-order-hunter", "--preview", "--idempotency-key", "c0ffee-run-2", "-o", "jsonl"}, recordedParams...)...)
	if strings.Count(out, "\n") != 4 {
		t.Errorf("rows:\n%s", out)
	}
	contains(t, errs, "Formula run "+previewRunID+" ended partial", "A preview: at most 10 rows and 50 units")
	start := lastRequest(t, h, "POST", "/v1/formulas/job-order-hunter/runs")
	if start.IdempotencyKey != "c0ffee-run-2" || !strings.Contains(start.Body, `"mode":"preview"`) {
		t.Errorf("the start: %+v", start)
	}
	if n := h.api.Count("GET", "/v1/formula-runs/"+previewRunID); n != 0 {
		t.Errorf("a run that came back ended needs no look; %d were made", n)
	}

	out, errs = h.ok(append([]string{"formulas", "run", "job-order-hunter", "--no-wait"}, recordedParams...)...)
	if out != queuedRunID+"\n" {
		t.Errorf("--no-wait should print the run id alone, got %q", out)
	}
	contains(t, errs, "enrichednow formulas wait "+queuedRunID, "enrichednow formulas rows "+queuedRunID)
	if q := lastRequest(t, h, "POST", "/v1/formulas/job-order-hunter/runs").Query; q != "" {
		t.Errorf("--no-wait should not ask the API to wait, got ?%s", q)
	}
	out, _ = h.ok(append([]string{"formulas", "run", "job-order-hunter", "--no-wait", "--json"}, recordedParams...)...)
	var run map[string]any
	if json.Unmarshal([]byte(out), &run) != nil || run["status"] != "queued" {
		t.Errorf("--no-wait --json:\n%s", out)
	}

	out, errs = h.ok("formulas", "wait", queuedRunID)
	contains(t, out, "Formula run  "+queuedRunID, "Status       partial", "Units        24, of a cap of 2000", "REASONS", "STEPS", "search_linkedin")
	contains(t, errs, "running")
	out, _ = h.ok("formulas", "wait", queuedRunID, "--json")
	if json.Unmarshal([]byte(out), &run) != nil || run["status"] != "partial" || run["steps"] == nil {
		t.Errorf("wait --json:\n%.300s", out)
	}
}

func TestFormulasRunTimeoutAndCancel(t *testing.T) {
	h := newHarness(t)
	h.api.PollsUntilDone = 1 << 30
	_, errs, code := h.run(append([]string{"formulas", "run", "job-order-hunter", "--timeout", "60ms"}, recordedParams...)...)
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "formula run "+queuedRunID+" has not ended after 60ms; it goes on", "enrichednow formulas wait "+queuedRunID,
		"enrichednow formulas cancel "+queuedRunID)

	out, errs := h.ok("formulas", "cancel", queuedRunID)
	contains(t, out, "Formula run "+queuedRunID+" was asked to stop; it is still running.")
	contains(t, errs, "enrichednow formulas wait "+queuedRunID)
	// It ends cancelled: exit 3.
	out, errs, code = h.run("formulas", "wait", queuedRunID)
	if code != exitRunFailed || out != "" {
		t.Errorf("exit %d, stdout %q", code, out)
	}
	contains(t, errs, "Error: formula run "+queuedRunID+" ended cancelled (reason: cancelled)", "No rows.", "enrichednow formulas get job-order-hunter")
	_, errs, _ = h.run("formulas", "wait", queuedRunID, "--json")
	var failure struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Run map[string]any `json:"run"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(errs), &failure) != nil || failure.Error.Code != "run_cancelled" || failure.Error.Details.Run["id"] != queuedRunID {
		t.Errorf("--json error: %s", errs)
	}

	out, _ = h.ok("formulas", "cancel", "frun_000000000000000000000001", "--json")
	if !strings.Contains(out, `"status": "running"`) {
		t.Errorf("--json:\n%.200s", out)
	}
	out, _ = h.ok("formulas", "cancel", previewRunID)
	contains(t, out, "had already ended partial; nothing changed.")
}

func TestFormulasRows(t *testing.T) {
	h := newHarness(t)
	h.width = 300
	out, _ := h.ok("formulas", "rows", queuedRunID)
	contains(t, out, "COMPANY_NAME", "Northwind Ltd", "Quiet Agencies Ltd")

	out, _ = h.ok("formulas", "rows", queuedRunID, "--fields", "company_name,job_title,contact_email", "--min-score", "10")
	if q := firstOfLast(t, h, 2, "GET", "/v1/formula-runs/"+queuedRunID+"/rows").Query; q != "fields=company_name%2Cjob_title%2Ccontact_email&limit=1000&min_score=10" {
		t.Errorf("query = %s", q)
	}
	head := strings.Fields(strings.Split(out, "\n")[0])
	if strings.Join(head, " ") != "COMPANY_NAME JOB_TITLE CONTACT_EMAIL" {
		t.Errorf("the columns asked for, in that order: %v", head)
	}

	out, _ = h.ok("formulas", "rows", queuedRunID, "--view", "company")
	contains(t, out, "Java Developer (Remote), Java Consultant", "ROWS")
	out, errs := h.ok("formulas", "rows", queuedRunID, "--limit", "1", "-o", "jsonl")
	if strings.Count(out, "\n") != 1 {
		t.Errorf("--limit 1:\n%s", out)
	}
	contains(t, errs, "Showing the first 1 row;")
	if q := lastRequest(t, h, "GET", "/v1/formula-runs/"+queuedRunID+"/rows").Query; q != "limit=1" {
		t.Errorf("query = %s", q)
	}

	file := filepath.Join(t.TempDir(), "leads.csv")
	_, errs = h.ok("formulas", "rows", queuedRunID, "--format", "csv", "--out", file)
	contains(t, errs, "Wrote 4 rows to "+file)

	_, errs, code := h.run("formulas", "rows", "frun_nope")
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "There is no formula run 'frun_nope'.", "(not_found)")
}

func TestFormulaErrors(t *testing.T) {
	h := newHarness(t)
	run := append([]string{"formulas", "run", "job-order-hunter"}, recordedParams...)

	h.api.NeedsAttestation = true
	_, errs, code := h.run(run...)
	if code != exitError {
		t.Errorf("exit %d", code)
	}
	contains(t, errs, "(attestation_required)", "The terms: personal_data, version 2026-11-24.",
		"An owner or an admin of the account accepts them in the console, on the Formulas page; an API key cannot.")
	_, errs, _ = h.run(append(run, "--json")...)
	var failure map[string]map[string]any
	if json.Unmarshal([]byte(errs), &failure) != nil || failure["error"]["code"] != "attestation_required" {
		t.Errorf("--json: %s", errs)
	}
	h.api.NeedsAttestation = false

	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"formulas", "run", "data-formula", "--params-json", `{"terms": ["java"]}`},
			[]string{"(requirement_missing)", "Missing product:data: Datasets, which this account has not been given", "enrichednow formulas get data-formula"}},
		{[]string{"formulas", "run", "retired-formula", "--params-json", `{"terms": ["java"]}`},
			[]string{"(formula_unavailable)", "The formula is deprecated.", "enrichednow formulas list"}},
		{append(append([]string{}, run...), "--unit-cap", "1"),
			[]string{"(unit_cap_too_low)", "The least unit cap it takes is 20.", "Give a higher --unit-cap"}},
		{[]string{"formulas", "run", "no-such-formula", "--params-json", `{}`},
			[]string{"There is no formula 'no-such-formula'.", "(formula_not_found)", "enrichednow formulas list"}},
		{[]string{"saved", "create", "job-order-hunter", "--params-json", `{"job_titles": ["Java Developer"], "locations": ["London"]}`, "--schedule", "hourly@07:30"},
			[]string{"(plan_limit_reached)", "The plan runs a saved formula every day at the shortest; this asked for every hour.", "on the Billing page"}},
		{[]string{"saved", "run", fakeapi.CappedSavedFormula},
			[]string{"(limit_reached)", "This saved formula's monthly unit cap: 4 of 3 units used this month; this run needs 10.", "Raise or remove its monthly cap"}},
		{[]string{"saved", "run", fakeapi.ArchivedSavedFormula},
			[]string{"(saved_formula_archived)", "Duplicate it to use it again: `enrichednow saved duplicate " + fakeapi.ArchivedSavedFormula + "`"}},
		{[]string{"saved", "pause", fakeapi.ArchivedSavedFormula}, []string{"(saved_formula_archived)"}},
	}
	for _, tc := range cases {
		out, errs, code := h.run(tc.args...)
		if code != exitError || out != "" {
			t.Errorf("%v: exit %d, stdout %q", tc.args, code, out)
		}
		contains(t, errs, tc.want...)
	}
}

func TestSaved(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("saved", "list")
	contains(t, out, "ID", "NAME", "SCHEDULE", savedID, "Java Developer, London, on weekdays", "weekdays at 07:30 Europe/London")
	_, errs := h.ok("saved", "list", "--formula", "posting-watch", "--status", "paused")
	contains(t, errs, "No saved formulas.")
	if q := lastRequest(t, h, "GET", "/v1/saved-formulas").Query; q != "formula_id=posting-watch&status=paused" {
		t.Errorf("query = %s", q)
	}

	out, errs = h.ok(append([]string{"saved", "create", "job-order-hunter", "--schedule", "weekdays@7:30", "--timezone", "Europe/London", "--notify", "digest"}, recordedParams...)...)
	sameJSON(t, lastRequest(t, h, "POST", "/v1/saved-formulas").Body, recordedBody(t, "saved_create"))
	contains(t, out, "Saved formula    "+savedID, "(made from its params)", "Schedule         weekdays at 07:30 Europe/London; next run", "Notify           digest")
	contains(t, errs, "enrichednow saved run "+savedID)
	out, _ = h.ok("saved", "create", "job-order-hunter", "--params-json", `{"job_titles": ["Nurse"], "locations": ["Texas"]}`, "--schedule", "weekly@08:00",
		"--day", "fri", "--monthly-unit-cap", "20000", "--unit-cap", "500", "--name", "Nurses", "--json")
	sameJSON(t, lastRequest(t, h, "POST", "/v1/saved-formulas").Body, map[string]any{"formula_id": "job-order-hunter",
		"params": map[string]any{"job_titles": []any{"Nurse"}, "locations": []any{"Texas"}}, "name": "Nurses", "unit_cap": float64(500),
		"monthly_unit_cap": float64(20000), "schedule": map[string]any{"cadence": "weekly", "local_time": "08:00", "timezone": "UTC", "day_of_week": float64(4)}})
	if !json.Valid([]byte(out)) {
		t.Errorf("--json:\n%s", out)
	}
	for _, bad := range [][]string{{"--schedule", "fortnightly@07:30"}, {"--schedule", "daily"}, {"--schedule", "daily@25:00"},
		{"--schedule", "daily@07:30", "--day", "mon"}, {"--schedule", "monthly@07:30", "--day", "32"}, {"--timezone", "UTC"}} {
		_, errs, code := h.run(append([]string{"saved", "create", "job-order-hunter", "--params-json", "{}"}, bad...)...)
		if code != exitUsage {
			t.Errorf("%v: exit %d %s", bad, code, errs)
		}
	}

	out, errs = h.ok("saved", "run", savedID, "--include-seen", "-o", "jsonl")
	if strings.Count(out, "\n") != 4 {
		t.Errorf("rows:\n%s", out)
	}
	contains(t, errs, "Formula run frun_000000000000000000000004 ended partial")
	if start := lastRequest(t, h, "POST", "/v1/saved-formulas/"+savedID+"/runs"); start.Body != `{"include_seen":true}` || start.Query != "wait=5" {
		t.Errorf("the start: %+v", start)
	}

	out, errs = h.ok("saved", "pause", savedID)
	contains(t, out, "Status           paused (manual)", "Last run         frun_000000000000000000000004 partial, 4 new rows")
	contains(t, errs, "enrichednow saved resume "+savedID)
	out, _ = h.ok("saved", "resume", savedID)
	contains(t, out, "Status           active")

	out, _ = h.ok("saved", "rows", savedID, "--since", "2026-11-25T09:02:55.25Z", "--order", "desc")
	contains(t, out, "Corvid", "Java Consultant")
	if q := firstOfLast(t, h, 2, "GET", "/v1/saved-formulas/"+savedID+"/rows").Query; q != "limit=1000&order=desc&since=2026-11-25T09%3A02%3A55.250000%2B00%3A00" {
		t.Errorf("query = %s", q)
	}
	h.ok("saved", "rows", savedID, "--since", "24h", "-o", "jsonl")
	if q := lastRequest(t, h, "GET", "/v1/saved-formulas/"+savedID+"/rows").Query; !strings.Contains(q, "since=") {
		t.Errorf("query = %s", q)
	}
	file := filepath.Join(t.TempDir(), "new.csv")
	_, errs = h.ok("saved", "rows", savedID, "--fields", "company_name,job_title,job_url", "--out", file)
	data, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(data), "company_name,job_title,job_url\r\n") {
		t.Errorf("file:\n%s", data)
	}
	contains(t, errs, "Wrote 4 rows to "+file)
	out, _ = h.ok("saved", "rows", savedID, "--format", "ndjson")
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 2 || !json.Valid([]byte(lines[1])) {
		t.Errorf("ndjson:\n%s", out)
	}
	if _, _, code := h.run("saved", "rows", savedID, "--since", "last week"); code != exitUsage {
		t.Errorf("a --since that is no time: exit %d", code)
	}

	out, _ = h.ok("saved", "duplicate", fakeapi.ArchivedSavedFormula, "--name", "Java in London (copy)", "--copy-seen")
	contains(t, out, "sf_000000000000000000000003", "Java in London (copy)", "none: it runs only by hand")
	if body := lastRequest(t, h, "POST", "/v1/saved-formulas/"+fakeapi.ArchivedSavedFormula+"/duplicate").Body; body != `{"copy_seen":true,"name":"Java in London (copy)"}` {
		t.Errorf("body = %s", body)
	}
}

func TestParseScheduleAndSince(t *testing.T) {
	s, err := parseSchedule("Monthly@9:05", "", "15")
	if err != nil || s.Cadence != "monthly" || s.LocalTime != "09:05" || s.Timezone != "UTC" || *s.DayOfMonth != 15 || s.DayOfWeek != nil {
		t.Errorf("%+v %v", s, err)
	}
	s, err = parseSchedule("weekly@23:59", "Asia/Tokyo", "Sunday")
	if err != nil || *s.DayOfWeek != 6 || s.Timezone != "Asia/Tokyo" {
		t.Errorf("%+v %v", s, err)
	}
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	for value, want := range map[string]time.Time{
		"24h":                  now.Add(-24 * time.Hour),
		"7d":                   now.AddDate(0, 0, -7),
		"2026-11-01":           time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),
		"2026-11-25T09:00:00Z": time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC),
		"":                     {},
	} {
		if got, err := parseSince(value, now); err != nil || !got.Equal(want) {
			t.Errorf("%q: %v %v", value, got, err)
		}
	}
	if text := scheduleText(nil); text != "none: it runs only by hand" {
		t.Error(text)
	}
}

func TestFormulaCompletion(t *testing.T) {
	h := newHarness(t)
	out, _ := h.ok("__complete", "formulas", "get", "")
	contains(t, out, "job-order-hunter\tJob Order Hunter", "posting-watch")
	out, _ = h.ok("__complete", "formulas", "rows", "x", "--format", "")
	contains(t, out, "csv", "ndjson", "json")
	out, _ = h.ok("__complete", "saved", "create", "job-order-hunter", "--notify", "")
	contains(t, out, "digest", "each_run")
}
