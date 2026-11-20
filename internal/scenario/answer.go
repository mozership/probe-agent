package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mozership/probe-agent/internal/llm"
)

// A target that answers a call, as an API or a database, is replayed by
// sending the call again, and checking that it gets the same kind of
// answer, as the same status. A value of the call the tester took from an
// earlier answer, such as the ID of a record it made, is this run's: it is
// recorded as where it was in that answer, the value of a key, and the
// replay takes it from the answer it got there instead. A value the tester
// may have read off a target otherwise is recorded as a gap.

// replayer is a target whose calls a replay sends again.
type replayer interface {
	Driver
	// outcome is what the call got, as the status of the response, which
	// a replay of it must get again; res is what the tester was told.
	outcome(call llm.Call, res llm.Result) string
	// reads says whether the call only reads, so that a replay may send it
	// again until it gets its outcome, and a run that sent it again and
	// again, as to wait for a mail, is recorded as one call that waits.
	reads(call llm.Call) bool
}

// Ref is a value of a recorded call taken from the answer to an earlier
// one, which the input of the call holds as {answer:N}, N counting from 1.
type Ref struct {
	// Step is the step of the recording, from 1, whose answer the value is
	// in.
	Step int `json:"step"`
	// Key is what comes before the value in the answer, as "id": or id=,
	// and Nth which of the values after it the value is, from 1.
	Key string `json:"key"`
	Nth int    `json:"nth"`
	// Number is whether the value is a number of the JSON of the input, as
	// {"id": 42}, rather than in a string.
	Number bool `json:"number,omitempty"`
}

// replayWait bounds sending again a call that reads, for it to get the
// answer it got when recorded: a call recorded as waiting, as for a mail,
// waits longer.
var (
	replayWait     = 3 * time.Second
	replayLongWait = 30 * time.Second
	replayPoll     = time.Second
)

// answerRef matches a placeholder of a value taken from an answer.
var answerRef = regexp.MustCompile(`\{answer:(\d+)\}`)

// valueToken is a word of an input that may be a value of the run: what is
// between the characters that end a word of a URL, a query or JSON.
var valueToken = regexp.MustCompile(`[A-Za-z0-9_~+=-]+(?:\.[A-Za-z0-9_~+=-]+)*`)

// keyBefore matches what comes before a value of a key at the end of a
// text: the key, and what joins it to the value, as "id": " or id=.
var keyBefore = regexp.MustCompile(`["']?([A-Za-z_][\w.-]*)["']?\s*[:=]\s*["']?$`)

// idKey and camelIDKey are keys whose value is an identifier, as id,
// note_id or noteId, which a number of an input is taken from alone; a
// value with a letter is taken from the value of any key.
var (
	idKey      = regexp.MustCompile(`(?i)(?:^|[_.-])(?:id|ids|key|token|code|ref|uuid|slug|number|no)$`)
	camelIDKey = regexp.MustCompile(`[a-z0-9](?:Id|ID|Ids|IDs|Key|Token|Code|Ref|Uuid|UUID|Slug|Number|No)$`)
)

