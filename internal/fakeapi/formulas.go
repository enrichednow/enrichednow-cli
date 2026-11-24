package fakeapi

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The answers of the formula routes are recorded from the real API: testdata/formulas, one exchange a file.
//
//go:embed testdata/formulas/*.json
var recorded embed.FS

// exchange is one recorded request and its answer.
type exchange struct {
	Request struct {
		Method string            `json:"method"`
		Path   string            `json:"path"`
		Query  map[string]string `json:"query"`
	} `json:"request"`
	Response struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
		Text    *string           `json:"text"`
	} `json:"response"`
}

func loadExchanges() map[string]exchange {
	out := map[string]exchange{}
	names, err := recorded.ReadDir("testdata/formulas")
	if err != nil {
		panic(err)
	}
	for _, entry := range names {
		data, err := recorded.ReadFile("testdata/formulas/" + entry.Name())
		if err != nil {
			panic(err)
		}
		var ex exchange
		if err := json.Unmarshal(data, &ex); err != nil {
			panic(fmt.Sprintf("%s: %v", entry.Name(), err))
		}
		out[strings.TrimSuffix(entry.Name(), ".json")] = ex
	}
	return out
}

// The formula runs and saved formulas the recorded answers are about.
const (
	queuedRun  = "frun_000000000000000000000002" // started by a full run of job-order-hunter: running, then partial
	previewRun = "frun_000000000000000000000003" // a preview: answered ended
	savedRun   = "frun_000000000000000000000004" // a saved formula's run: answered ended
	runToStop  = "frun_000000000000000000000001" // asked to stop, still running
	// ArchivedSavedFormula is a saved formula the fake keeps archived: changing or running it is refused.
	ArchivedSavedFormula = "sf_000000000000000000000009"
	// CappedSavedFormula is a saved formula whose monthly unit cap is reached: running it is refused.
	CappedSavedFormula = "sf_000000000000000000000001"
)

// formulaRun is a formula run the fake knows: what each read of it answers.
type formulaRun struct {
	running, ended json.RawMessage // ended nil: it stays as running says
	reads          int
	cancelAsked    bool
}

func (s *Server) initFormulas() {
	s.exchanges = loadExchanges()
	body := func(name string) json.RawMessage { return s.exchanges[name].Response.Body }
	s.formulaRuns = map[string]*formulaRun{
		runToStop:  {running: body("formula_runs_cancel")},
		queuedRun:  {running: body("formula_runs_get_running"), ended: body("formula_runs_get")},
		previewRun: {running: body("formulas_run")},
		savedRun:   {running: body("saved_run")},
	}
	s.formulaKeys = map[string]string{}
}

// answer writes a recorded answer.
func (s *Server) answer(w http.ResponseWriter, name string) {
	ex, ok := s.exchanges[name]
	if !ok {
		panic("no recorded answer " + name)
	}
	s.answerWith(w, ex, ex.Response.Body)
}

// answerWith writes a recorded answer's status and headers with another body.
func (s *Server) answerWith(w http.ResponseWriter, ex exchange, body []byte) {
	for name, value := range ex.Response.Headers {
		w.Header().Set(name, value)
	}
	w.WriteHeader(ex.Response.Status)
	switch {
	case ex.Response.Text != nil:
		io.WriteString(w, *ex.Response.Text)
	default:
		w.Write(body)
	}
}

// publicFormulas answers the catalogue of formulas, which needs no key. It reports whether it answered.
func (s *Server) publicFormulas(w http.ResponseWriter, req *http.Request, parts []string) bool {
	if req.Method != http.MethodGet || len(parts) < 2 || parts[1] != "formulas" || len(parts) > 3 {
		return false
	}
	if len(parts) == 3 {
		if parts[2] != "job-order-hunter" {
			s.noFormula(w, parts[2])
			return true
		}
		s.answer(w, "formulas_get")
		return true
	}
	// The catalogue, narrowed as the API narrows it.
	q := req.URL.Query()
	var list struct {
		Formulas []map[string]any `json:"formulas"`
	}
	_ = json.Unmarshal(s.exchanges["formulas_list"].Response.Body, &list)
	kept := []map[string]any{}
	for _, f := range list.Formulas {
		text := strings.ToLower(fmt.Sprint(f["title"], " ", f["summary"], " ", f["use_when"]))
		personas, _ := json.Marshal(f["personas"])
		match := (q.Get("category") == "" || f["category"] == q.Get("category")) && (q.Get("status") == "" || f["status"] == q.Get("status")) &&
			(q.Get("persona") == "" || strings.Contains(string(personas), `"`+q.Get("persona")+`"`))
		for _, word := range strings.Fields(strings.ToLower(q.Get("query"))) {
			match = match && strings.Contains(text, word)
		}
		if match {
			kept = append(kept, f)
		}
	}
	data, _ := json.Marshal(map[string]any{"formulas": kept})
	write(w, 200, string(data))
	return true
}

