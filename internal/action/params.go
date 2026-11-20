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
	"unicode"
	"unicode/utf8"

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
	// maxLanguage bounds the name of a language, which is a word or two.
	maxLanguage   = 40
	defaultWidth  = 1280
	defaultHeight = 800
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
	"task", "expect", "persona", "goal", "targets", "secrets",
	"cache", "cache_mode",
	"provider", "model", "effort", "base_url", "vision", "max_steps", "timeout",
	"language", "evidence_dir", "job_summary",
}

// The kinds of target, as the keys of targets.
const (
	targetBrowser = "browser"
	targetIOS     = "ios"
	targetAndroid = "android"
	targetHTTP    = "http"
	targetGRPC    = "grpc"
	targetGraphQL = "graphql"
	targetMail    = "mail"
	targetDB      = "db"
)

// browserParams are the keys of targets.browser. A step once gave them at
// the top of with, as a short form of targets.browser, which is no longer
// taken.
var browserParams = []string{"url", "allow_hosts", "storage_state", "headless", "window_w", "window_h"}

// appParams are the keys of targets.ios and targets.android.
var appParams = []string{"app", "device", "timeout"}

// httpParams are the keys of targets.http.
var httpParams = []string{"url", "openapi", "headers", "cookies", "timeout"}

// grpcParams are the keys of targets.grpc.
var grpcParams = []string{"addr", "protocol", "tls", "insecure", "cert_file", "key_file", "ca_file", "metadata", "proto", "timeout"}

// graphqlParams are the keys of targets.graphql.
var graphqlParams = []string{"url", "headers", "timeout"}

// mailParams are the keys of targets.mail.
var mailParams = []string{"url", "basic_auth", "headers", "account_id", "timeout"}

// dbParams are the keys of targets.db.
var dbParams = []string{"dsn", "timeout"}

// Params returns the keys the action takes in with.
func Params() []string {
	return append([]string{}, params...)
}

// req is what a step asks of the action.
type req struct {
	Task      string
	Expect    string
	Persona   string
	Goal      string
	Cache     string
	CacheMode string
	// Browser says whether the test drives a browser, of URL and the
	// fields after it.
	Browser    bool
	URL        string
	AllowHosts []string
	Secrets    map[string]string
	// StorageState names a file of cookies and localStorage, as the
	// browser action's save_storage_state writes, that the browser starts
	// with.
	StorageState string
	// Provider is anthropic, or a provider of llm.Providers.
	Provider string
	Model    string
	Effort   string
	// Language is the language the model writes in, and the notes are
	// written in, as Japanese; empty, that of the task or the goal.
	Language string
	// JobSummary says whether the usage of the model is added to the job
	// summary of GitHub Actions.
	JobSummary  bool
	BaseURL     string
	Vision      bool
	MaxSteps    int
	Timeout     time.Duration
	Headless    bool
	Width       int
	Height      int
	EvidenceDir string
	// IOS is the app the test drives on an iOS simulator, if any.
	IOS *appReq
	// Android is the app the test drives on an Android emulator or device,
	// if any.
	Android *appReq
	// HTTP is the HTTP API the test calls, if any.
	HTTP *httpReq
	// GRPC is the gRPC API the test calls, if any.
	GRPC *grpcReq
	// GraphQL is the GraphQL API the test calls, if any.
	GraphQL *graphqlReq
	// Mail is the mailbox the test reads, if any.
	Mail *mailReq
	// DB is the database the test reads, if any.
	DB *dbReq
}

// dbReq is targets.db.
type dbReq struct {
	// DSN is the database, as the db action of Probe takes it.
	DSN string
	// Driver is postgres, mysql or sqlite, by the scheme of DSN.
	Driver string
	// Timeout bounds each statement; zero for the default of the db action.
	Timeout time.Duration
}

