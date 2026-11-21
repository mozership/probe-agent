package scenario

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
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
// text: the key, and what joins it to the value, as "id": " or id=, and the
// path the value ends, as in Location: /notes/57, whose segment it is.
var keyBefore = regexp.MustCompile(`["']?([A-Za-z_][\w.-]*)["']?\s*[:=]\s*["']?([^\s"',=]*/)?$`)

// idKey and camelIDKey are keys whose value is an identifier, as id,
// note_id or noteId, which a number of an input is taken from alone; a
// value with a letter is taken from the value of any key.
var (
	idKey      = regexp.MustCompile(`(?i)(?:^|[_.-])(?:id|ids|key|token|code|ref|uuid|slug|number|no)$`)
	camelIDKey = regexp.MustCompile(`[a-z0-9](?:Id|ID|Ids|IDs|Key|Token|Code|Ref|Uuid|UUID|Slug|Number|No)$`)
)

// keyValue matches a value after key in an answer: a segment of a path
// after a key that ends with one.
func keyValue(key string) *regexp.Regexp {
	value := `([^\s"',}\])]+)`
	if strings.HasSuffix(key, "/") {
		value = `([^\s"',}\])/?#]+)`
	}
	return regexp.MustCompile(`(?:^|[^\w.-])` + regexp.QuoteMeta(key) + value)
}