func (s *Server) noFormula(w http.ResponseWriter, id string) {
	if id == "no-such-formula" {
		s.answer(w, "error_formula_not_found")
		return
	}
	fail(w, 404, "formula_not_found", fmt.Sprintf("There is no formula '%s'.", id), "")
}

// formulas answers the formula routes that need a key: starting and reading formula runs, saved formulas, the
// suppression list, the terms and row feedback. It reports whether it answered.
func (s *Server) formulas(w http.ResponseWriter, req *http.Request, parts []string, body []byte) bool {
	if len(parts) < 2 {
		return false
	}
	switch parts[1] {
	case "formulas":
		if req.Method == http.MethodPost && len(parts) == 4 && parts[3] == "estimate" {
			s.estimate(w, parts[2], body)
			return true
		}
		if req.Method == http.MethodPost && len(parts) == 4 && parts[3] == "runs" {
			s.startFormula(w, parts[2], body, req.Header.Get("Idempotency-Key"))
			return true
		}
	case "formula-runs":
		s.formulaRunRoutes(w, req, parts[2:])
		return true
	case "saved-formulas":
		s.savedRoutes(w, req, parts[2:], body)
		return true
	case "suppressions", "attestations", "formula-rows":
		for _, ex := range s.exchanges {
			if ex.Request.Method == req.Method && ex.Request.Path == req.URL.Path {
				s.answerWith(w, ex, ex.Response.Body)
				return true
			}
		}
		fail(w, 404, "not_found", "There is nothing at this address.", "")
		return true
	}
	return false
}

func (s *Server) estimate(w http.ResponseWriter, id string, body []byte) {
	if id != "job-order-hunter" {
		s.noFormula(w, id)
		return
	}
	var sent struct {
		Params map[string]any `json:"params"`
	}
	_ = json.Unmarshal(body, &sent)
	if sent.Params["job_titles"] == nil || sent.Params["locations"] == nil {
		s.answer(w, "formulas_estimate_invalid")
		return
	}
	s.answer(w, "formulas_estimate")
}

// startFormula starts a run of a formula. Only job-order-hunter runs here; retired-formula and data-formula answer
// what the real API answered for them.
func (s *Server) startFormula(w http.ResponseWriter, id string, body []byte, key string) {
	switch id {
	case "retired-formula":
		s.answer(w, "error_formula_unavailable")
		return
	case "data-formula":
		s.answer(w, "error_requirement_missing")
		return
	case "job-order-hunter":
	default:
		s.noFormula(w, id)
		return
	}
	if runID, ok := s.formulaKeys[key]; ok && key != "" {
		w.Header().Set("Idempotency-Replayed", "true")
		write(w, 200, string(s.read(s.formulaRuns[runID])))
		return
	}
	if s.NeedsAttestation {
		s.answer(w, "error_attestation_required")
		return
	}
	var sent struct {
		Params  map[string]any `json:"params"`
		Mode    string         `json:"mode"`
		UnitCap *int           `json:"unit_cap"`
	}
	if json.Unmarshal(body, &sent) != nil || sent.Params == nil {
		fail(w, 422, "invalid_input", "The request is not valid.", `{"fields":[{"path":"params","message":"Field required"}]}`)
		return
	}
	if sent.Params["job_titles"] == nil || sent.Params["locations"] == nil {
		fail(w, 422, "invalid_input", "The parameters are not valid.", `{"fields":[{"path":"params.job_titles","message":"This is required."}]}`)
		return
	}
	if sent.UnitCap != nil && *sent.UnitCap < 20 {
		s.answer(w, "error_unit_cap_too_low")
		return
	}
	runID := queuedRun
	if sent.Mode == "preview" {
		runID = previewRun
		s.answer(w, "formulas_run")
	} else {
		s.formulaRuns[queuedRun].reads, s.formulaRuns[queuedRun].cancelAsked = 0, false
		s.answer(w, "formulas_run_queued")
	}
	if key != "" {
		s.formulaKeys[key] = runID
	}
}

// read is what a read of a formula run answers now: it ends on the PollsUntilDone-th read.
func (s *Server) read(r *formulaRun) json.RawMessage {
	r.reads++
	switch {
	case r.cancelAsked:
		return withFields(r.running, map[string]any{"status": "cancelled", "reason": "cancelled", "finished_at": "2026-11-25T09:01:00.250000Z"})
	case r.ended != nil && r.reads >= s.PollsUntilDone:
		return r.ended
	}
	return r.running
}