// mailReq is targets.mail.
type mailReq struct {
	// URL is the JMAP server, or its session URL.
	URL string
	// Username and Password are for HTTP Basic authentication, if given.
	Username, Password string
	// Headers are sent with each request.
	Headers map[string]string
	// AccountID is the account to read; empty for the primary one.
	AccountID string
	// Timeout bounds each call, when TimeoutSet; 0 removes the bound, as
	// in probe-jmap, whose default is taken without one.
	Timeout    time.Duration
	TimeoutSet bool
}

// graphqlReq is targets.graphql.
type graphqlReq struct {
	// URL is the endpoint of the API.
	URL string
	// Headers are sent with each request.
	Headers map[string]string
	// Timeout bounds each request; zero for the default of probe-graphql.
	Timeout time.Duration
}

// grpcReq is targets.grpc.
type grpcReq struct {
	Addr string
	// Conn are the keys of the grpc action that say how to reach the
	// server, as targets.grpc gives them: protocol, the TLS ones and
	// timeout.
	Conn map[string]any
	// Metadata are sent with each call.
	Metadata map[string]string
	// Proto is the proto of the grpc action; nil for none.
	Proto map[string]any
}

// appReq is targets.ios or targets.android.
type appReq struct {
	App    string
	Device string
	// Timeout bounds the actions of each call of probe-ios, from the first
	// to the last, as an action on an element that does not appear; zero
	// for the 30 seconds of probe-ios.
	Timeout time.Duration
}

// httpReq is targets.http.
type httpReq struct {
	// URL is the base URL of the API.
	URL string
	// OpenAPI is the openapi of the http action: a map of spec, the path of
	// the document, and strict; nil for none.
	OpenAPI map[string]any
	// Headers and Cookies are sent with each request.
	Headers map[string]string
	Cookies map[string]string
	// Timeout bounds each request; zero for the default of the http
	// action.
	Timeout time.Duration
}

// spec is the path of the OpenAPI document, or "" for none.
func (h *httpReq) spec() string {
	s, _ := h.OpenAPI["spec"].(string)
	return s
}

// kinds are the kinds of the targets of r, in the order the agent drives
// them.
func (r req) kinds() []string {
	var k []string
	if r.Browser {
		k = append(k, targetBrowser)
	}
	if r.IOS != nil {
		k = append(k, targetIOS)
	}
	if r.Android != nil {
		k = append(k, targetAndroid)
	}
	if r.HTTP != nil {
		k = append(k, targetHTTP)
	}
	if r.GRPC != nil {
		k = append(k, targetGRPC)
	}
	if r.GraphQL != nil {
		k = append(k, targetGraphQL)
	}
	if r.Mail != nil {
		k = append(k, targetMail)
	}
	if r.DB != nil {
		k = append(k, targetDB)
	}
	return k
}

