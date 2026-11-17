// Package fakeapi is a stand-in for the EnrichedNow API, for the tests of the command-line tool. It answers the
// documented endpoints with the documented shapes and keeps runs in memory; it scrapes nothing.
package fakeapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	// Key is the API key the fake accepts.
	Key = "en_live_Zk3uTq8vWx1yB2cD4eF6gH8jK0lM2nP4qR6s"
	// KeyWithoutData is accepted too, by an account that has not been given Datasets.
	KeyWithoutData = "en_live_NoData00aWx1yB2cD4eF6gH8jK0lM2nP4qR6"
)

// Request is one request the fake was sent.
type Request struct {
	Method, Path, Query, Authorization, UserAgent, Body string
}

// Server is a running fake.
type Server struct {
	*httptest.Server
	// PollsUntilDone is how many times a run is read as running before it has ended. The default is 2.
	PollsUntilDone int

	mu       sync.Mutex
	requests []Request
	runs     map[string]*run
	order    []string // run ids, oldest first
}

type run struct {
	id, scraper, status, reason string
	input                       json.RawMessage
	records                     []string
	units                       int
	polls                       int
	ends                        string // the status it will end with
	endReason                   string
	cancelAsked                 bool
}

// New starts a fake with a few runs already in its history.
func New() *Server {
	s := &Server{PollsUntilDone: 2, runs: map[string]*run{}}
	s.add(&run{id: "run_25d40cfbc237d0caee2fc583", scraper: "ats.board", status: "unsupported", reason: "render_required",
		input: json.RawMessage(`{"board_url":"https://jobs.dayforcehcm.com/en-US/acme/CANDIDATEPORTAL"}`)})
	s.add(&run{id: "run_b18944845743ccb58e932c28", scraper: "ats.board", status: "failed", reason: "adapter_failed",
		input: json.RawMessage(`{"board_url":"https://boards.greenhouse.io/acme-board-that-does-not-exist"}`)})
	s.add(&run{id: "run_841742f37553189ff9c27ba1", scraper: "ats.board", status: "ok", units: 1, records: jobRecords,
		input: json.RawMessage(`{"board_url":"https://boards.greenhouse.io/acme"}`)})
	s.Server = httptest.NewServer(http.HandlerFunc(s.serve))
	return s
}

func (s *Server) add(r *run) {
	s.runs[r.id] = r
	s.order = append(s.order, r.id)
}

