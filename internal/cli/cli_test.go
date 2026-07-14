// Tests for the CLI: dispatch, exit codes, flag validation, and the
// text/JSON renderings of check and plan. The run test at the bottom
// exercises the full loopback path: bind, proxy a real request, shut down
// on context cancel — synchronized by the ready line, never by sleeping.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JaydenCJ/slowlane/internal/proxy"
	"github.com/JaydenCJ/slowlane/internal/scenario"
)

// runCLI executes the CLI in-process and returns (exit, stdout, stderr).
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := Main(context.Background(), args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// writeScenario writes doc to a temp file and returns its path.
func writeScenario(t *testing.T, doc string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.json")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// oneProxy returns a valid scenario with a 0.5-rate 503 rule, seed 42.
func oneProxy(upstream string) string {
	return `{
	  "version": 1,
	  "seed": 42,
	  "proxies": [{
	    "name": "api",
	    "listen": "127.0.0.1:0",
	    "upstream": "` + upstream + `",
	    "rules": [{
	      "name": "brownout",
	      "rate": 0.5,
	      "fault": {"status": 503, "body": "injected outage"}
	    }]
	  }]
	}`
}

func TestVersionSubcommandAndFlagAlias(t *testing.T) {
	for _, arg := range []string{"version", "--version", "-v"} {
		code, out, _ := runCLI(t, arg)
		if code != ExitOK || out != "slowlane 0.1.0\n" {
			t.Fatalf("%s: code=%d out=%q", arg, code, out)
		}
	}
}

func TestHelpPrintsUsageAndExitsZero(t *testing.T) {
	code, out, _ := runCLI(t, "help")
	if code != ExitOK || !strings.Contains(out, "slowlane run") {
		t.Fatalf("help: code=%d out=%q", code, out)
	}
}

func TestMissingAndUnknownCommandsAreUsageErrors(t *testing.T) {
	code, _, errOut := runCLI(t)
	if code != ExitUsage || !strings.Contains(errOut, "Usage") {
		t.Fatalf("no args: code=%d err=%q", code, errOut)
	}
	code, _, errOut = runCLI(t, "frobnicate")
	if code != ExitUsage || !strings.Contains(errOut, `unknown command "frobnicate"`) {
		t.Fatalf("unknown cmd: code=%d err=%q", code, errOut)
	}
}

func TestCheckAcceptsValidScenario(t *testing.T) {
	path := writeScenario(t, oneProxy("http://127.0.0.1:9"))
	code, out, _ := runCLI(t, "check", path)
	if code != ExitOK {
		t.Fatalf("check exit = %d", code)
	}
	if !strings.Contains(out, "OK (version 1, seed 42, 1 proxy, 1 rule)") {
		t.Fatalf("check summary wrong: %q", out)
	}
	if !strings.Contains(out, "proxy api") || !strings.Contains(out, "-> http://127.0.0.1:9") {
		t.Fatalf("check proxy line wrong: %q", out)
	}
}

func TestCheckJSONFormat(t *testing.T) {
	path := writeScenario(t, oneProxy("http://127.0.0.1:9"))
	code, out, _ := runCLI(t, "check", "--format", "json", path)
	if code != ExitOK {
		t.Fatalf("check exit = %d", code)
	}
	var got struct {
		OK      bool   `json:"ok"`
		Seed    uint64 `json:"seed"`
		Proxies []struct {
			Name  string `json:"name"`
			Rules int    `json:"rules"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("check --format json is not JSON: %v\n%s", err, out)
	}
	if !got.OK || got.Seed != 42 || len(got.Proxies) != 1 || got.Proxies[0].Rules != 1 {
		t.Fatalf("check json content wrong: %+v", got)
	}
}

func TestCheckListsEveryProblemAndExitsOne(t *testing.T) {
	path := writeScenario(t, `{
	  "version": 9,
	  "proxies": [{"name": "", "listen": "x", "upstream": "ftp://y", "rules": []}]
	}`)
	code, _, errOut := runCLI(t, "check", path)
	if code != ExitInvalid {
		t.Fatalf("invalid scenario exit = %d, want %d", code, ExitInvalid)
	}
	for _, frag := range []string{"version", "proxies[0].name", "proxies[0].listen", "proxies[0].upstream"} {
		if !strings.Contains(errOut, frag) {
			t.Errorf("stderr missing finding %q:\n%s", frag, errOut)
		}
	}
}

func TestCheckUnknownFieldIsInvalidNotCrash(t *testing.T) {
	path := writeScenario(t, `{"version": 1, "sead": 42, "proxies": []}`)
	code, _, errOut := runCLI(t, "check", path)
	if code != ExitInvalid || !strings.Contains(errOut, "sead") {
		t.Fatalf("typo detection: code=%d err=%q", code, errOut)
	}
}

func TestCheckMissingFileExitsOne(t *testing.T) {
	code, _, errOut := runCLI(t, "check", filepath.Join(t.TempDir(), "absent.json"))
	if code != ExitInvalid || errOut == "" {
		t.Fatalf("missing file: code=%d err=%q", code, errOut)
	}
}

func TestCheckUsageErrors(t *testing.T) {
	path := writeScenario(t, oneProxy("http://127.0.0.1:9"))
	code, _, errOut := runCLI(t, "check", "--format", "yaml", path)
	if code != ExitUsage || !strings.Contains(errOut, "yaml") {
		t.Fatalf("bad format: code=%d err=%q", code, errOut)
	}
	if code, _, _ := runCLI(t, "check"); code != ExitUsage {
		t.Fatalf("no path: code=%d, want %d", code, ExitUsage)
	}
	if code, _, _ := runCLI(t, "check", "a.json", "b.json"); code != ExitUsage {
		t.Fatalf("two paths: code=%d, want %d", code, ExitUsage)
	}
}

func TestPlanTextOutputIsDeterministicAndSummarized(t *testing.T) {
	path := writeScenario(t, oneProxy("http://127.0.0.1:9"))
	code, out1, _ := runCLI(t, "plan", "--requests", "30", path)
	if code != ExitOK {
		t.Fatalf("plan exit = %d", code)
	}
	_, out2, _ := runCLI(t, "plan", "--requests", "30", path)
	if out1 != out2 {
		t.Fatal("plan output must be byte-identical across runs")
	}
	if !strings.Contains(out1, `plan: proxy "api" seed 42 — GET /, requests 1-30`) {
		t.Fatalf("plan header wrong:\n%s", out1)
	}
	if !strings.Contains(out1, "503 (brownout)") {
		t.Fatalf("plan should show injected 503s:\n%s", out1)
	}
	if !strings.Contains(out1, "30 requests:") {
		t.Fatalf("plan summary footer missing:\n%s", out1)
	}
}

// Pinned by the seeded golden values: seed 42, rule "brownout", rate 0.5
// fires on counter 2 but not 1 or 3.
func TestPlanShowsThePinnedSchedule(t *testing.T) {
	path := writeScenario(t, oneProxy("http://127.0.0.1:9"))
	_, out, _ := runCLI(t, "plan", "--requests", "3", path)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var rows []string
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "1 ") || strings.HasPrefix(trimmed, "2 ") || strings.HasPrefix(trimmed, "3 ") {
			rows = append(rows, trimmed)
		}
	}
	want := []string{"1  pass", "2  503 (brownout)", "3  pass"}
	for i, w := range want {
		if i >= len(rows) || rows[i] != w {
			t.Fatalf("row %d = %q, want %q\nfull output:\n%s", i+1, rows, w, out)
		}
	}
}

func TestPlanJSONStructure(t *testing.T) {
	path := writeScenario(t, oneProxy("http://127.0.0.1:9"))
	code, out, _ := runCLI(t, "plan", "--requests", "10", "--format", "json", path)
	if code != ExitOK {
		t.Fatalf("plan json exit = %d", code)
	}
	var got struct {
		Proxy    string `json:"proxy"`
		Seed     uint64 `json:"seed"`
		Requests []struct {
			N      uint64 `json:"n"`
			Action string `json:"action"`
			Status int    `json:"status"`
		} `json:"requests"`
		Summary struct {
			Pass     uint64 `json:"pass"`
			Injected uint64 `json:"injected"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("plan --format json is not JSON: %v", err)
	}
	if got.Proxy != "api" || got.Seed != 42 || len(got.Requests) != 10 {
		t.Fatalf("plan json shape wrong: %+v", got)
	}
	if got.Summary.Pass+got.Summary.Injected != 10 {
		t.Fatalf("plan json summary inconsistent: %+v", got.Summary)
	}
	if got.Requests[1].Status != 503 { // counter 2 is the pinned injection
		t.Fatalf("plan json pinned schedule wrong: %+v", got.Requests)
	}
}

func TestPlanRequiresProxyChoiceWhenAmbiguous(t *testing.T) {
	path := writeScenario(t, `{
	  "version": 1,
	  "proxies": [
	    {"name": "a", "listen": "127.0.0.1:0", "upstream": "http://127.0.0.1:1", "rules": []},
	    {"name": "b", "listen": "127.0.0.1:0", "upstream": "http://127.0.0.1:2", "rules": []}
	  ]
	}`)
	code, _, errOut := runCLI(t, "plan", path)
	if code != ExitUsage || !strings.Contains(errOut, "--proxy") {
		t.Fatalf("ambiguous proxy: code=%d err=%q", code, errOut)
	}
	code, _, _ = runCLI(t, "plan", "--proxy", "b", path)
	if code != ExitOK {
		t.Fatalf("explicit --proxy b should work, code=%d", code)
	}
	code, _, errOut = runCLI(t, "plan", "--proxy", "nope", path)
	if code != ExitUsage || !strings.Contains(errOut, `"nope"`) {
		t.Fatalf("unknown proxy: code=%d err=%q", code, errOut)
	}
}

func TestPlanValidatesCounterAndHeaderFlags(t *testing.T) {
	path := writeScenario(t, oneProxy("http://127.0.0.1:9"))
	if code, _, _ := runCLI(t, "plan", "--from", "0", path); code != ExitUsage {
		t.Fatalf("--from 0 should be a usage error, code=%d", code)
	}
	if code, _, _ := runCLI(t, "plan", "--requests", "0", path); code != ExitUsage {
		t.Fatalf("--requests 0 should be a usage error, code=%d", code)
	}
	if code, _, _ := runCLI(t, "plan", "--header", "no-colon-here", path); code != ExitUsage {
		t.Fatalf("malformed --header should be a usage error, code=%d", code)
	}
}

func TestPlanHeaderFlagShapesTheSchedule(t *testing.T) {
	path := writeScenario(t, `{
	  "version": 1,
	  "seed": 1,
	  "proxies": [{
	    "name": "api", "listen": "127.0.0.1:0", "upstream": "http://127.0.0.1:9",
	    "rules": [{
	      "name": "canary-outage",
	      "match": {"headers": {"X-Canary": "1"}},
	      "fault": {"status": 503}
	    }]
	  }]
	}`)
	_, plain, _ := runCLI(t, "plan", "--requests", "5", path)
	if strings.Contains(plain, "503") {
		t.Fatalf("rule should not fire without the header:\n%s", plain)
	}
	_, canary, _ := runCLI(t, "plan", "--requests", "5", "--header", "X-Canary: 1", path)
	if !strings.Contains(canary, "503 (canary-outage)") {
		t.Fatalf("rule should fire with the header:\n%s", canary)
	}
}

func TestRunValidatesFlagsAndScenarioBeforeBinding(t *testing.T) {
	good := writeScenario(t, oneProxy("http://127.0.0.1:9"))
	code, _, errOut := runCLI(t, "run", "--log", "xml", good)
	if code != ExitUsage || !strings.Contains(errOut, "xml") {
		t.Fatalf("bad --log: code=%d err=%q", code, errOut)
	}
	bad := writeScenario(t, `{"version": 1, "proxies": []}`)
	code, _, errOut = runCLI(t, "run", bad)
	if code != ExitInvalid || !strings.Contains(errOut, "at least one proxy") {
		t.Fatalf("invalid run: code=%d err=%q", code, errOut)
	}
}

// lineWriter buffers output and emits each completed line on a channel, so
// tests can wait for the ready line without polling or sleeping.
type lineWriter struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	part  bytes.Buffer
	lines chan string
}

func newLineWriter() *lineWriter { return &lineWriter{lines: make(chan string, 64)} }

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for _, b := range p {
		if b == '\n' {
			select {
			case w.lines <- w.part.String():
			default: // never block the server on a full channel
			}
			w.part.Reset()
		} else {
			w.part.WriteByte(b)
		}
	}
	return len(p), nil
}

func (w *lineWriter) contents() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// TestRunServesScenarioEndToEnd is the CLI-level integration test: bind on
// an ephemeral loopback port, proxy one passing and one injected request,
// then shut down cleanly on context cancel.
func TestRunServesScenarioEndToEnd(t *testing.T) {
	origin := httptest.NewServer(proxy.EchoHandler())
	t.Cleanup(origin.Close)

	sc, err := scenario.Parse([]byte(oneProxy(origin.URL)))
	if err != nil {
		t.Fatalf("scenario: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := newLineWriter()
	done := make(chan int, 1)
	go func() { done <- serveScenario(ctx, sc, "text", false, stdout, io.Discard) }()

	// The ready line is printed before serving starts.
	var ready string
	for line := range stdout.lines {
		if strings.HasPrefix(line, "proxy api listening on ") {
			ready = line
			break
		}
	}
	fields := strings.Fields(ready) // proxy api listening on ADDR -> URL
	addr := fields[4]

	// Request 1 passes through to the echo origin (pinned schedule).
	resp, err := http.Get("http://" + addr + "/hello")
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Slowlane-Echo") != "1" {
		t.Fatalf("request 1 should pass to the origin: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Slowlane-Request") != "1" {
		t.Fatalf("request counter header = %q", resp.Header.Get("X-Slowlane-Request"))
	}

	// Request 2 is the pinned 503 injection.
	resp2, err := http.Get("http://" + addr + "/hello")
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if resp2.StatusCode != 503 || resp2.Header.Get("X-Slowlane-Injected") != "brownout" {
		t.Fatalf("request 2 should be injected: %d %q", resp2.StatusCode, body2)
	}

	cancel()
	if code := <-done; code != ExitOK {
		t.Fatalf("run exit = %d, want 0", code)
	}
	out := stdout.contents()
	if !strings.Contains(out, "[pass] -> 200") || !strings.Contains(out, "[503 (brownout)] -> 503") {
		t.Fatalf("request log lines missing:\n%s", out)
	}
	if !strings.Contains(out, "slowlane: shut down") {
		t.Fatalf("shutdown line missing:\n%s", out)
	}
}

// TestEchoCommandServesUntilCancelled drives the echo subcommand the same
// way: parse the ready line, hit it once, cancel.
func TestEchoCommandServesUntilCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stdout := newLineWriter()
	done := make(chan int, 1)
	go func() {
		done <- Main(ctx, []string{"echo", "--listen", "127.0.0.1:0"}, stdout, io.Discard)
	}()

	var ready string
	for line := range stdout.lines {
		if strings.HasPrefix(line, "echo listening on ") {
			ready = line
			break
		}
	}
	addr := strings.TrimPrefix(ready, "echo listening on ")
	resp, err := http.Get("http://" + addr + "/ping")
	if err != nil {
		t.Fatalf("echo request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("X-Slowlane-Echo") != "1" {
		t.Fatalf("echo response wrong: %d", resp.StatusCode)
	}
	cancel()
	if code := <-done; code != ExitOK {
		t.Fatalf("echo exit = %d, want 0", code)
	}
}