// parse reads with. A value a template rendered comes as a string, so the
// numbers and the booleans are taken as strings too.
func parse(with map[string]any) (req, error) {
	r := req{
		Provider:   anthropic,
		Effort:     defaultEffort,
		JobSummary: true,
		MaxSteps:   defaultMaxSteps,
		Timeout:    defaultTimeout,
		Headless:   true,
		CacheMode:  cacheAuto,
		Width:      defaultWidth,
		Height:     defaultHeight,
	}
	var errs []error
	// strIn and numIn read key of src, which prefix names in an error, as
	// targets.browser. for a key under targets.
	strIn := func(src map[string]any, prefix, key string, dst *string) {
		v, ok := src[key]
		if !ok {
			return
		}
		s, ok := v.(string)
		if !ok {
			errs = append(errs, fmt.Errorf("%s%s must be a string", prefix, key))
			return
		}
		*dst = strings.TrimSpace(s)
	}
	numIn := func(src map[string]any, prefix, key string, dst *int) {
		v, ok := src[key]
		if !ok {
			return
		}
		n, err := toInt(v)
		if err != nil || n < 1 {
			errs = append(errs, fmt.Errorf("%s%s must be a whole number of 1 or more", prefix, key))
			return
		}
		*dst = n
	}
	str := func(key string, dst *string) { strIn(with, "", key, dst) }
	num := func(key string, dst *int) { numIn(with, "", key, dst) }

	for _, k := range slices.Sorted(maps.Keys(with)) {
		switch {
		case slices.Contains(browserParams, k):
			errs = append(errs, fmt.Errorf("%s goes under targets.browser, as targets.browser.%s: the short form of the browser at the top of with is no longer taken", k, k))
		case !slices.Contains(params, k):
			errs = append(errs, fmt.Errorf("unknown parameter %s; the parameters are %s", k, strings.Join(params, ", ")))
		}
	}

	str("task", &r.Task)
	str("expect", &r.Expect)
	str("persona", &r.Persona)
	str("goal", &r.Goal)
	str("provider", &r.Provider)
	str("model", &r.Model)
	str("effort", &r.Effort)
	str("language", &r.Language)
	str("base_url", &r.BaseURL)
	str("evidence_dir", &r.EvidenceDir)
	str("cache", &r.Cache)
	str("cache_mode", &r.CacheMode)
	num("max_steps", &r.MaxSteps)

	// The browser is given under targets.browser.
	var browser map[string]any
	prefix := "targets.browser."
	if v, ok := with["targets"]; !ok {
		errs = append(errs, errors.New("targets is required: name what the test drives, as targets.browser.url for a browser"))
	} else {
		targets, terrs := readTargets(v)
		errs = append(errs, terrs...)
		if b, ok := targets[targetBrowser]; ok {
			r.Browser, browser = true, b
		}
		for _, app := range []struct {
			kind string
			dst  **appReq
		}{{targetIOS, &r.IOS}, {targetAndroid, &r.Android}} {
			i, ok := targets[app.kind]
			if !ok {
				continue
			}
			a := &appReq{}
			prefix := "targets." + app.kind + "."
			strIn(i, prefix, "app", &a.App)
			strIn(i, prefix, "device", &a.Device)
			if v, ok := i["timeout"]; ok {
				d, err := actionrpc.ParseTimeout(v)
				if err != nil {
					errs = append(errs, fmt.Errorf("%stimeout: %w", prefix, err))
				} else {
					a.Timeout = d
				}
			}
			if a.App == "" {
				errs = append(errs, fmt.Errorf("%sapp is required", prefix))
			}
			*app.dst = a
		}
		if h, ok := targets[targetHTTP]; ok {
			r.HTTP = &httpReq{}
			errs = append(errs, readHTTP(h, r.HTTP)...)
		}
		if g, ok := targets[targetGRPC]; ok {
			r.GRPC = &grpcReq{}
			errs = append(errs, readGRPC(g, r.GRPC)...)
		}
		if g, ok := targets[targetGraphQL]; ok {
			r.GraphQL = &graphqlReq{}
			errs = append(errs, readGraphQL(g, r.GraphQL)...)
		}
		if m, ok := targets[targetMail]; ok {
			r.Mail = &mailReq{}
			errs = append(errs, readMail(m, r.Mail)...)
		}
		if d, ok := targets[targetDB]; ok {
			r.DB = &dbReq{}
			errs = append(errs, readDB(d, r.DB)...)
		}
	}
	strIn(browser, prefix, "url", &r.URL)
	strIn(browser, prefix, "storage_state", &r.StorageState)
	numIn(browser, prefix, "window_w", &r.Width)
	numIn(browser, prefix, "window_h", &r.Height)

	// A step is a scenario test of task and expect, or a persona test of
	// persona and goal.
	scenario := r.Task != "" || r.Expect != ""
	persona := r.Persona != "" || r.Goal != ""
	var required []string
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
	if r.Browser && r.URL == "" {
		errs = append(errs, fmt.Errorf("%surl is required", prefix))
	}
	if r.URL != "" {
		if u, err := url.Parse(r.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("%surl %q must be an http or https URL", prefix, r.URL))
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
	if r.Cache != "" && !slices.Equal(r.kinds(), []string{targetBrowser}) {
		errs = append(errs, errors.New("cache is for a test of a browser alone; a recording holds the steps of a browser"))
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
	if v, ok := with["job_summary"]; ok {
		b, err := toBool(v)
		if err != nil {
			errs = append(errs, errors.New("job_summary must be true or false"))
		} else {
			r.JobSummary = b
		}
	}
	if r.BaseURL != "" {
		if u, err := url.Parse(r.BaseURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("base_url %q must be an http or https URL", r.BaseURL))
		}
	}

	r.Language = strings.TrimSpace(r.Language)
	if utf8.RuneCountInString(r.Language) > maxLanguage || strings.ContainsFunc(r.Language, func(c rune) bool {
		return c == '\u2028' || c == '\u2029' || !unicode.IsPrint(c) && c != '\u00a0' && c != '\u3000'
	}) {
		errs = append(errs, fmt.Errorf("language must be the name of a language, as Japanese, on one line of at most %d characters", maxLanguage))
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
	if v, ok := browser["headless"]; ok {
		b, err := toBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%sheadless must be true or false", prefix))
		} else {
			r.Headless = b
		}
	}
	if v, ok := browser["allow_hosts"]; ok {
		hosts, err := toStrings(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%sallow_hosts: %w", prefix, err))
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

// readTargets reads with.targets: a map from the kind of each target to a
// map of what the workflow fixes for it. A kind the agent does not drive,
// a key a kind does not take, and a target that is not a map are errors.
func readTargets(v any) (map[string]map[string]any, []error) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, []error{errors.New("targets must be a map from the kind of each target, browser, ios, android, http, grpc, graphql, mail or db, to what the workflow fixes for it")}
	}
	if len(m) == 0 {
		return nil, []error{errors.New("targets names no target; name browser, ios, android, http, grpc, graphql, mail, db or more than one")}
	}
	var errs []error
	out := map[string]map[string]any{}
	for _, kind := range slices.Sorted(maps.Keys(m)) {
		var keys []string
		switch kind {
		case targetBrowser:
			keys = browserParams
		case targetIOS, targetAndroid:
			keys = appParams
		case targetHTTP:
			keys = httpParams
		case targetGRPC:
			keys = grpcParams
		case targetGraphQL:
			keys = graphqlParams
		case targetMail:
			keys = mailParams
		case targetDB:
			keys = dbParams
		default:
			errs = append(errs, fmt.Errorf("unknown target %s; the targets are browser, ios, android, http, grpc, graphql, mail and db", kind))
			continue
		}
		t, ok := m[kind].(map[string]any)
		if !ok {
			// targets.ios: with nothing under it comes as nil.
			if m[kind] != nil {
				errs = append(errs, fmt.Errorf("targets.%s must be a map of %s", kind, strings.Join(keys, ", ")))
				continue
			}
			t = map[string]any{}
		}
		for _, k := range slices.Sorted(maps.Keys(t)) {
			if !slices.Contains(keys, k) {
				errs = append(errs, fmt.Errorf("unknown parameter targets.%s.%s; the parameters of %s are %s", kind, k, kind, strings.Join(keys, ", ")))
			}
		}
		out[kind] = t
	}
	return out, errs
}

// readHTTP reads targets.http t into h.
func readHTTP(t map[string]any, h *httpReq) []error {
	var errs []error
	s, isStr := t["url"].(string)
	h.URL = strings.TrimSpace(s)
	switch {
	case t["url"] != nil && !isStr:
		errs = append(errs, errors.New("targets.http.url must be a string"))
	case h.URL == "":
		errs = append(errs, errors.New("targets.http.url is required"))
	default:
		if u, err := url.Parse(h.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("targets.http.url %q must be an http or https URL", h.URL))
		}
	}
	if v, ok := t["openapi"]; ok {
		switch o := v.(type) {
		case string:
			if strings.TrimSpace(o) == "" {
				errs = append(errs, errors.New("targets.http.openapi must name the OpenAPI document"))
			} else {
				h.OpenAPI = map[string]any{"spec": strings.TrimSpace(o)}
			}
		case map[string]any:
			h.OpenAPI = map[string]any{}
			for _, k := range slices.Sorted(maps.Keys(o)) {
				switch k {
				case "spec":
					s, _ := o[k].(string)
					if strings.TrimSpace(s) == "" {
						errs = append(errs, errors.New("targets.http.openapi.spec must name the OpenAPI document"))
					}
					h.OpenAPI[k] = strings.TrimSpace(s)
				case "strict":
					b, err := toBool(o[k])
					if err != nil {
						errs = append(errs, errors.New("targets.http.openapi.strict must be true or false"))
					}
					h.OpenAPI[k] = b
				default:
					errs = append(errs, fmt.Errorf("unknown parameter targets.http.openapi.%s; the parameters are spec and strict", k))
				}
			}
			if _, ok := o["spec"]; !ok {
				errs = append(errs, errors.New("targets.http.openapi.spec is required"))
			}
		default:
			errs = append(errs, errors.New("targets.http.openapi must be the path of an OpenAPI document, or a map of spec and strict"))
		}
	}
	for _, f := range []struct {
		key string
		dst *map[string]string
	}{{"headers", &h.Headers}, {"cookies", &h.Cookies}} {
		v, ok := t[f.key]
		if !ok {
			continue
		}
		m, isMap := v.(map[string]any)
		if !isMap {
			errs = append(errs, fmt.Errorf("targets.http.%s must be a map of names to values", f.key))
			continue
		}
		*f.dst = map[string]string{}
		for _, name := range slices.Sorted(maps.Keys(m)) {
			switch val := m[name].(type) {
			case string:
				(*f.dst)[name] = val
			case int, int64, float64, bool:
				(*f.dst)[name] = fmt.Sprint(val)
			default:
				errs = append(errs, fmt.Errorf("targets.http.%s.%s must be a string", f.key, name))
			}
		}
	}
	if v, ok := t["timeout"]; ok {
		d, err := actionrpc.ParseTimeout(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("targets.http.timeout: %w", err))
		} else {
			h.Timeout = d
		}
	}
	return errs
}