// Requests returns what the fake was sent so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Count returns how many requests matched a method and a path.
func (s *Server) Count(method, path string) int {
	n := 0
	for _, r := range s.Requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

var jobRecords = []string{
	`{"title":"Senior Software Engineer","company_name":"Acme","company_domain":null,"location":{"raw":"Austin, TX","city":"Austin","region":"TX","country":"United States","remote":false},"employment_type":"Full-time","posted_at":"2026-11-03","valid_through":null,"salary":null,"description":"We are looking for an engineer to work on our ingestion pipeline…","apply_url":"https://boards.greenhouse.io/acme/jobs/4567890","page_url":"https://boards.greenhouse.io/acme/jobs/4567890","ats":"greenhouse","external_id":"4567890","fetched_at":"2026-11-17T09:30:01Z"}`,
	`{"title":"Product Designer","company_name":"Acme","company_domain":null,"location":{"raw":"Remote, US","city":null,"region":null,"country":"United States","remote":true},"employment_type":"Full-time","posted_at":"2026-11-05","valid_through":null,"salary":{"raw":"$120,000 - $150,000","min":120000,"max":150000,"currency":"USD","period":"year"},"description":"Design the things, \"carefully\".","apply_url":"https://boards.greenhouse.io/acme/jobs/4567891","page_url":"https://boards.greenhouse.io/acme/jobs/4567891","ats":"greenhouse","external_id":"4567891","fetched_at":"2026-11-17T09:30:01Z"}`,
	`{"title":"Account Executive, Mid-Market","company_name":"Acme","company_domain":null,"location":{"raw":"London, England","city":"London","region":"England","country":null,"remote":false},"employment_type":null,"posted_at":null,"valid_through":null,"salary":null,"description":null,"apply_url":"https://boards.greenhouse.io/acme/jobs/4567892","page_url":"https://boards.greenhouse.io/acme/jobs/4567892","ats":"greenhouse","external_id":"4567892","fetched_at":"2026-11-17T09:30:02Z"}`,
}

const scrapers = `[{"id":"ats.board","version":1,"title":"ATS job board","summary":"Job openings from one board of an applicant tracking system (Greenhouse, Lever, Ashby, Workday and others).","mode":"job","billing_unit":"request","data_class":"public","availability":"public"},` +
	`{"id":"careers.company","version":1,"title":"Company careers site","summary":"Job openings from a company's own careers site: give its domain or its careers page.","mode":"job","billing_unit":"request","data_class":"public","availability":"public"},` +
	`{"id":"linkedin.jobs_search","version":1,"title":"LinkedIn job search","summary":"Job postings that match a search on LinkedIn.","mode":"job","billing_unit":"record","data_class":"public","availability":"public"}]`

const atsBoard = `{"id":"ats.board","version":1,"title":"ATS job board","summary":"Job openings from one board of an applicant tracking system (Greenhouse, Lever, Ashby, Workday and others).","mode":"job","billing_unit":"request","data_class":"public","availability":"public",` +
	`"input_schema":{"additionalProperties":false,"properties":{"board_url":{"description":"The board's address, e.g. https://boards.greenhouse.io/acme.","maxLength":2000,"title":"Board Url","type":"string"},"company_name":{"anyOf":[{"maxLength":200,"type":"string"},{"type":"null"}],"default":null,"description":"The employer's name, when the board does not give it.","title":"Company Name"},"max_detail_pages":{"default":30,"description":"Posting pages read for details, at most.","maximum":200,"minimum":0,"title":"Max Detail Pages","type":"integer"}},"required":["board_url"],"title":"AtsBoardInput","type":"object"},` +
	`"record_schema":{"$defs":{"JobLocation":{"properties":{"raw":{"anyOf":[{"type":"string"},{"type":"null"}]}},"type":"object"}},"properties":{"title":{"title":"Title","type":"string"},"company_name":{"anyOf":[{"type":"string"},{"type":"null"}],"default":null,"title":"Company Name"},"location":{"anyOf":[{"$ref":"#/$defs/JobLocation"},{"type":"null"}],"default":null},"posted_at":{"anyOf":[{"format":"date","type":"string"},{"type":"null"}],"default":null,"title":"Posted At"},"apply_url":{"anyOf":[{"type":"string"},{"type":"null"}],"default":null,"title":"Apply Url"},"page_url":{"title":"Page Url","type":"string"},"fetched_at":{"format":"date-time","title":"Fetched At","type":"string"}},"required":["title","page_url","fetched_at"],"title":"CareersJobPosting","type":"object"}}`

const company = `{"domain":"figma.com","name":"Figma","names":[{"name":"Figma","first_seen_at":"2026-11-10T22:37:56Z","last_seen_at":"2026-11-16T22:38:01.152673Z"}],"careers_url":"https://boards.greenhouse.io/figma","ats":"greenhouse","first_seen_at":"2026-11-10T22:37:56Z","last_seen_at":"2026-11-16T22:38:01.152673Z","last_read":{"at":"2026-11-16T22:38:01.152673Z","status":"complete","last_complete_at":"2026-11-16T22:38:01.152673Z"},"postings":{"open":140,"closed":0}}`

func posting(id, title, location string) string {
	return fmt.Sprintf(`{"id":%q,"title":%q,"title_clean":%q,"company":{"domain":"figma.com","name":"Figma"},"location":{"raw":%q,"city":null,"region":null,"country":null,"country_code":null,"remote":false},"employment_type":null,"salary":null,"posted_at":"2026-06-04","valid_through":null,"seniority":"manager","function":null,"url":"https://boards-api.greenhouse.io/v1/boards/figma/jobs?content=true","apply_url":"https://boards.greenhouse.io/figma/jobs/6008343004","source":{"name":"greenhouse","provenance":"feed","trusted":true,"external_id":"6008343004"},"status":"open","first_seen_at":"2026-11-10T22:37:56Z","last_seen_at":"2026-11-16T22:38:01.152673Z","closed_at":null`,
		id, title, title, location)
}

var postings = []string{
	posting("job_2a8c2e32f38fa96099cc53b2", "Voice of the Customer Program Manager", "San Francisco, CA") + "}",
	posting("job_fffb79446c3b7279ca528b04", "Account Executive, Mid-Market", "London, England") + "}",
	posting("job_0c1d2e3f4a5b6c7d8e9f0a1b", "Data Engineer", "New York, NY") + "}",
}

func (r *run) json() string {
	reason, diagnostics, started, finished := "null", "null", "null", "null"
	if r.reason != "" {
		reason = strconv.Quote(r.reason)
	}
	if r.status != "queued" {
		started = `"2026-11-16T22:37:50.178331Z"`
	}
	if r.status != "queued" && r.status != "running" {
		finished = `"2026-11-16T22:37:51.066313Z"`
		diagnostics = `{"requests":1,"bytes":259214,"wall_ms":885,"facts":{"careers_url":"https://boards.greenhouse.io/acme","ats":"greenhouse","pages_read":1}}`
	}
	return fmt.Sprintf(`{"id":%q,"scraper_id":%q,"scraper_version":1,"status":%q,"reason":%s,"input":%s,"record_count":%d,"units":%d,"diagnostics":%s,"created_at":"2026-11-16T22:37:50.170950Z","started_at":%s,"finished_at":%s}`,
		r.id, r.scraper, r.status, reason, r.input, len(r.records), r.units, diagnostics, started, finished)
}

func fail(w http.ResponseWriter, status int, code, message, details string) {
	if details == "" {
		details = "{}"
	}
	write(w, status, fmt.Sprintf(`{"error":{"code":%q,"message":%q,"details":%s}}`, code, message, details))
}

func write(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	io.WriteString(w, body)
}

func cursorOf(n int) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(n)))
}