// withFields is a JSON object with some fields changed.
func withFields(raw json.RawMessage, fields map[string]any) json.RawMessage {
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for k, v := range fields {
		m[k] = v
	}
	data, _ := json.Marshal(m)
	return data
}

func (s *Server) formulaRunRoutes(w http.ResponseWriter, req *http.Request, parts []string) {
	if len(parts) == 0 {
		if req.Method == http.MethodGet {
			s.answer(w, "formula_runs_list")
			return
		}
		fail(w, 404, "not_found", "There is nothing at this address.", "")
		return
	}
	r, ok := s.formulaRuns[parts[0]]
	if !ok {
		fail(w, 404, "not_found", fmt.Sprintf("There is no formula run '%s'.", parts[0]), "")
		return
	}
	switch {
	case req.Method == http.MethodGet && len(parts) == 1:
		write(w, 200, string(s.read(r)))
	case req.Method == http.MethodPost && len(parts) == 2 && parts[1] == "cancel":
		if parts[0] == runToStop {
			s.answer(w, "formula_runs_cancel")
			return
		}
		current := r.running
		if r.ended != nil && r.reads >= s.PollsUntilDone {
			current = r.ended
		} else if statusOf(r.running) == "running" || statusOf(r.running) == "queued" {
			r.cancelAsked = true
		}
		write(w, 200, string(current))
	case req.Method == http.MethodGet && len(parts) == 2 && parts[1] == "rows":
		s.rows(w, req)
	default:
		fail(w, 404, "not_found", "There is nothing at this address.", "")
	}
}

// rows answers a page of a run's rows from the recorded pages (whichever the run): JSON, CSV or NDJSON, one row per
// company, a page at a time with the cursor "Mg". fields and min_score narrow the JSON pages.
func (s *Server) rows(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	cursor := q.Get("cursor")
	if cursor != "" && cursor != "Mg" {
		fail(w, 422, "invalid_input", "This is not a cursor this API gave out.", `{"fields":[{"path":"cursor","message":"This is not a cursor this API gave out."}]}`)
		return
	}
	page2 := cursor == "Mg"
	switch {
	case q.Get("format") == "csv" && page2:
		s.answer(w, "formula_runs_rows_csv_page2")
	case q.Get("format") == "csv":
		s.answer(w, "formula_runs_rows_csv")
	case q.Get("format") == "ndjson":
		s.answer(w, "formula_runs_rows_ndjson")
	case q.Get("view") == "company":
		s.answer(w, "formula_runs_rows_company")
	default:
		name := "formula_runs_rows"
		if page2 {
			name = "formula_runs_rows_page2"
		}
		ex := s.exchanges[name]
		s.answerWith(w, ex, narrowed(ex.Response.Body, q.Get("fields"), q.Get("min_score")))
	}
}

// narrowed keeps the columns asked for of each row, and the rows scored at least min_score.
func narrowed(page json.RawMessage, fields, minScore string) json.RawMessage {
	if fields == "" && minScore == "" {
		return page
	}
	var doc struct {
		Rows       []map[string]any `json:"rows"`
		NextCursor *string          `json:"next_cursor"`
	}
	_ = json.Unmarshal(page, &doc)
	var least float64
	fmt.Sscan(minScore, &least)
	kept := []map[string]any{}
	for _, row := range doc.Rows {
		if score, _ := row["score"].(float64); minScore != "" && score < least {
			continue
		}
		if fields != "" {
			data, _ := row["data"].(map[string]any)
			only := map[string]any{}
			for _, name := range strings.Split(fields, ",") {
				if v, ok := data[strings.TrimSpace(name)]; ok {
					only[strings.TrimSpace(name)] = v
				}
			}
			row["data"] = only
		}
		kept = append(kept, row)
	}
	data, _ := json.Marshal(map[string]any{"rows": kept, "next_cursor": doc.NextCursor})
	return data
}