// readGRPC reads targets.grpc t into g. What the grpc action reads itself,
// such as the files of proto, it checks when it is called.
func readGRPC(t map[string]any, g *grpcReq) []error {
	var errs []error
	s, isStr := t["addr"].(string)
	g.Addr = strings.TrimSpace(s)
	switch {
	case t["addr"] != nil && !isStr:
		errs = append(errs, errors.New("targets.grpc.addr must be a string"))
	case g.Addr == "":
		errs = append(errs, errors.New("targets.grpc.addr is required"))
	}
	g.Conn = map[string]any{}
	if v, ok := t["protocol"]; ok {
		p, _ := v.(string)
		if p != "grpc" && p != "connect" {
			errs = append(errs, errors.New("targets.grpc.protocol must be grpc or connect"))
		}
		g.Conn["protocol"] = p
	}
	for _, k := range []string{"tls", "insecure"} {
		if v, ok := t[k]; ok {
			b, err := toBool(v)
			if err != nil {
				errs = append(errs, fmt.Errorf("targets.grpc.%s must be true or false", k))
			}
			g.Conn[k] = b
		}
	}
	for _, k := range []string{"cert_file", "key_file", "ca_file"} {
		if v, ok := t[k]; ok {
			s, ok := v.(string)
			if !ok || strings.TrimSpace(s) == "" {
				errs = append(errs, fmt.Errorf("targets.grpc.%s must be the path of a file", k))
			}
			g.Conn[k] = s
		}
	}
	if v, ok := t["timeout"]; ok {
		d, err := actionrpc.ParseTimeout(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("targets.grpc.timeout: %w", err))
		} else {
			g.Conn["timeout"] = d.String()
		}
	}
	if v, ok := t["metadata"]; ok {
		m, isMap := v.(map[string]any)
		if !isMap {
			errs = append(errs, errors.New("targets.grpc.metadata must be a map of names to values"))
		}
		g.Metadata = map[string]string{}
		for _, name := range slices.Sorted(maps.Keys(m)) {
			switch val := m[name].(type) {
			case string:
				g.Metadata[name] = val
			case int, int64, float64, bool:
				g.Metadata[name] = fmt.Sprint(val)
			default:
				errs = append(errs, fmt.Errorf("targets.grpc.metadata.%s must be a string", name))
			}
		}
	}
	if v, ok := t["proto"]; ok {
		errs = append(errs, readProto(v, g)...)
	}
	return errs
}