func offsetOf(cursor string) (int, bool) {
	if cursor == "" {
		return 0, true
	}
	data, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(data))
	return n, err == nil && n >= 0
}

// page cuts a list by the request's limit and cursor, and writes {"<key>": [...], "next_cursor": ...}.
func page(w http.ResponseWriter, req *http.Request, key string, items []string, defaultLimit, maxLimit int) {
	limit := defaultLimit
	if raw := req.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			fail(w, 422, "invalid_input", "The request is not valid.", fmt.Sprintf(`{"fields":[{"path":"limit","message":"Input should be between 1 and %d"}]}`, maxLimit))
			return
		}
		limit = n
	}
	offset, ok := offsetOf(req.URL.Query().Get("cursor"))
	if !ok {
		fail(w, 422, "invalid_input", "This is not a cursor this API gave out.", `{"fields":[{"path":"cursor","message":"This is not a cursor this API gave out."}]}`)
		return
	}
	offset = min(offset, len(items))
	end := min(offset+limit, len(items))
	next := "null"
	if end < len(items) {
		next = strconv.Quote(cursorOf(end))
	}
	write(w, 200, fmt.Sprintf(`{%q:[%s],"next_cursor":%s}`, key, strings.Join(items[offset:end], ","), next))
}

func (s *Server) serve(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{Method: req.Method, Path: req.URL.Path, Query: req.URL.RawQuery,
		Authorization: req.Header.Get("Authorization"), UserAgent: req.Header.Get("User-Agent"), Body: string(body)})

	path := req.URL.Path
	switch {
	case req.Method == "GET" && path == "/v1/health":
		write(w, 200, `{"status":"ok"}`)
		return
	case req.Method == "GET" && path == "/v1/scrapers":
		write(w, 200, `{"scrapers":`+scrapers+`}`)
		return
	case req.Method == "GET" && path == "/v1/scrapers/ats.board":
		write(w, 200, atsBoard)
		return
	case req.Method == "GET" && strings.HasPrefix(path, "/v1/scrapers/") && !strings.Contains(strings.TrimPrefix(path, "/v1/scrapers/"), "/"):
		fail(w, 404, "not_found", fmt.Sprintf("There is no scraper '%s'.", strings.TrimPrefix(path, "/v1/scrapers/")), "")
		return
	}

	auth := req.Header.Get("Authorization")
	switch {
	case auth == "":
		fail(w, 401, "unauthorized", "Send an API key (Authorization: Bearer <key>) or log in.", "")
		return
	case auth != "Bearer "+Key && auth != "Bearer "+KeyWithoutData:
		fail(w, 401, "unauthorized", "This API key is not valid.", "")
		return
	}

	parts := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case req.Method == "POST" && len(parts) == 4 && parts[1] == "scrapers" && parts[3] == "runs":
		s.start(w, parts[2], body)
	case req.Method == "GET" && path == "/v1/runs":
		var items []string
		for i := len(s.order) - 1; i >= 0; i-- { // newest first
			r := s.runs[s.order[i]]
			q := req.URL.Query()
			if (q.Get("status") == "" || q.Get("status") == r.status) && (q.Get("scraper_id") == "" || q.Get("scraper_id") == r.scraper) {
				items = append(items, r.json())
			}
		}
		page(w, req, "runs", items, 20, 100)
	case len(parts) >= 3 && parts[1] == "runs":
		r, ok := s.runs[parts[2]]
		if !ok {
			fail(w, 404, "not_found", fmt.Sprintf("There is no run '%s'.", parts[2]), "")
			return
		}
		switch {
		case req.Method == "GET" && len(parts) == 3:
			s.advance(r)
			write(w, 200, r.json())
		case req.Method == "GET" && len(parts) == 4 && parts[3] == "records":
			page(w, req, "records", r.records, 100, 1000)
		case req.Method == "POST" && len(parts) == 4 && parts[3] == "cancel":
			if r.status == "queued" {
				r.status, r.reason = "cancelled", "cancelled"
			} else if r.status == "running" {
				r.cancelAsked = true
			}
			write(w, 200, r.json())
		default:
			fail(w, 404, "not_found", "There is nothing at this address.", "")
		}
	case req.Method == "GET" && path == "/v1/usage":
		s.usage(w, req)
	case req.Method == "GET" && strings.HasPrefix(path, "/v1/data/"):
		if auth == "Bearer "+KeyWithoutData {
			fail(w, 403, "forbidden", "The datasets product is by early access, and this account does not have it. Ask us for access.", "")
			return
		}
		s.data(w, req, parts[2:])
	default:
		fail(w, 404, "not_found", "There is nothing at this address.", "")
	}
}

