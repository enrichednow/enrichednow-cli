package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/enrichednow/enrichednow-cli/internal/fakeapi"
)

// session runs the built binary against a fake API, one command line after another.
type session struct {
	t          *testing.T
	binary     string
	env        []string
	transcript strings.Builder
	mute       bool // leave the next commands out of the transcript
}

// run runs one command line and returns standard output, standard error and the exit code.
func (s *session) run(stdin string, args ...string) (string, string, int) {
	s.t.Helper()
	cmd := exec.Command(s.binary, args...)
	cmd.Env = s.env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errs bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errs
	code := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			s.t.Fatalf("enrichednow %v: %v", args, err)
		}
		code = exit.ExitCode()
	}
	if !s.mute {
		s.transcript.WriteString("$ enrichednow " + strings.Join(args, " ") + "\n" + errs.String() + out.String())
		if code != 0 {
			s.transcript.WriteString("[exit " + strconv.Itoa(code) + "]\n")
		}
		s.transcript.WriteString("\n")
	}
	for _, text := range []string{out.String(), errs.String()} {
		if strings.Contains(text, fakeapi.Key) {
			s.t.Errorf("enrichednow %v printed the API key", args)
		}
	}
	return out.String(), errs.String(), code
}

func (s *session) expect(wantCode int, stdin string, args ...string) (string, string) {
	s.t.Helper()
	out, errs, code := s.run(stdin, args...)
	if code != wantCode {
		s.t.Fatalf("enrichednow %v exited %d, want %d\nstdout: %s\nstderr: %s", args, code, wantCode, out, errs)
	}
	return out, errs
}