// readGraphQL reads targets.graphql t into g.
func readGraphQL(t map[string]any, g *graphqlReq) []error {
	var errs []error
	s, isStr := t["url"].(string)
	g.URL = strings.TrimSpace(s)
	switch {
	case t["url"] != nil && !isStr:
		errs = append(errs, errors.New("targets.graphql.url must be a string"))
	case g.URL == "":
		errs = append(errs, errors.New("targets.graphql.url is required"))
	default:
		if u, err := url.Parse(g.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("targets.graphql.url %q must be an http or https URL", g.URL))
		}
	}
	if v, ok := t["headers"]; ok {
		m, isMap := v.(map[string]any)
		if !isMap {
			errs = append(errs, errors.New("targets.graphql.headers must be a map of names to values"))
		}
		g.Headers = map[string]string{}
		for _, name := range slices.Sorted(maps.Keys(m)) {
			switch val := m[name].(type) {
			case string:
				g.Headers[name] = val
			case int, int64, float64, bool:
				g.Headers[name] = fmt.Sprint(val)
			default:
				errs = append(errs, fmt.Errorf("targets.graphql.headers.%s must be a string", name))
			}
		}
	}
	if v, ok := t["timeout"]; ok {
		d, err := actionrpc.ParseTimeout(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("targets.graphql.timeout: %w", err))
		} else {
			g.Timeout = d
		}
	}
	return errs
}

