package action

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mozership/probe/actionrpc"

	"github.com/mozership/probe-agent/internal/llm"
)

const (
	// defaultTimeout is below the 5 minutes Probe gives a step unless told
	// otherwise, so that the action ends with a verdict before the step is
	// stopped.
	defaultTimeout  = 4 * time.Minute
	defaultMaxSteps = 40
	defaultEffort   = "medium"
	defaultWidth    = 1280
	defaultHeight   = 800
)

// The modes of the cache.
const (
	// cacheAuto replays the recording if there is one, and records the
	// run when it passes without replaying all of it.
	cacheAuto = "auto"
	// cacheRead replays the recording if there is one, and never writes
	// it, as in CI, where the recording comes from the repository.
	cacheRead = "read"
	// cacheRefresh records the run as if there were no recording.
	cacheRefresh = "refresh"
	// cacheStrict replays as read does, and ends a test whose recording no
	// longer matches the application blocked, without the model, so that
	// CI tells a stale recording rather than paying for the tester.
	cacheStrict = "strict"
)

// params are the keys the action takes in with.
var params = []string{
	"task", "expect", "persona", "goal", "url", "allow_hosts", "secrets",
	"cache", "cache_mode",
	"provider", "model", "effort", "base_url", "vision", "max_steps", "timeout",
	"headless", "window_w", "window_h", "evidence_dir",
}

// Params returns the keys the action takes in with.
func Params() []string {
	return append([]string{}, params...)
}

// req is what a step asks of the action.
type req struct {
	Task       string
	Expect     string
	Persona    string
	Goal       string
	Cache      string
	CacheMode  string
	URL        string
	AllowHosts []string
	Secrets    map[string]string
	// Provider is anthropic, or a provider of llm.Providers.
	Provider    string
	Model       string
	Effort      string
	BaseURL     string
	Vision      bool
	MaxSteps    int
	Timeout     time.Duration
	Headless    bool
	Width       int
	Height      int
	EvidenceDir string
}

