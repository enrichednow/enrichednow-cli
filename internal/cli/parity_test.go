package cli

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const instantBoard = "board_url=https://boards.greenhouse.io/instant"

func TestRunSendsAnIdempotencyKeyAndAWebhookURL(t *testing.T) {
	h := newHarness(t)
	first, _ := h.ok("run", "ats.board", "-i", instantBoard, "--idempotency-key", "acme-2026-11-20", "--webhook-url", "https://example.com/hook", "--no-wait")
	starts := []string{}
	for _, r := range h.api.Requests() {
		if r.Method == "POST" {
			starts = append(starts, r.IdempotencyKey)
			if !strings.Contains(r.Body, `"webhook_url":"https://example.com/hook"`) {
				t.Errorf("the webhook URL was not sent: %s", r.Body)
			}
		}
	}
	if len(starts) != 1 || starts[0] != "acme-2026-11-20" {
		t.Fatalf("keys sent: %v", starts)
	}
	// the same key again gives the same run back, not a second one
	again, _ := h.ok("run", "ats.board", "-i", instantBoard, "--idempotency-key", "acme-2026-11-20", "--no-wait")
	if first != again {
		t.Errorf("a repeated key started another run: %q then %q", first, again)
	}
	// without one, the tool makes a key for each start
	h.ok("run", "ats.board", "-i", instantBoard, "--no-wait")
	h.ok("run", "ats.board", "-i", instantBoard, "--no-wait")
	var made []string
	for _, r := range h.api.Requests() {
		if r.Method == "POST" && strings.HasPrefix(r.IdempotencyKey, "cli-") {
			made = append(made, r.IdempotencyKey)
		}
	}
	if len(made) != 2 || made[0] == made[1] {
		t.Errorf("keys made: %v", made)
	}
	_, errs, code := h.run("run", "ats.board", "-i", instantBoard, "--idempotency-key", "has space")
	if code != exitUsage || !strings.Contains(errs, "--idempotency-key") {
		t.Errorf("exit %d: %s", code, errs)
	}
}

func TestRecordsAsTheAPIWritesThem(t *testing.T) {
	h := newHarness(t)
	const runID = "run_841742f37553189ff9c27ba1"
	out, _ := h.ok("runs", "records", runID, "--format", "csv")
	rows, err := csv.NewReader(strings.NewReader(out)).ReadAll()
	if err != nil || len(rows) != 4 || rows[0][0] != "title" || rows[1][0] != "Senior Software Engineer" {
		t.Fatalf("csv: %v %q", err, rows)
	}
	if strings.Count(out, "title,company_name") != 1 {
		t.Errorf("the header row should be written once:\n%s", out)
	}
	var asked []string
	for _, r := range h.api.Requests() {
		if strings.HasSuffix(r.Path, "/records") {
			asked = append(asked, r.Query+" "+r.Accept)
		}
	}
	if len(asked) != 1 || asked[0] != "format=csv&limit=1000 text/csv" {
		t.Errorf("asked %v", asked)
	}
	nd, _ := h.ok("runs", "records", runID, "--format", "ndjson")
	if lines := strings.Split(strings.TrimSpace(nd), "\n"); len(lines) != 3 || !json.Valid([]byte(lines[0])) {
		t.Errorf("ndjson:\n%s", nd)
	}
	file := filepath.Join(t.TempDir(), "jobs.csv")
	_, errs := h.ok("run", "ats.board", "-i", instantBoard, "--format", "csv", "--out", file)
	data, _ := os.ReadFile(file)
	if !strings.HasPrefix(string(data), "title,company_name") || !strings.Contains(errs, "Wrote 3 records to") {
		t.Errorf("file %q, stderr %s", data, errs)
	}
	for _, bad := range [][]string{{"--format", "xml"}, {"--format", "csv", "-o", "json"}, {"--format", "csv", "--json"}} {
		if _, _, code := h.run(append([]string{"runs", "records", runID}, bad...)...); code != exitUsage {
			t.Errorf("%v: exit %d", bad, code)
		}
	}
}