// readMail reads targets.mail t into m.
func readMail(t map[string]any, m *mailReq) []error {
	var errs []error
	s, isStr := t["url"].(string)
	m.URL = strings.TrimSpace(s)
	switch {
	case t["url"] != nil && !isStr:
		errs = append(errs, errors.New("targets.mail.url must be a string"))
	case m.URL == "":
		errs = append(errs, errors.New("targets.mail.url is required"))
	default:
		if u, err := url.Parse(m.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			errs = append(errs, fmt.Errorf("targets.mail.url %q must be the http or https URL of a JMAP server", m.URL))
		}
	}
	if v, ok := t["basic_auth"]; ok {
		a, isMap := v.(map[string]any)
		user, userOK := a["username"].(string)
		pass, passOK := a["password"].(string)
		if !isMap || !userOK || !passOK || len(a) != 2 || user == "" {
			errs = append(errs, errors.New("targets.mail.basic_auth must be a map of username and password"))
		}
		m.Username, m.Password = user, pass
	}
	if v, ok := t["headers"]; ok {
		h, isMap := v.(map[string]any)
		if !isMap {
			errs = append(errs, errors.New("targets.mail.headers must be a map of names to values"))
		}
		m.Headers = map[string]string{}
		for _, name := range slices.Sorted(maps.Keys(h)) {
			switch val := h[name].(type) {
			case string:
				m.Headers[name] = val
			case int, int64, float64, bool:
				m.Headers[name] = fmt.Sprint(val)
			default:
				errs = append(errs, fmt.Errorf("targets.mail.headers.%s must be a string", name))
			}
		}
	}
	if v, ok := t["account_id"]; ok {
		s, isStr := v.(string)
		if !isStr || strings.TrimSpace(s) == "" {
			errs = append(errs, errors.New("targets.mail.account_id must be the id of an account"))
		}
		m.AccountID = strings.TrimSpace(s)
	}
	if v, ok := t["timeout"]; ok {
		d, err := actionrpc.ParseTimeout(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("targets.mail.timeout: %w", err))
		} else {
			m.Timeout, m.TimeoutSet = d, true
		}
	}
	return errs
}

