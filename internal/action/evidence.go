package action

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mozership/probe-agent/internal/scenario"
)

// evidence is the directory a test keeps its screenshots, report and trace
// in. Pages can show private data, so only the user who ran Probe can read
// it.
type evidence struct {
	dir string
}

// newEvidence makes a directory of its own for the test under dir, or under
// the temporary directory when dir is empty, so that the steps of a run do
// not write over each other's files.
func newEvidence(dir string) (*evidence, error) {
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("evidence_dir: %w", err)
	}
	d, err := os.MkdirTemp(dir, "probe-agent-")
	if err != nil {
		return nil, fmt.Errorf("evidence_dir: %w", err)
	}
	return &evidence{dir: d}, nil
}

// save writes data to name in the directory, and returns its path, or ""
// when it could not be written.
func (e *evidence) save(name string, data []byte) string {
	path := filepath.Join(e.dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return ""
	}
	return path
}

// files are the files a test wrote besides its images.
type files struct {
	Report string
	Trace  string
}

// write writes the report of o, for a person to read, and its trace, for a
// program.
func (e *evidence) write(r req, o scenario.Outcome) (files, error) {
	trace, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return files{}, err
	}
	f := files{
		Report: e.save("report.md", []byte(markdown(r, o, e.dir))),
		Trace:  e.save("trace.json", append(trace, '\n')),
	}
	if f.Report == "" || f.Trace == "" {
		return f, fmt.Errorf("could not write the report in %s", e.dir)
	}
	return f, nil
}

var verdictMarks = map[string]string{scenario.Pass: "✅", scenario.Fail: "❌", scenario.Blocked: "⚠️"}

// markdown is the report of o. Images are linked by their names, as they
// are in the same directory.
func markdown(r req, o scenario.Outcome, dir string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	rel := func(path string) string {
		if p, err := filepath.Rel(dir, path); err == nil {
			return p
		}
		return path
	}

	w("# %s %s\n\n", verdictMarks[o.Verdict], strings.ToUpper(o.Verdict))
	w("%s\n\n", o.Reason)
	w("| | |\n|---|---|\n")
	w("| Stopped | %s |\n", o.Stopped)
	w("| Steps | %d |\n", len(o.Steps))
	w("| Duration | %s |\n", o.Duration.Round(1e8))
	w("| Model | %s |\n", o.Model)
	w("| Tokens | %d in (%d from cache), %d out, %d requests |\n\n",
		o.Usage.InputTokens+o.Usage.CacheReadTokens+o.Usage.CacheWriteTokens, o.Usage.CacheReadTokens, o.Usage.OutputTokens, o.Usage.Requests)

	w("## Task\n\n%s\n\n", quoteBlock(r.Task))
	w("## Expectation\n\n%s\n\n", quoteBlock(r.Expect))
	if len(o.Evidence) > 0 {
		w("## Evidence\n\n")
		for _, e := range o.Evidence {
			mark := "✓"
			if !e.Verified {
				mark = "✗ not found on the pages:"
			}
			w("- %s %q\n", mark, e.Quote)
		}
		w("\n")
	}
	w("## What the tester saw\n\n%s\n\n", o.Summary)

	w("## Steps\n\n")
	for _, s := range o.Steps {
		input, _ := json.Marshal(s.Input)
		w("### %d. %s\n\n", s.Index, s.Tool)
		for _, n := range s.Notes {
			w("%s\n\n", quoteBlock(n))
		}
		if len(s.Input) > 0 {
			w("`%s`\n\n", input)
		}
		if s.Error != "" {
			w("**%s**\n\n", s.Error)
		} else if s.Result != "" {
			w("→ %s\n\n", s.Result)
		}
		if s.URL != "" {
			w("at <%s>\n\n", s.URL)
		}
		if s.Screenshot != "" {
			w("![step %d](%s)\n\n", s.Index, rel(s.Screenshot))
		}
	}

	if len(o.ConsoleErrors) > 0 {
		w("## Console errors\n\n")
		for _, e := range o.ConsoleErrors {
			w("- %s: %s", e.Type, e.Text)
			if e.URL != "" {
				w(" (%s:%d:%d)", e.URL, e.Line, e.Column)
			}
			w("\n")
		}
		w("\n")
	}
	if len(o.NetworkErrors) > 0 {
		w("## Failed requests\n\n")
		for _, e := range o.NetworkErrors {
			if e.Status > 0 {
				w("- %s %s → %d\n", e.Method, e.URL, e.Status)
			} else {
				w("- %s %s → %s\n", e.Method, e.URL, e.Error)
			}
		}
		w("\n")
	}

	w("## Final page\n\n%s (<%s>)\n\n", o.Title, o.URL)
	if o.FinalScreenshot != "" {
		w("![final page](%s)\n", rel(o.FinalScreenshot))
	}
	return b.String()
}

func quoteBlock(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}