func TestCSVKeepsAFormulaAsText(t *testing.T) {
	var buf bytes.Buffer
	rows := []json.RawMessage{json.RawMessage(`{"a":"=HYPERLINK(\"x\")","b":"+1","c":"-2","d":"@SUM(A1)","e":-5,"f":"\tx","g":"  =1","h":"plain"}`)}
	if err := writeCSV(&buf, rows); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(buf.String(), "\r\n") {
		t.Errorf("lines should end CRLF, as the API writes them: %q", buf.String())
	}
	got, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`'=HYPERLINK("x")`, "'+1", "'-2", "'@SUM(A1)", "-5", "'\tx", "'  =1", "plain"}
	if strings.Join(got[1], "|") != strings.Join(want, "|") {
		t.Errorf("got %q\nwant %q", got[1], want)
	}
}

func TestWebhooks(t *testing.T) {
	h := newHarness(t)
	out, errs := h.ok("webhooks", "add", "https://example.com/hooks/enrichednow", "--events", "run.succeeded,run.failed", "--description", "production")
	contains(t, out, "whk_1", "https://example.com/hooks/enrichednow", "run.succeeded, run.failed", "Signing secret: whsec_made_whk_1")
	contains(t, errs, "shown this once")
	out, _ = h.ok("webhooks", "list")
	contains(t, out, "whk_1", "run.succeeded,run.failed", "enabled")
	out, _ = h.ok("webhooks", "update", "whk_1", "--disable", "--json")
	var e map[string]any
	if json.Unmarshal([]byte(out), &e) != nil || e["enabled"] != false {
		t.Errorf("update: %s", out)
	}
	if _, _, code := h.run("webhooks", "update", "whk_1"); code != exitUsage {
		t.Errorf("an update with nothing to change should be a usage error, got %d", code)
	}
	out, _ = h.ok("webhooks", "show", "whk_1")
	contains(t, out, "disabled")
	out, _ = h.ok("webhooks", "rotate-secret", "whk_1")
	contains(t, out, "whsec_rotated_whk_1")
	out, _ = h.ok("webhooks", "run-secret")
	contains(t, out, "whsec_run_secret")
	out, _ = h.ok("webhooks", "deliveries", "--status", "failed")
	contains(t, out, "whd_2", "run.succeeded", "8/8", "500")
	if strings.Contains(out, "whd_1") {
		t.Errorf("--status failed should leave out a delivery that succeeded:\n%s", out)
	}
	out, _ = h.ok("webhooks", "resend", "whd_2")
	contains(t, out, "whd_3")
	_, errs = h.ok("webhooks", "remove", "whk_1")
	contains(t, errs, "Removed whk_1")
	_, errs, code := h.run("webhooks", "show", "whk_1")
	if code != exitError || !strings.Contains(errs, "(not_found)") {
		t.Errorf("exit %d: %s", code, errs)
	}
}

func TestWebhooksVerify(t *testing.T) {
	h := newHarness(t)
	body := `{"id":"evt_1","type":"run.succeeded"}`
	now := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("whsec_test"))
	mac.Write([]byte(now + "." + body))
	signature := "v1=" + hex.EncodeToString(mac.Sum(nil))
	h.stdin = body
	out, _ := h.ok("webhooks", "verify", "--secret", "whsec_test", "--timestamp", now, "--signature", signature)
	contains(t, out, "Verified.")
	h.stdin = body + " "
	_, errs, code := h.run("webhooks", "verify", "--secret", "whsec_test", "--timestamp", now, "--signature", signature)
	if code != exitError || !strings.Contains(errs, "does not verify") {
		t.Errorf("exit %d: %s", code, errs)
	}
	t.Setenv("ENRICHEDNOW_WEBHOOK_SECRET", "whsec_test")
	h.stdin = body
	h.ok("webhooks", "verify", "--timestamp", now, "--signature", signature)
}