// keyValue matches a value after key in an answer.
func keyValue(key string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\w.-])` + regexp.QuoteMeta(key) + `([^\s"',}\])]+)`)
}

// recordCall keeps a call of the tester or of a replay to a target that
// answers, with its answer, for the recording and the checks of the run.
func (r *run) recordCall(d replayer, call llm.Call, res llm.Result) {
	if res.IsError {
		return
	}
	var in map[string]any
	if json.Unmarshal(call.Input, &in) != nil {
		return
	}
	in, _ = r.secrets.maskAny(in).(map[string]any)
	rec := Recorded{Target: d.Kind(), Tool: call.Name, Outcome: d.outcome(call, res)}
	rec.Input, rec.Refs, rec.Gap = r.bind(in)
	if rec.Gap != "" {
		// The input holds a value of this run, which is not kept.
		rec.Input, rec.Refs = nil, nil
	}
	if n := len(r.recorded); n > 0 && rec.Gap == "" && d.reads(call) && sameCall(r.recorded[n-1], rec) {
		rec.Wait = true
		r.recorded[n-1], r.answers[n-1] = rec, res.Text
		return
	}
	r.keep(rec, res.Text)
}

// sameCall says whether two recorded steps are the same call, whatever
// they got.
func sameCall(a, b Recorded) bool {
	a.Outcome, a.Wait, b.Outcome, b.Wait = "", false, "", false
	return sameStep(a, b)
}

func sameStep(a, b Recorded) bool {
	ja, err1 := json.Marshal(a)
	jb, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(ja) == string(jb)
}

// bind returns the input with each value taken from an earlier answer as
// {answer:N}, and where those values are; or why the input holds a value
// of this run that cannot be taken again.
func (r *run) bind(in map[string]any) (map[string]any, []Ref, string) {
	instruction := flatten(r.secrets.mask(r.cfg.Task) + "\n" + r.secrets.mask(r.cfg.Expect))
	var refs []Ref
	gap := ""
	var walk func(v any) any
	walk = func(v any) any {
		switch v := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(v))
			for k, e := range v {
				out[k] = walk(e)
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, e := range v {
				out[i] = walk(e)
			}
			return out
		case float64:
			s := strconv.FormatFloat(v, 'f', -1, 64)
			if ref, why := r.valueOf(s, instruction); why != "" {
				gap = why
			} else if ref != nil {
				ref.Number = true
				refs = append(refs, *ref)
				return fmt.Sprintf("{answer:%d}", len(refs))
			}
			return v
		case string:
			return r.bindString(v, instruction, &refs, &gap)
		}
		return v
	}
	out, _ := walk(in).(map[string]any)
	return out, refs, gap
}