// start creates a run. What the board address contains decides how the run goes, so that a test can ask for
// each outcome.
func (s *Server) start(w http.ResponseWriter, scraper string, body []byte) {
	if scraper != "ats.board" {
		fail(w, 404, "not_found", fmt.Sprintf("There is no scraper '%s'.", scraper), "")
		return
	}
	var request struct {
		Input map[string]json.RawMessage `json:"input"`
	}
	if json.Unmarshal(body, &request) != nil || request.Input == nil {
		fail(w, 422, "invalid_input", "The request is not valid.", `{"fields":[{"path":"input","message":"Field required"}]}`)
		return
	}
	var fields []string
	if _, ok := request.Input["board_url"]; !ok {
		fields = append(fields, `{"path":"input.board_url","message":"Field required"}`)
	}
	if raw, ok := request.Input["max_detail_pages"]; ok {
		if n, err := strconv.Atoi(string(raw)); err != nil {
			fields = append(fields, `{"path":"input.max_detail_pages","message":"Input should be a valid integer"}`)
		} else if n > 200 {
			fields = append(fields, `{"path":"input.max_detail_pages","message":"Input should be less than or equal to 200"}`)
		}
	}
	names := make([]string, 0, len(request.Input))
	for name := range request.Input {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name != "board_url" && name != "company_name" && name != "max_detail_pages" {
			fields = append(fields, fmt.Sprintf(`{"path":"input.%s","message":"Extra inputs are not permitted"}`, name))
		}
	}
	if len(fields) > 0 {
		fail(w, 422, "invalid_input", "The input is not valid.", `{"fields":[`+strings.Join(fields, ",")+`]}`)
		return
	}
	var board string
	if json.Unmarshal(request.Input["board_url"], &board) != nil {
		fail(w, 422, "invalid_input", "The input is not valid.", `{"fields":[{"path":"input.board_url","message":"Input should be a valid string"}]}`)
		return
	}
	switch {
	case strings.Contains(board, "no-allowance"):
		fail(w, 402, "quota_exceeded", "This month's units are used up.", `{"used_units":1000,"allowance_units":1000}`)
		return
	case strings.Contains(board, "too-many"):
		fail(w, 429, "too_many_runs", "The account already has as many runs in progress as it may; wait for one to end.", `{"active_runs":2,"max_concurrent_runs":2}`)
		return
	case strings.Contains(board, "server-error"):
		fail(w, 500, "internal", "Something went wrong on our side.", "")
		return
	}
	input, _ := json.Marshal(request.Input)
	r := &run{id: fmt.Sprintf("run_%024x", len(s.order)+1), scraper: scraper, input: input, status: "running", ends: "ok"}
	switch {
	case strings.Contains(board, "does-not-exist"):
		r.status, r.reason = "failed", "adapter_failed"
	case strings.Contains(board, "dayforce"):
		r.status, r.reason = "unsupported", "render_required"
	case strings.Contains(board, "partial"):
		r.ends, r.endReason = "partial", "render_required"
	case strings.Contains(board, "instant"):
		r.status, r.records, r.units = "ok", jobRecords, 1
	case strings.Contains(board, "never-ends"):
		r.ends = ""
	case strings.Contains(board, "empty"):
		r.status, r.units = "ok", 1
	}
	s.add(r)
	status := 202
	if r.status != "running" {
		status = 200
	}
	write(w, status, r.json())
}

