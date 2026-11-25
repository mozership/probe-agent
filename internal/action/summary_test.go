package action

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mozership/probe/sdk"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/scenario"
)

func usageOutcome(verdict string, u llm.Usage) scenario.Outcome {
	o := outcome()
	o.Verdict, o.Model, o.Usage = verdict, "claude-opus-5-5", u
	return o
}

func TestWriteSummary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := os.WriteFile(path, []byte("# Earlier step\n\nSome text."), 0o644); err != nil {
		t.Fatal(err)
	}
	r := req{Model: "claude-opus-5-5"}
	first := sdk.Step{JobName: "Sign-up", Name: "A new user | signs up"}
	if err := writeSummary(path, first, r, usageOutcome(scenario.Pass, llm.Usage{Requests: 3, InputTokens: 1200, CacheReadTokens: 50000, CacheWriteTokens: 8000, OutputTokens: 900}), "hit"); err != nil {
		t.Fatal(err)
	}
	second := sdk.Step{JobName: "Persona", ID: "step_2", Repeat: 1, Attempt: 2}
	if err := writeSummary(path, second, r, usageOutcome(scenario.Fail, llm.Usage{Requests: 12, InputTokens: 3000, CacheReadTokens: 1000000, OutputTokens: 4100}), ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := `# Earlier step

Some text.

### Model usage of probe-agent

| Job | Step | Verdict | Model | Requests | Input | Cache read | Cache write | Output | Replay |
|---|---|---|---|--:|--:|--:|--:|--:|---|
| Sign-up | A new user \| signs up | pass | claude-opus-5-5 | 3 | 1,200 | 50,000 | 8,000 | 900 | hit |
| Persona | step_2 (run 2) (attempt 2) | fail | claude-opus-5-5 | 12 | 3,000 | 1,000,000 | 0 | 4,100 | - |
| **Total** | | | | **15** | **4,200** | **1,050,000** | **8,000** | **5,000** | |
`
	if string(got) != want {
		t.Errorf("summary =\n%s\nwant\n%s", got, want)
	}

	// Another step writes to the summary, and the next test starts a table
	// anew after it.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = fmt.Fprint(f, "\n## Another step\n")
	_ = f.Close()
	if err := writeSummary(path, first, r, usageOutcome(scenario.Blocked, llm.Usage{Requests: 1, InputTokens: 7}), ""); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(path)
	tail := "## Another step\n\n### Model usage of probe-agent\n\n" + summaryHeader + "\n" + summaryRule + "\n| Sign-up | A new user \\| signs up | blocked | claude-opus-5-5 | 1 | 7 | 0 | 0 | 0 | - |\n| **Total** | | | | **1** | **7** | **0** | **0** | **0** | |\n"
	if !strings.HasSuffix(string(got), tail) || strings.Count(string(got), summaryHeading) != 2 {
		t.Errorf("summary =\n%s\nwant it to end with\n%s", got, tail)
	}
}

func TestWriteSummary_TestsSideBySide(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	const n = 20
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			step := sdk.Step{JobName: fmt.Sprintf("job %d", i), Name: "test"}
			if err := writeSummary(path, step, req{}, usageOutcome(scenario.Pass, llm.Usage{Requests: 1, InputTokens: 10}), ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	got, _ := os.ReadFile(path)
	s := string(got)
	if strings.Count(s, summaryHeading) != 1 || strings.Count(s, "| test | pass |") != n || !strings.HasSuffix(s, fmt.Sprintf("| **Total** | | | | **%d** | **%d** | **0** | **0** | **0** | |\n", n, 10*n)) {
		t.Errorf("summary =\n%s", s)
	}
}

func TestCount(t *testing.T) {
	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1000: "-1,000"} {
		if got := count(n); got != want {
			t.Errorf("count(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestParse_JobSummary(t *testing.T) {
	r, err := parse(map[string]any{"task": "t", "expect": "e", "targets": browserAt("http://a.test/")})
	if err != nil || !r.JobSummary {
		t.Fatalf("job_summary is %v by default, %v", r.JobSummary, err)
	}
	r, err = parse(map[string]any{"task": "t", "expect": "e", "targets": browserAt("http://a.test/"), "job_summary": false})
	if err != nil || r.JobSummary {
		t.Fatalf("job_summary false is %v, %v", r.JobSummary, err)
	}
	if _, err = parse(map[string]any{"task": "t", "expect": "e", "targets": browserAt("http://a.test/"), "job_summary": "maybe"}); err == nil || !strings.Contains(err.Error(), "job_summary must be true or false") {
		t.Errorf("a bad job_summary: %v", err)
	}
}

func TestWriteSummary_HidesTheSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	step := sdk.Step{JobName: "Log in", Name: "Log in as Alice@Example.com"}
	r := req{Secrets: map[string]string{"EMAIL": "alice@example.com"}}
	if err := writeSummary(path, step, r, usageOutcome(scenario.Pass, llm.Usage{Requests: 1}), ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(strings.ToLower(string(got)), "alice@example.com") || !strings.Contains(string(got), "| Log in as <secret:EMAIL> |") {
		t.Errorf("summary =\n%s", got)
	}
}

func TestWriteSummary_HidesASecretTheCellChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	step := sdk.Step{JobName: "Log in", Name: "Log in with p|ss"}
	r := req{Secrets: map[string]string{"PASS": "p|ss", "ONE": "1"}}
	if err := writeSummary(path, step, r, usageOutcome(scenario.Pass, llm.Usage{Requests: 1, InputTokens: 1}), ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "p\\|ss") || !strings.Contains(string(got), "| Log in with <secret:PASS> | pass | claude-opus-5-5 | 1 | 1 | 0 | 0 | 0 | - |") {
		t.Errorf("summary =\n%s", got)
	}
}

func TestWriteSummary_ATotalEditedByHand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "summary.md")
	if err := writeSummary(path, sdk.Step{JobName: "a", Name: "b"}, req{}, usageOutcome(scenario.Pass, llm.Usage{Requests: 1}), ""); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	long := strings.TrimSuffix(string(data), "\n") + strings.Repeat(" note", 100) + "\n"
	if err := os.WriteFile(path, []byte(long), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSummary(path, sdk.Step{JobName: "a", Name: "c"}, req{}, usageOutcome(scenario.Pass, llm.Usage{Requests: 1}), ""); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if strings.Contains(string(got), "note") || !strings.HasSuffix(string(got), "| **Total** | | | | **2** | **0** | **0** | **0** | **0** | |\n") {
		t.Errorf("summary =\n%s", got)
	}
}
