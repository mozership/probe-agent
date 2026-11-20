package action

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"syscall"

	"github.com/mozership/probe/actionrpc"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/scenario"
)

// The table of the job summary the tests of a run add a row each to, with
// the total of the rows last.
const (
	summaryHeading = "### Model usage of probe-agent"
	summaryHeader  = "| Job | Step | Verdict | Model | Requests | Input | Cache read | Cache write | Output | Replay |"
	summaryRule    = "|---|---|---|---|--:|--:|--:|--:|--:|---|"
	summaryTotal   = "| **Total** |"
)

// usageColumns are the columns of the table that hold numbers, which the
// total adds up: the requests and the tokens.
var usageColumns = []int{4, 5, 6, 7, 8}

// writeSummary adds the usage of the model by the test to the job summary
// of GitHub Actions, the file GITHUB_STEP_SUMMARY names, as a row of a
// table that the tests of the run share, with their total last. Each test
// is a process of its own, which may run beside another, so the file is
// locked while it is read and written. The table is written again over its
// total alone, and only while the file is as it was read, so that what a
// writer that does not lock it, as a step that appends to it beside the
// test, added before is kept; one that adds to it at the very moment of the
// write can still be overwritten. A summary another step wrote to last
// starts a table anew. The secrets of the test are hidden in the row, as a
// step is named after the values of the workflow.
func writeSummary(path string, step actionrpc.Step, r req, o scenario.Outcome, replay string) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN) //nolint:errcheck // closing the file unlocks it as well

	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	row := summaryRow(step, r, o, replay)
	rows, at := summaryRows(string(data))
	// What another writer added since the file was read is kept: the
	// table goes after it, anew.
	if fi, err := f.Stat(); err != nil {
		return err
	} else if fi.Size() != int64(len(data)) {
		rows, data = nil, nil
		at = fi.Size()
	}
	var b strings.Builder
	if rows == nil {
		if data != nil {
			at = int64(len(data))
		}
		if at > 0 && !strings.HasSuffix(string(data), "\n") {
			b.WriteString("\n")
		}
		if at > 0 {
			b.WriteString("\n")
		}
		b.WriteString(summaryHeading + "\n\n" + summaryHeader + "\n" + summaryRule + "\n")
	}
	rows = append(rows, row)
	b.WriteString(row + "\n" + totalRow(rows) + "\n")
	if _, err := f.WriteAt([]byte(b.String()), at); err != nil {
		return err
	}
	// The file is cut after the total, as a total edited by hand can be
	// longer than what is written over it.
	return f.Truncate(at + int64(b.Len()))
}

// summaryRows returns the rows of the table the summary ends with, and
// where its total begins, or nil when the summary ends with something
// else.
func summaryRows(s string) ([]string, int64) {
	i := strings.LastIndex(s, summaryHeader+"\n"+summaryRule+"\n")
	if i < 0 {
		return nil, 0
	}
	start := i + len(summaryHeader) + len(summaryRule) + 2
	lines := strings.Split(strings.TrimSuffix(s[start:], "\n"), "\n")
	last := len(lines) - 1
	if last < 0 || !strings.HasPrefix(lines[last], summaryTotal) {
		return nil, 0
	}
	rows := []string{}
	for _, l := range lines[:last] {
		if !strings.HasPrefix(l, "| ") || strings.HasPrefix(l, summaryTotal) {
			return nil, 0
		}
		rows = append(rows, l)
	}
	// The total is the last line, which the new row takes the place of.
	return rows, int64(strings.LastIndex(strings.TrimSuffix(s, "\n"), "\n") + 1)
}

// summaryRow is the row of the test: where it ran, how it came out, and
// what it used of the model.
func summaryRow(step actionrpc.Step, r req, o scenario.Outcome, replay string) string {
	name := step.Name
	if name == "" {
		name = step.ID
	}
	if step.Repeat > 0 {
		name += fmt.Sprintf(" (run %d)", step.Repeat+1)
	}
	if step.Attempt > 1 {
		name += fmt.Sprintf(" (attempt %d)", step.Attempt)
	}
	model := o.Model
	if model == "" {
		model = r.Model
	}
	// The secrets are hidden in the text alone, before it is made to fit a
	// cell, which changes how a secret is written, as | to \|.
	hide := func(s string) string {
		var b strings.Builder
		_, _ = secretsHidden(&b, r.Secrets).Write([]byte(s))
		return cell(b.String())
	}
	u := o.Usage
	cells := []string{
		hide(step.JobName), hide(name), cell(o.Verdict), hide(model),
		count(int64(u.Requests)), count(u.InputTokens), count(u.CacheReadTokens), count(u.CacheWriteTokens), count(u.OutputTokens),
		cell(replay),
	}
	return "| " + strings.Join(cells, " | ") + " |"
}

// totalRow adds up the numbers of rows.
func totalRow(rows []string) string {
	sums := make([]int64, len(usageColumns))
	for _, row := range rows {
		cells := strings.Split(strings.Trim(row, "|"), " | ")
		for i, c := range usageColumns {
			if c < len(cells) {
				n, _ := strconv.ParseInt(strings.ReplaceAll(strings.TrimSpace(cells[c]), ",", ""), 10, 64)
				sums[i] += n
			}
		}
	}
	var cells []string
	for _, n := range sums {
		cells = append(cells, "**"+count(n)+"**")
	}
	return summaryTotal + " | | | " + strings.Join(cells, " | ") + " | |"
}

// cell makes s fit a cell of a table: one line, with no bar to end it.
func cell(s string) string {
	s = strings.ReplaceAll(oneLine(s), "|", `\|`)
	if s == "" {
		return "-"
	}
	return s
}

// count writes n with a comma every three digits.
func count(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// usageLine says what the test used of the model, for the report.
func usageLine(u llm.Usage) string {
	return fmt.Sprintf("model: %s requests, %s input tokens (%s read from the cache, %s written to it), %s output tokens",
		count(int64(u.Requests)), count(u.InputTokens), count(u.CacheReadTokens), count(u.CacheWriteTokens), count(u.OutputTokens))
}