// TestBinary builds the tool and uses it the way a person would, from login to logout, against the fake API.
// Run it with -v to see the session.
func TestBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	goTool, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool on the PATH")
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "enrichednow")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(goTool, "build", "-ldflags", "-X main.version=0.1.0 -X main.commit=abc1234 -X main.date=2026-11-18", "-o", binary, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	api := fakeapi.New()
	defer api.Close()
	config := filepath.Join(dir, "config")
	s := &session{t: t, binary: binary, env: []string{"ENRICHEDNOW_API_URL=" + api.URL, "ENRICHEDNOW_CONFIG_DIR=" + config,
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir}}
	defer func() { t.Log("\n" + s.transcript.String()) }()

	out, _ := s.expect(0, "", "--version")
	if out != "enrichednow 0.1.0 (abc1234, 2026-11-18)\n" {
		t.Errorf("--version: %q", out)
	}

	// Without a key: the catalogue is public, everything else says what to do.
	_, errs := s.expect(4, "", "runs", "list")
	if !strings.Contains(errs, "enrichednow login") {
		t.Errorf("stderr: %s", errs)
	}
	out, _ = s.expect(0, "", "scrapers", "list")
	if !strings.Contains(out, "ats.board") || strings.Contains(out, "\x1b[") {
		t.Errorf("scrapers list (piped, so without colours):\n%s", out)
	}

	out, _ = s.expect(0, fakeapi.Key+"\n", "login")
	if !strings.Contains(out, "Logged in.") {
		t.Errorf("login: %s", out)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(config, "config.json"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Errorf("the saved key: %v, mode %v", err, info.Mode().Perm())
		}
	}

	out, _ = s.expect(0, "", "scrapers", "show", "ats.board")
	if !strings.Contains(out, "board_url") || !strings.Contains(out, "required") {
		t.Errorf("scrapers show:\n%s", out)
	}

	out, errs = s.expect(0, "", "run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/acme")
	if !strings.Contains(out, "Senior Software Engineer") || !strings.Contains(errs, "ended ok: 3 records, 2 units") {
		t.Errorf("run:\nstdout: %s\nstderr: %s", out, errs)
	}

	out, _ = s.expect(0, "", "run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/acme", "--output", "jsonl", "--quiet")
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 || !json.Valid([]byte(lines[0])) {
		t.Errorf("run -o jsonl:\n%s", out)
	}

	csvPath := filepath.Join(dir, "jobs.csv")
	out, _ = s.expect(0, "", "run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/acme", "--out", csvPath)
	if data, err := os.ReadFile(csvPath); err != nil || !strings.HasPrefix(string(data), "title,company_name") || out != "" {
		t.Errorf("run --out: %v\n%s", err, data)
	}

	out, errs = s.expect(3, "", "run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/acme-board-that-does-not-exist")
	if out != "" || !strings.Contains(errs, "ended failed (reason: adapter_failed)") {
		t.Errorf("a failed run:\nstdout: %s\nstderr: %s", out, errs)
	}
	_, errs = s.expect(1, "", "run", "ats.board", "--input", "board=acme")
	if !strings.Contains(errs, "input.board_url: Field required") {
		t.Errorf("invalid input: %s", errs)
	}
	s.expect(2, "", "run")

	id, _ := s.expect(0, "", "run", "ats.board", "--input", "board_url=https://boards.greenhouse.io/acme", "--no-wait", "--quiet")
	id = strings.TrimSpace(id)
	if !strings.HasPrefix(id, "run_") {
		t.Fatalf("--no-wait printed %q", id)
	}
	s.expect(0, "", "runs", "wait", id)
	out, _ = s.expect(0, "", "runs", "get", id, "--json")
	var run map[string]any
	if err := json.Unmarshal([]byte(out), &run); err != nil || run["status"] != "ok" {
		t.Errorf("runs get --json: %v\n%s", err, out)
	}
	out, _ = s.expect(0, "", "runs", "records", id, "--limit", "1", "--json")
	var records []map[string]any
	if err := json.Unmarshal([]byte(out), &records); err != nil || len(records) != 1 {
		t.Errorf("runs records --json: %v\n%s", err, out)
	}
	out, _ = s.expect(0, "", "runs", "list", "--limit", "3")
	if strings.Count(out, "run_") != 3 {
		t.Errorf("runs list:\n%s", out)
	}
	s.expect(0, "", "runs", "cancel", id)

	out, _ = s.expect(0, "", "usage", "--group-by", "scraper")
	if !strings.Contains(out, "13 units used") {
		t.Errorf("usage:\n%s", out)
	}
	out, _ = s.expect(0, "", "data", "postings", "list", "--title", "engineer")
	if !strings.Contains(out, "Data Engineer") {
		t.Errorf("data postings list:\n%s", out)
	}
	s.expect(0, "", "data", "companies", "get", "figma.com")

	// Formulas: what one takes, a preview, then a saved formula and what it delivered.
	out, _ = s.expect(0, "", "formulas", "get", "job-order-hunter")
	if !strings.Contains(out, "job_titles") || !strings.Contains(out, "required") {
		t.Errorf("formulas get:\n%s", out)
	}
	out, errs = s.expect(0, "", "formulas", "run", "job-order-hunter", "--param", "job_titles=Java Developer", "--param", "locations=London", "--preview", "-o", "jsonl")
	if strings.Count(out, "\n") != 4 || !strings.Contains(errs, "ended partial") {
		t.Errorf("formulas run:\nstdout: %s\nstderr: %s", out, errs)
	}
	leads := filepath.Join(dir, "leads.csv")
	s.expect(0, "", "formulas", "rows", "frun_000000000000000000000003", "--out", leads)
	if data, err := os.ReadFile(leads); err != nil || strings.Count(string(data), "company_name,") != 1 {
		t.Errorf("formulas rows --out: %v\n%s", err, data)
	}
	out, _ = s.expect(0, "", "saved", "create", "job-order-hunter", "--param", "job_titles=Java Developer", "--param", "locations=London",
		"--schedule", "weekdays@07:30", "--timezone", "Europe/London")
	if !strings.Contains(out, "weekdays at 07:30 Europe/London") {
		t.Errorf("saved create:\n%s", out)
	}
	s.expect(0, "", "saved", "rows", "sf_000000000000000000000002", "--since", "24h")
	_, errs = s.expect(1, "", "saved", "create", "job-order-hunter", "--params-json", `{"job_titles": ["Nurse"], "locations": ["Texas"]}`, "--schedule", "hourly@00:15")
	if !strings.Contains(errs, "plan_limit_reached") {
		t.Errorf("a schedule the plan does not allow: %s", errs)
	}

	s.mute = true // a completion script is long, and of no interest in the session
	out, _ = s.expect(0, "", "completion", "bash")
	if !strings.Contains(out, "enrichednow") {
		t.Error("completion bash printed no script")
	}
	s.mute = false
	out, _ = s.expect(0, "", "logout")
	if !strings.Contains(out, "Logged out") {
		t.Errorf("logout: %s", out)
	}
	s.expect(4, "", "usage")
}