// bindString binds the values of a string of an input.
func (r *run) bindString(s, instruction string, refs *[]Ref, gap *string) string {
	if m := dateOrTime.FindString(s); m != "" && !strings.Contains(instruction, flatten(m)) {
		*gap = "it holds a date or a time, which may have been reckoned from the date"
		return s
	}
	// The placeholders of the secrets are the test's, not values to bind.
	blanked := placeholder.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat(" ", len(m)) })
	var b strings.Builder
	last := 0
	for _, loc := range valueToken.FindAllStringIndex(blanked, -1) {
		ref, why := r.valueOf(s[loc[0]:loc[1]], instruction)
		if why != "" {
			*gap = why
		}
		if ref == nil {
			continue
		}
		*refs = append(*refs, *ref)
		b.WriteString(s[last:loc[0]])
		fmt.Fprintf(&b, "{answer:%d}", len(*refs))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// valueOf returns where token is in an earlier answer, when the tester
// took it from one; or why it may have read it off a target otherwise; or
// neither, for a value of the test, as one the task gives or the tester
// made up.
func (r *run) valueOf(token, instruction string) (*Ref, string) {
	digit := strings.ContainsAny(token, "0123456789")
	if (!digit && !mintedValue(token)) || hasWord(instruction, strings.ToLower(token)) {
		return nil, ""
	}
	// A number is as often a count or an amount the tester chose, which
	// an earlier answer may hold by chance, as an identifier.
	number := digitsPattern.MatchString(token)
	for i := len(r.answers) - 1; i >= 0; i-- {
		if ref := keyed(r.answers[i], token, !number); ref != nil {
			ref.Step = i + 1
			return ref, ""
		}
	}
	if number && len(token) < 4 {
		return nil, ""
	}
	for _, t := range r.texts {
		if hasWord(strings.ToLower(t), strings.ToLower(token)) {
			return nil, "it holds a value that may have been read off a target"
		}
	}
	return nil, ""
}

// keyed returns where token is the value of a key in answer, or nil when
// it is nowhere the whole value of one. anyKey takes a key of any name,
// and otherwise only one of an identifier.
func keyed(answer, token string, anyKey bool) *Ref {
	for from := 0; ; {
		i := strings.Index(answer[from:], token)
		if i < 0 {
			return nil
		}
		i += from
		from = i + 1
		m := keyBefore.FindStringSubmatchIndex(answer[:i])
		if m == nil {
			continue
		}
		key := answer[m[0]:i]
		if name := answer[m[2]:m[3]]; !anyKey && !idKey.MatchString(name) && !camelIDKey.MatchString(name) {
			continue
		}
		re := keyValue(key)
		nth := 0
		for _, v := range re.FindAllStringSubmatchIndex(answer, -1) {
			nth++
			if v[2] == i {
				if answer[v[2]:v[3]] != token {
					break
				}
				return &Ref{Key: key, Nth: nth}
			}
		}
	}
}

// fill returns the input of a recorded step with the values it took from
// earlier answers taken from the answers of the replay.
func fill(input map[string]any, refs []Ref, answers []string) (map[string]any, error) {
	values := make([]string, len(refs))
	for i, ref := range refs {
		if ref.Step < 1 || ref.Step > len(answers) {
			return nil, fmt.Errorf("the value %d of the step is of a step it does not follow", i+1)
		}
		got := keyValue(ref.Key).FindAllStringSubmatch(answers[ref.Step-1], -1)
		if len(got) < ref.Nth {
			return nil, fmt.Errorf("the answer of step %d has no value after %s", ref.Step, ref.Key)
		}
		values[i] = got[ref.Nth-1][1]
	}
	var walk func(v any) (any, error)
	walk = func(v any) (any, error) {
		switch v := v.(type) {
		case map[string]any:
			out := make(map[string]any, len(v))
			for k, e := range v {
				f, err := walk(e)
				if err != nil {
					return nil, err
				}
				out[k] = f
			}
			return out, nil
		case []any:
			out := make([]any, len(v))
			for i, e := range v {
				f, err := walk(e)
				if err != nil {
					return nil, err
				}
				out[i] = f
			}
			return out, nil
		case string:
			if m := answerRef.FindStringSubmatch(v); m != nil && m[0] == v {
				n, _ := strconv.Atoi(m[1])
				if n >= 1 && n <= len(refs) && refs[n-1].Number {
					f, err := strconv.ParseFloat(values[n-1], 64)
					if err != nil {
						return nil, fmt.Errorf("the answer of step %d has %s after %s, where the recording had a number", refs[n-1].Step, quote(values[n-1]), refs[n-1].Key)
					}
					return f, nil
				}
			}
			return answerRef.ReplaceAllStringFunc(v, func(m string) string {
				n, _ := strconv.Atoi(answerRef.FindStringSubmatch(m)[1])
				if n < 1 || n > len(values) {
					return m
				}
				return values[n-1]
			}), nil
		}
		return v, nil
	}
	out, err := walk(input)
	if err != nil {
		return nil, err
	}
	m, _ := out.(map[string]any)
	return m, nil
}

// replayCall sends the call of step i of the recording again, with the
// values it took from earlier answers taken from answers, and returns the
// answer it got; or why it stops the replay, and whether that is because
// the application no longer matches the recording.
func (r *run) replayCall(ctx context.Context, i int, s Recorded, answers []string) (string, bool, string) {
	d, _ := r.driverOfKind(s.Target).(replayer)
	if d == nil {
		return "", false, "the test has no target " + s.Target
	}
	input, err := fill(s.Input, s.Refs, answers)
	if err != nil {
		return "", true, err.Error()
	}
	if input == nil {
		input = map[string]any{}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return "", false, err.Error()
	}
	call := llm.Call{ID: fmt.Sprintf("replay_%d", i+1), Name: s.Tool, Input: raw}
	wait := replayWait
	if s.Wait {
		wait = replayLongWait
	}
	deadline := time.Now().Add(wait)
	for {
		res, step := d.Do(ctx, call)
		step.Index = len(r.steps) + 1
		step.Notes = []string{fmt.Sprintf("Replayed step %d of the recording.", i+1)}
		r.steps = append(r.steps, step)
		if res.IsError {
			return "", true, fmt.Sprintf("%s failed: %s", s.Tool, strings.TrimPrefix(res.Text, "Error: "))
		}
		got := d.outcome(call, res)
		if got == s.Outcome {
			r.recordCall(d, call, res)
			return res.Text, false, ""
		}
		if !d.reads(call) || time.Now().After(deadline) || ctx.Err() != nil || len(r.steps) >= r.cfg.MaxSteps {
			return "", true, fmt.Sprintf("%s got %s, where the recording got %s", s.Tool, quote(got), quote(s.Outcome))
		}
		select {
		case <-ctx.Done():
		case <-time.After(replayPoll):
		}
	}
}

// driverOfKind returns the target of the test of kind, or nil.
func (r *run) driverOfKind(kind string) Driver {
	for _, d := range r.drivers {
		if d.Kind() == kind {
			return d
		}
	}
	return nil
}

// replayable says whether the steps of the test can be recorded and
// replayed: those of a browser, and of the targets that answer a call.
func (r *run) replayable() bool {
	for _, d := range r.drivers {
		if _, ok := d.(replayer); !ok && d != Driver(r.web) {
			return false
		}
	}
	return len(r.drivers) > 0
}