// advance moves a running run one poll closer to its end.
func (s *Server) advance(r *run) {
	if r.status != "running" {
		return
	}
	if r.cancelAsked {
		r.status, r.reason, r.records = "cancelled", "cancelled", jobRecords[:1]
		return
	}
	r.polls++
	if r.ends == "" || r.polls < s.PollsUntilDone {
		return
	}
	r.status, r.reason, r.units = r.ends, r.endReason, 1
	r.records = jobRecords
	if r.ends == "partial" {
		r.records = jobRecords[:2]
	}
}

func (s *Server) usage(w http.ResponseWriter, req *http.Request) {
	q := req.URL.Query()
	from, to := q.Get("from"), q.Get("to")
	if from == "" {
		from = "2026-11-01"
	}
	if to == "" {
		to = "2026-11-03"
	}
	if from > to {
		fail(w, 422, "invalid_input", "The period starts after it ends.", `{"fields":[{"path":"from","message":"The period starts after it ends."}]}`)
		return
	}
	allowance := 1000
	if q.Get("product") == "data" {
		allowance = 5000
	}
	rows := `{"key":"2026-11-01","units":0,"runs":0},{"key":"2026-11-02","units":4,"runs":3},{"key":"2026-11-03","units":9,"runs":8}`
	switch q.Get("group_by") {
	case "scraper":
		rows = `{"key":"data.postings.list","units":7,"runs":5},{"key":"ats.board","units":4,"runs":4},{"key":"data.companies.get","units":2,"runs":2}`
	case "product":
		rows = `{"key":"scrapers","units":4,"runs":4},{"key":"data","units":9,"runs":7}`
	case "", "day":
	default:
		fail(w, 422, "invalid_input", "The request is not valid.", `{"fields":[{"path":"group_by","message":"Input should be 'day', 'scraper' or 'product'"}]}`)
		return
	}
	write(w, 200, fmt.Sprintf(`{"period":{"from":%q,"to":%q},"total_units":13,"allowance_units":%d,"rows":[%s]}`, from, to, allowance, rows))
}

func (s *Server) data(w http.ResponseWriter, req *http.Request, parts []string) {
	q := req.URL.Query()
	matching := func() []string {
		var out []string
		for _, p := range postings {
			if title := strings.ToLower(q.Get("title")); title == "" || strings.Contains(strings.ToLower(p), title) {
				out = append(out, p)
			}
		}
		return out
	}
	switch {
	case len(parts) == 1 && parts[0] == "companies":
		items := []string{company}
		if query := q.Get("q"); query != "" && !strings.Contains("figma.com", strings.ToLower(query)) {
			items = nil
		}
		page(w, req, "companies", items, 50, 200)
	case len(parts) == 2 && parts[0] == "companies":
		if parts[1] != "figma.com" {
			fail(w, 404, "not_found", fmt.Sprintf("There is no company '%s' in the dataset.", parts[1]), "")
			return
		}
		write(w, 200, company)
	case len(parts) == 3 && parts[0] == "companies" && parts[2] == "postings":
		if parts[1] != "figma.com" {
			fail(w, 404, "not_found", fmt.Sprintf("There is no company '%s' in the dataset.", parts[1]), "")
			return
		}
		page(w, req, "postings", matching(), 50, 200)
	case len(parts) == 1 && parts[0] == "postings":
		if q.Get("title") == "unavailable" {
			fail(w, 503, "data_unavailable", "The dataset cannot be reached at the moment. Try again shortly.", "")
			return
		}
		page(w, req, "postings", matching(), 50, 200)
	case len(parts) == 2 && parts[0] == "postings":
		for _, p := range postings {
			if strings.Contains(p, `"id":"`+parts[1]+`"`) {
				write(w, 200, strings.TrimSuffix(p, "}")+`,"description":"Figma is growing our team of passionate creatives and builders on a mission to make design accessible to all.","classification":{"seniority_rule":"manager","function_rule":null},"history":{"events":[{"kind":"first_seen","at":"2026-11-10T22:37:56Z"},{"kind":"closed","at":"2026-11-13T22:37:56Z"},{"kind":"reopened","at":"2026-11-15T22:37:56Z"}],"changes":[],"sightings":{"count":3,"recent":["2026-11-16T22:38:01.152673Z"]},"reopened_count":1}}`)
				return
			}
		}
		fail(w, 404, "not_found", fmt.Sprintf("There is no posting '%s'.", parts[1]), "")
	default:
		fail(w, 404, "not_found", "There is nothing at this address.", "")
	}
}