// readDB reads targets.db t into d.
func readDB(t map[string]any, d *dbReq) []error {
	var errs []error
	s, isStr := t["dsn"].(string)
	d.DSN = strings.TrimSpace(s)
	switch {
	case t["dsn"] != nil && !isStr:
		errs = append(errs, errors.New("targets.db.dsn must be a string"))
	case d.DSN == "":
		errs = append(errs, errors.New("targets.db.dsn is required"))
	default:
		// The db action reads a scheme in lower case alone, as
		// postgresql:// and file: are, so it is given one.
		if i := strings.Index(d.DSN, ":"); i > 0 {
			d.DSN = strings.ToLower(d.DSN[:i]) + d.DSN[i:]
		}
		d.Driver = dbDriverOf(d.DSN)
		switch {
		case d.Driver == "":
			errs = append(errs, errors.New("targets.db.dsn must start with postgres://, postgresql://, mysql:// or file: for SQLite"))
		case d.Driver != "sqlite":
			// The error leaves the DSN out, as it holds the password.
			if _, err := url.Parse(d.DSN); err != nil {
				errs = append(errs, errors.New("targets.db.dsn is not a URL the db action can read: percent-encode a special character of its password, such as / ? # @ or a space"))
			}
		}
	}
	if v, ok := t["timeout"]; ok {
		dur, err := actionrpc.ParseTimeout(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("targets.db.timeout: %w", err))
		} else {
			d.Timeout = dur
		}
	}
	return errs
}

// dbDriverOf names the driver of dsn by its scheme, as the db action of
// Probe reads it, or "" for one it does not take.
func dbDriverOf(dsn string) string {
	dsn = strings.ToLower(dsn)
	switch {
	case strings.HasPrefix(dsn, "postgres://"), strings.HasPrefix(dsn, "postgresql://"):
		return "postgres"
	case strings.HasPrefix(dsn, "mysql://"):
		return "mysql"
	case strings.HasPrefix(dsn, "file:"):
		return "sqlite"
	}
	return ""
}

// readProto reads targets.grpc.proto into g: files, the .proto files, and
// import_paths, each a list of paths, and strict and request, as the grpc
// action takes them. Whether the files compile, the grpc action tells.
func readProto(v any, g *grpcReq) []error {
	p, isMap := v.(map[string]any)
	if !isMap {
		return []error{errors.New("targets.grpc.proto must be a map of files, and import_paths, strict and request if need be, as the grpc action takes it")}
	}
	var errs []error
	paths := func(k string) {
		l, ok := p[k].([]any)
		if !ok {
			errs = append(errs, fmt.Errorf("targets.grpc.proto.%s must be a list of paths", k))
			return
		}
		for _, e := range l {
			if s, ok := e.(string); !ok || strings.TrimSpace(s) == "" {
				errs = append(errs, fmt.Errorf("targets.grpc.proto.%s must be a list of paths", k))
				return
			}
		}
		if k == "files" && len(l) == 0 {
			errs = append(errs, errors.New("targets.grpc.proto.files names no file"))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(p)) {
		switch k {
		case "files", "import_paths":
			paths(k)
		case "strict", "request":
			b, err := toBool(p[k])
			if err != nil {
				errs = append(errs, fmt.Errorf("targets.grpc.proto.%s must be true or false", k))
			}
			p[k] = b
		default:
			errs = append(errs, fmt.Errorf("unknown parameter targets.grpc.proto.%s; the parameters are files, import_paths, strict and request", k))
		}
	}
	if _, ok := p["files"]; !ok {
		errs = append(errs, errors.New("targets.grpc.proto.files is required"))
	}
	g.Proto = p
	return errs
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