// parse reads with. A value a template rendered comes as a string, so the
// numbers and the booleans are taken as strings too.
func parse(with map[string]any) (req, error) {
	r := req{
		Provider:  anthropic,
		Effort:    defaultEffort,
		MaxSteps:  defaultMaxSteps,
		Timeout:   defaultTimeout,
		Headless:  true,
		CacheMode: cacheAuto,
		Width:     defaultWidth,
		Height:    defaultHeight,
	}
	var errs []error
	str := func(key string, dst *string) {
		v, ok := with[key]
		if !ok {
			return
		}
		s, ok := v.(string)
		if !ok {
			errs = append(errs, fmt.Errorf("%s must be a string", key))
			return
		}
		*dst = strings.TrimSpace(s)
	}
	num := func(key string, dst *int) {
		v, ok := with[key]
		if !ok {
			return
		}
		n, err := toInt(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("%s must be a whole number of 1 or more", key))
			return
		}
		*dst = n
	}

	for _, k := range slices.Sorted(maps.Keys(with)) {
		if !slices.Contains(params, k) {
			errs = append(errs, fmt.Errorf("unknown parameter %s; the parameters are %s", k, strings.Join(params, ", ")))
		}
	}

	str("task", &r.Task)
	str("expect", &r.Expect)
	str("persona", &r.Persona)
	str("goal", &r.Goal)
	str("url", &r.URL)
	str("provider", &r.Provider)
	str("model", &r.Model)
	str("effort", &r.Effort)
	str("base_url", &r.BaseURL)
	str("evidence_dir", &r.EvidenceDir)
	str("cache", &r.Cache)
	str("cache_mode", &r.CacheMode)
	num("max_steps", &r.MaxSteps)
	num("window_w", &r.Width)
	num("window_h", &r.Height)

	// A step is a scenario test of task and expect, or a persona test of
	// persona and goal.
	scenario := r.Task != "" || r.Expect != ""
	persona := r.Persona != "" || r.Goal != ""
	required := []string{"url"}
	switch {
	case scenario && persona:
		errs = append(errs, errors.New("give task and expect for a scenario test, or persona and goal for a persona test, not both"))
	case persona:
		required = append(required, "persona", "goal")
	default:
		required = append(required, "task", "expect")
	}
	for _, k := range required {
		if v, _ := with[k].(string); strings.TrimSpace(v) == "" {
			errs = append(errs, fmt.Errorf("%s is required", k))
		}
	}
	if r.URL != "" {
		if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("url %q must be an http or https URL", r.URL))
		}
	}
	switch r.CacheMode {
	case cacheAuto, cacheRead, cacheRefresh, cacheStrict:
	default:
		errs = append(errs, fmt.Errorf("cache_mode %q must be auto, read, refresh or strict", r.CacheMode))
	}
	if r.Cache != "" && persona {
		errs = append(errs, errors.New("cache is for scenario tests; a persona test takes another way each run"))
	}

	r.Provider = strings.ToLower(r.Provider)
	p, compatible := llm.Providers[r.Provider]
	switch {
	case r.Provider == anthropic:
		r.Vision = true
		if r.Model == "" {
			r.Model = llm.DefaultModel
		}
		if _, ok := with["vision"]; ok {
			errs = append(errs, errors.New("vision is for the providers other than anthropic, whose models all see images"))
		}
	case compatible:
		r.Vision = p.Vision
		if r.Model == "" {
			errs = append(errs, fmt.Errorf("model is required with the provider %s", r.Provider))
		}
	default:
		errs = append(errs, fmt.Errorf("provider %q must be %s", r.Provider, providerNames()))
	}
	if v, ok := with["vision"]; ok && compatible {
		b, err := toBool(v)
		if err != nil {
			errs = append(errs, errors.New("vision must be true or false"))
		} else {
			r.Vision = b
		}
	}
	if r.BaseURL != "" {
		if u, err := url.Parse(r.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("base_url %q must be an http or https URL", r.BaseURL))
		}
	}

	switch r.Effort {
	case "low", "medium", "high", "xhigh", "max":
	default:
		errs = append(errs, fmt.Errorf("effort %q must be low, medium, high, xhigh or max", r.Effort))
	}

	if v, ok := with["timeout"]; ok {
		d, err := actionrpc.ParseTimeout(v)
		if err != nil {
			errs = append(errs, err)
		} else {
			r.Timeout = d
		}
	}
	if v, ok := with["headless"]; ok {
		b, err := toBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("headless must be true or false"))
		} else {
			r.Headless = b
		}
	}
	if v, ok := with["allow_hosts"]; ok {
		hosts, err := toStrings(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("allow_hosts: %w", err))
		}
		r.AllowHosts = hosts
	}
	if v, ok := with["secrets"]; ok {
		m, isMap := v.(map[string]any)
		if !isMap {
			errs = append(errs, errors.New("secrets must be a map of names to values"))
		}
		r.Secrets = map[string]string{}
		for name, val := range m {
			if !secretName(name) {
				errs = append(errs, fmt.Errorf("the secret name %q must be letters, digits and _, not starting with a digit", name))
				continue
			}
			s, ok := val.(string)
			if !ok {
				s = fmt.Sprint(val)
			}
			r.Secrets[name] = s
		}
	}
	return r, errors.Join(errs...)
}

// anthropic is the provider of the Claude API, which the agent uses unless
// told another.
const anthropic = "anthropic"

// providerNames lists the providers the agent knows, for an error.
func providerNames() string {
	names := append([]string{anthropic}, slices.Sorted(maps.Keys(llm.Providers))...)
	return strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
}

// forLog returns with as it may be logged: the values of the secrets
// hidden.
func forLog(with map[string]any) map[string]any {
	out := make(map[string]any, len(with))
	for k, v := range with {
		out[k] = v
	}
	if m, ok := with["secrets"].(map[string]any); ok {
		hidden := make(map[string]any, len(m))
		for name := range m {
			hidden[name] = "<redacted>"
		}
		out["secrets"] = hidden
	}
	return out
}

func secretName(s string) bool {
	if s == "" || (s[0] >= '0' && s[0] <= '9') {
		return false
	}
	for _, c := range s {
		if !(c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')) {
			return false
		}
	}
	return true
}

func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int64:
		return int(n), nil
	case float64:
		if n != float64(int(n)) {
			return 0, errors.New("not a whole number")
		}
		return int(n), nil
	case string:
		return strconv.Atoi(strings.TrimSpace(n))
	}
	return 0, fmt.Errorf("not a number: %v", v)
}

func toBool(v any) (bool, error) {
	switch b := v.(type) {
	case bool:
		return b, nil
	case string:
		return strconv.ParseBool(strings.TrimSpace(b))
	}
	return false, fmt.Errorf("not a boolean: %v", v)
}

// toStrings reads a list of strings, or one string of them apart by commas.
func toStrings(v any) ([]string, error) {
	var out []string
	switch l := v.(type) {
	case string:
		for _, s := range strings.Split(l, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	case []any:
		for _, e := range l {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("%v is not a string", e)
			}
			out = append(out, strings.TrimSpace(s))
		}
	default:
		return nil, errors.New("must be a list of hosts")
	}
	return out, nil
}