// savedRoutes answers /v1/saved-formulas.
func (s *Server) savedRoutes(w http.ResponseWriter, req *http.Request, parts []string, body []byte) {
	switch {
	case len(parts) == 0 && req.Method == http.MethodGet:
		s.savedList(w, req)
		return
	case len(parts) == 0 && req.Method == http.MethodPost:
		s.createSaved(w, body)
		return
	case len(parts) == 1 && parts[0] == "limits" && req.Method == http.MethodGet:
		s.answer(w, "saved_limits")
		return
	}
	if len(parts) == 0 {
		fail(w, 404, "not_found", "There is nothing at this address.", "")
		return
	}
	// Any id of the recorded shape is a saved formula here.
	id := parts[0]
	if !strings.HasPrefix(id, "sf_") || len(id) != len(ArchivedSavedFormula) {
		fail(w, 404, "not_found", fmt.Sprintf("There is no saved formula '%s'.", id), "")
		return
	}
	archived := id == ArchivedSavedFormula
	action := strings.Join(parts[1:], "/")
	changes := req.Method == http.MethodPatch || req.Method == http.MethodPost && action != "duplicate"
	if archived && changes {
		s.answer(w, "error_saved_formula_archived")
		return
	}
	switch {
	case action == "" && req.Method == http.MethodGet && archived:
		s.answer(w, "saved_archive")
	case action == "" && req.Method == http.MethodGet:
		s.answer(w, "saved_get")
	case action == "" && req.Method == http.MethodPatch:
		s.answer(w, "saved_update")
	case action == "" && req.Method == http.MethodDelete:
		s.answer(w, "saved_archive")
	case action == "runs" && req.Method == http.MethodPost:
		key := req.Header.Get("Idempotency-Key")
		if runID, ok := s.formulaKeys[key]; ok && key != "" {
			w.Header().Set("Idempotency-Replayed", "true")
			write(w, 200, string(s.read(s.formulaRuns[runID])))
			return
		}
		if id == CappedSavedFormula {
			s.answer(w, "error_limit_reached")
			return
		}
		s.answer(w, "saved_run")
		if key != "" {
			s.formulaKeys[key] = savedRun
		}
	case action == "pause" && req.Method == http.MethodPost:
		s.answer(w, "saved_pause")
	case action == "resume" && req.Method == http.MethodPost:
		s.answer(w, "saved_resume")
	case action == "duplicate" && req.Method == http.MethodPost:
		s.answer(w, "saved_duplicate")
	case action == "seen/reset" && req.Method == http.MethodPost:
		s.answer(w, "saved_seen_reset")
	case action == "snapshots" && req.Method == http.MethodGet:
		s.answer(w, "saved_snapshots")
	case action == "rows" && req.Method == http.MethodGet:
		s.savedRows(w, req)
	default:
		fail(w, 404, "not_found", "There is nothing at this address.", "")
	}
}

// savedList answers the recorded list, narrowed by formula and status as the API narrows it.
func (s *Server) savedList(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	var list struct {
		Saved []map[string]any `json:"saved_formulas"`
	}
	_ = json.Unmarshal(s.exchanges["saved_list"].Response.Body, &list)
	kept := []map[string]any{}
	for _, item := range list.Saved {
		if (q.Get("formula_id") == "" || item["formula_id"] == q.Get("formula_id")) && (q.Get("status") == "" || item["status"] == q.Get("status")) {
			kept = append(kept, item)
		}
	}
	data, _ := json.Marshal(map[string]any{"saved_formulas": kept, "next_cursor": nil})
	write(w, 200, string(data))
}

func (s *Server) createSaved(w http.ResponseWriter, body []byte) {
	var sent struct {
		FormulaID string         `json:"formula_id"`
		Params    map[string]any `json:"params"`
		Schedule  *struct {
			Cadence string `json:"cadence"`
		} `json:"schedule"`
	}
	if json.Unmarshal(body, &sent) != nil {
		fail(w, 422, "invalid_input", "The request is not valid.", "")
		return
	}
	switch {
	case sent.Schedule != nil && sent.Schedule.Cadence == "hourly":
		s.answer(w, "error_plan_limit_reached")
	case sent.FormulaID != "job-order-hunter":
		s.noFormula(w, sent.FormulaID)
	case s.NeedsAttestation && sent.Schedule != nil:
		s.answer(w, "error_attestation_required")
	default:
		s.answer(w, "saved_create")
	}
}

// savedRows answers a saved formula's rows: the recorded page, then an empty one; the recorded CSV; NDJSON made of
// the recorded page, as the API writes it.
func (s *Server) savedRows(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	switch {
	case q.Get("format") == "csv":
		s.answer(w, "saved_rows_csv")
	case q.Get("format") == "ndjson":
		var page struct {
			Rows []struct {
				Data json.RawMessage `json:"data"`
			} `json:"rows"`
		}
		_ = json.Unmarshal(s.exchanges["saved_rows"].Response.Body, &page)
		var out bytes.Buffer
		for _, row := range page.Rows {
			_ = json.Compact(&out, row.Data)
			out.WriteByte('\n')
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.WriteHeader(200)
		w.Write(out.Bytes())
	case q.Get("cursor") != "":
		write(w, 200, `{"rows":[],"next_cursor":null}`)
	default:
		s.answer(w, "saved_rows")
	}
}

// statusOf is the status of a run, as JSON.
func statusOf(raw json.RawMessage) string {
	var run struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(raw, &run)
	return run.Status
}