// recordCall keeps a call of the tester or of a replay to a target that
// answers, with its answer, for the recording and the checks of the run.
// texts is how many texts the targets had shown before the call, which its
// own answer is not among.
func (r *run) recordCall(d replayer, call llm.Call, res llm.Result, texts int) {
	// A call that only showed an image, as a screenshot, changed nothing
	// and answered nothing a later call can take a value from.
	if res.IsError || (res.Text == "" && len(res.Images) > 0) {
		return
	}
	// The numbers are kept as they were written, as an ID of 64 bits
	// that a float would round.
	dec := json.NewDecoder(bytes.NewReader(call.Input))
	dec.UseNumber()
	var in map[string]any
	if dec.Decode(&in) != nil {
		return
	}
	in, _ = r.secrets.maskAny(in).(map[string]any)
	seen := r.texts[:min(texts, len(r.texts))]
	bound := func(answers []string) Recorded {
		rec := Recorded{Target: d.Kind(), Tool: call.Name, Outcome: d.outcome(call, res)}
		rec.Input, rec.Refs, rec.Gap = r.bind(in, answers, seen)
		if rec.Gap != "" {
			// The input holds a value of this run, which is not kept.
			rec.Input, rec.Refs = nil, nil
		}
		return rec
	}
	rec := bound(r.answers)
	if g, ok := d.(interface {
		gap(call llm.Call, in map[string]any, instruction string) string
	}); ok && rec.Gap == "" {
		if why := g.gap(call, in, r.secrets.mask(r.cfg.Task)+"\n"+r.secrets.mask(r.cfg.Expect)); why != "" {
			rec.Input, rec.Refs, rec.Gap = nil, nil, why
		}
	}
	// A call sent again, as to wait for an export, is bound to what the
	// call before it was bound to, not to that call's answer.
	if n := len(r.recorded); n > 0 && d.reads(call) && r.recorded[n-1].Target == d.Kind() {
		if again := bound(r.answers[:n-1]); again.Gap == "" && sameCall(r.recorded[n-1], again) {
			again.Wait = true
			r.recorded[n-1], r.answers[n-1] = again, res.Text
			return
		}
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

// binding is what an input is bound against: the instruction of the test,
// the answers to the calls before it, and what the targets showed before
// it.
type binding struct {
	instruction string
	answers     []string
	seen        []string
}

// bind returns the input with each value taken from an earlier answer as
// {answer:N}, and where those values are; or why the input holds a value
// of this run that cannot be taken again.
func (r *run) bind(in map[string]any, answers, seen []string) (map[string]any, []Ref, string) {
	b := binding{instruction: flatten(r.secrets.mask(r.cfg.Task) + "\n" + r.secrets.mask(r.cfg.Expect)), answers: answers, seen: seen}
	var refs []Ref
	gap := ""
	var walk func(key string, v any) any
	walk = func(key string, v any) any {
		switch v := v.(type) {
		case map[string]any:
			// The keys in order, so that the values are numbered alike on
			// each run, and a recording is not written again for nothing.
			out := make(map[string]any, len(v))
			for _, k := range slices.Sorted(maps.Keys(v)) {
				out[k] = walk(k, v[k])
			}
			return out
		case []any:
			out := make([]any, len(v))
			for i, e := range v {
				out[i] = walk(key, e)
			}
			return out
		case json.Number, float64:
			s := fmt.Sprint(v)
			if ref, why := b.valueOf(s, key); why != "" {
				gap = why
			} else if ref != nil {
				ref.Number = true
				refs = append(refs, *ref)
				return fmt.Sprintf("{answer:%d}", len(refs))
			}
			return v
		case string:
			return b.bindString(v, &refs, &gap)
		}
		return v
	}
	out, _ := walk("", in).(map[string]any)
	return out, refs, gap
}

// bindString binds the values of a string of an input.
func (b binding) bindString(s string, refs *[]Ref, gap *string) string {
	instruction := b.instruction
	if m := dateOrTime.FindString(s); m != "" && !strings.Contains(instruction, flatten(m)) {
		*gap = "it holds a date or a time, which may have been reckoned from the date"
		return s
	}
	// The placeholders of the secrets are the test's, not values to bind.
	blanked := placeholder.ReplaceAllStringFunc(s, func(m string) string { return strings.Repeat(" ", len(m)) })
	var out strings.Builder
	last := 0
	for _, loc := range valueToken.FindAllStringIndex(blanked, -1) {
		// The key of a value of a query or of JSON in the string, as page
		// in ?page=2.
		key := ""
		if m := keyBefore.FindStringSubmatch(s[:loc[0]]); m != nil && m[2] == "" {
			key = m[1]
		}
		ref, why := b.valueOf(s[loc[0]:loc[1]], key)
		if why != "" {
			*gap = why
		}
		if ref == nil {
			continue
		}
		*refs = append(*refs, *ref)
		out.WriteString(s[last:loc[0]])
		fmt.Fprintf(&out, "{answer:%d}", len(*refs))
		last = loc[1]
	}
	out.WriteString(s[last:])
	return out.String()
}

// identifier says whether key names an identifier, as id, note_id or
// noteId.
func identifier(key string) bool {
	return idKey.MatchString(key) || camelIDKey.MatchString(key)
}

// valueOf returns where token is in an earlier answer, when the tester
// took it from one; or why it may have read it off a target otherwise; or
// neither, for a value of the test, as one the task gives or the tester
// made up. key is the key the input gives the value under, if any.
func (b binding) valueOf(token, key string) (*Ref, string) {
	digit := strings.ContainsAny(token, "0123456789")
	if (!digit && !mintedValue(token)) || hasWord(b.instruction, strings.ToLower(token)) {
		return nil, ""
	}
	// A number is as often a count or an amount the tester chose, which an
	// earlier answer may hold by chance, as an identifier: it is taken
	// from an answer only under a key of one, and only where the input
	// gives it under none, or under one of an identifier too.
	number := digitsPattern.MatchString(token)
	if !number || key == "" || identifier(key) {
		for i, v := range slices.Backward(b.answers) {
			if ref := keyed(v, token, !number); ref != nil {
				ref.Step = i + 1
				return ref, ""
			}
		}
	}
	// Only what looks minted per record, as an ID or a token, is taken to
	// be read off a target, and not a name of a field or a table, as
	// address_line2, that a schema showed.
	if !mintedValue(token) || (number && len(token) < 4) {
		return nil, ""
	}
	for _, t := range b.seen {
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
		// A segment of a path is of an identifier, as in /notes/57.
		if name := answer[m[2]:m[3]]; !anyKey && m[4] < 0 && !identifier(name) {
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
					if _, err := strconv.ParseFloat(values[n-1], 64); err != nil {
						return nil, fmt.Errorf("the answer of step %d has %s after %s, where the recording had a number", refs[n-1].Step, quote(values[n-1]), refs[n-1].Key)
					}
					return json.Number(values[n-1]), nil
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
		texts := len(r.texts)
		res, step := d.Do(ctx, call)
		step.Index = len(r.steps) + 1
		step.Notes = []string{fmt.Sprintf("Replayed step %d of the recording.", i+1)}
		r.steps = append(r.steps, step)
		if res.IsError {
			return "", true, fmt.Sprintf("%s failed: %s", s.Tool, strings.TrimPrefix(res.Text, "Error: "))
		}
		got := d.outcome(call, res)
		if got == s.Outcome {
			r.recordCall(d, call, res, texts)
			return res.Text, false, ""
		}
		switch {
		case ctx.Err() != nil:
			return "", false, "the test ran out of time"
		case len(r.steps) >= r.cfg.MaxSteps:
			return "", false, "the test ran out of steps"
		case !d.reads(call) || time.Now().After(deadline):
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
// replayed: those of a browser, of an app, and of the targets that answer
// a call.
func (r *run) replayable() bool {
	for _, d := range r.drivers {
		if _, ok := d.(replayer); !ok && d != Driver(r.web) {
			return false
		}
	}
	return len(r.drivers) > 0
}
