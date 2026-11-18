// Package action serves the agent as a Probe action: it reads the step's
// with, readies the targets and the model, runs the test, and returns how
// it came out, with its evidence written to files. The browser is driven
// through probe-browser, an iOS app through probe-ios, an Android app
// through probe-android, and an HTTP API and a gRPC API through the http
// and grpc actions of Probe, which the agent calls as a step of the run
// would.
package action

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/hashicorp/go-hclog"
	"github.com/mozership/probe"
	"github.com/mozership/probe-browser/storagestate"
	"github.com/mozership/probe/actionref"
	"github.com/mozership/probe/actionrpc"
	"github.com/mozership/probe/style"

	"github.com/mozership/probe-agent/internal/llm"
	"github.com/mozership/probe-agent/internal/page"
	"github.com/mozership/probe-agent/internal/scenario"
)

// Action is the agent action.
type Action struct {
	log hclog.Logger
	// ctx ends when Probe stops the action, so that the browser is closed
	// rather than left running.
	ctx context.Context
}

// Serve serves the action until Probe closes the connection.
func Serve() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	actionrpc.Serve(func(log hclog.Logger) actionrpc.Action {
		return &Action{log: log, ctx: ctx}
	})
}

// Run runs the test the step asks for, under a Probe that tells the
// action nothing of the step: it can drive no target through an action.
func (a *Action) Run(with map[string]any) (map[string]any, error) {
	ret, _, err := a.RunStep(actionrpc.Call{With: with})
	return ret, err
}

// RunStep runs the test the step asks for. The browser and an iOS app are
// driven through probe-browser and probe-ios, by the names the workflow
// gives them under actions, and an HTTP API and a gRPC API through the
// http and grpc actions of Probe, as a step of the run would call them.
func (a *Action) RunStep(call actionrpc.Call) (map[string]any, map[string]any, error) {
	actionrpc.LogParams(a.log, "received agent action request", forLog(call.With))
	ret, err := a.run(call)
	actionrpc.LogOutcome(a.log, "agent test", ret, err)
	return ret, nil, err
}

func (a *Action) run(call actionrpc.Call) (map[string]any, error) {
	start := time.Now()
	r, err := parse(call.With)
	if err != nil {
		return nil, err
	}
	// The targets are driven through actions, probe-ios and probe-browser,
	// which one caller calls, so that each keeps its state from one call to
	// the next, and is cleaned up once the run is over.
	// The records of the actions called hide the secrets of the agent,
	// which the tester types into them, under whatever names they have:
	// the caller hides only the credentials it recognizes.
	caller := probe.NewActionCaller(call, "", probe.WithCallerLogOutput(secretsHidden(os.Stderr, r.Secrets)))
	var ios *scenario.App
	if r.IOS != nil {
		if ios, err = appTarget(caller, call.Actions, probeIOS, targetIOS, *r.IOS); err != nil {
			return nil, err
		}
	}
	var android *scenario.App
	if r.Android != nil {
		if android, err = appTarget(caller, call.Actions, probeAndroid, targetAndroid, *r.Android); err != nil {
			return nil, err
		}
	}
	var api *scenario.HTTP
	if r.HTTP != nil {
		// The http action is called by a caller of its own, whose guard
		// lets it reach the host of the API alone, so that a redirect to
		// another host, which would carry the headers the workflow fixes,
		// is refused.
		hc := call
		hc.Guard = apiGuard(call.Guard, r.HTTP.URL)
		httpCaller := probe.NewActionCaller(hc, "", probe.WithCallerLogOutput(secretsHidden(os.Stderr, r.Secrets)))
		if api, err = httpTarget(httpCaller, call.Actions, *r.HTTP); err != nil {
			return nil, err
		}
	}
	var rpc *scenario.GRPC
	if r.GRPC != nil {
		if rpc, err = grpcTarget(caller, call.Actions, *r.GRPC); err != nil {
			return nil, err
		}
	}
	var browser string
	if r.Browser {
		if browser, err = actionNamed(call.Actions, probeBrowser, "targets.browser drives the browser", "browser"); err != nil {
			return nil, err
		}
		// The storage state is read before the browser starts, so that a
		// missing file, as when the step that writes it failed, fails the
		// step at once.
		if r.StorageState != "" {
			if _, err = storagestate.Read(r.StorageState); err != nil {
				return nil, fmt.Errorf("storage_state: %w", err)
			}
		}
	}
	var rec *scenario.Recording
	if r.Cache != "" && r.CacheMode != cacheRefresh {
		if rec, err = loadRecording(r.Cache); err != nil {
			return nil, err
		}
	}
	model, err := newModel(r)
	if err != nil {
		return nil, err
	}
	ev, err := newEvidence(r.EvidenceDir)
	if err != nil {
		return nil, err
	}
	targets := scenario.Targets{IOS: ios, Android: android, HTTP: api, GRPC: rpc}
	if r.Browser {
		tab, err := page.NewBrowser(func(ctx context.Context, with map[string]any) (map[string]any, error) {
			return callRes(ctx, caller, browser, with)
		}, page.Options{Headless: r.Headless, Width: r.Width, Height: r.Height, StorageState: r.StorageState})
		if err != nil {
			return nil, err
		}
		defer tab.Close()
		targets.Browser = tab
	}

	cfg := scenario.Config{
		Task:     r.Task,
		Expect:   r.Expect,
		Persona:  r.Persona,
		Goal:     r.Goal,
		StartURL: r.URL,
		Hosts:    r.AllowHosts,
		Secrets:  r.Secrets,
		MaxSteps: r.MaxSteps,
		Timeout:  r.Timeout,
		Save:     ev.save,
		// A recording is replayed in any mode of the cache but refresh,
		// and written in auto and refresh.
		Recording: rec,
		Record:    r.Cache != "" && (r.CacheMode == cacheAuto || r.CacheMode == cacheRefresh),
		Strict:    r.Cache != "" && r.CacheMode == cacheStrict,
		Viewport:  scenario.Viewport{Width: r.Width, Height: r.Height},
	}
	o, err := scenario.Run(a.ctx, cfg, targets, model)
	if err != nil {
		return nil, err
	}
	a.log.Info("agent test finished", "verdict", o.Verdict, "steps", len(o.Steps), "evidence", ev.dir)

	f, err := ev.write(r, o)
	if err != nil {
		return nil, err
	}
	if o.Recording != nil {
		if err := saveRecording(r.Cache, o.Recording); err != nil {
			return nil, err
		}
		f.Recording = r.Cache
		a.log.Info("recorded the test", "cache", r.Cache, "steps", len(o.Recording.Steps))
	}
	return result(r, o, f, time.Since(start))
}

// probeIOS and probeBrowser are the repositories of probe-ios and
// probe-browser, by which the agent finds the names the workflow gives
// them.
const (
	probeIOS     = "github.com/mozership/probe-ios"
	probeAndroid = "github.com/mozership/probe-android"
	probeBrowser = "github.com/mozership/probe-browser"
)

// callRes calls the action name with with, and returns its res. A failed
// call returns its res too, when it has one, as of the console errors of
// the page.
func callRes(ctx context.Context, caller *probe.ActionCaller, name string, with map[string]any) (map[string]any, error) {
	cr, err := caller.Call(ctx, name, with)
	if err != nil {
		return nil, err
	}
	res, ok := cr.Result["res"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s returned no res", name)
	}
	return res, nil
}

// appTarget returns the app of t, the targets.<kind> of the step, driven
// through the action of repo, probe-ios or probe-android: the one the
// workflow names under actions whose reference is repo, whatever its name.
func appTarget(caller *probe.ActionCaller, actions map[string]string, repo, kind string, t appReq) (*scenario.App, error) {
	name, err := actionNamed(actions, repo, "targets."+kind+" drives the app", kind)
	if err != nil {
		return nil, err
	}
	return &scenario.App{
		App:    t.App,
		Device: t.Device,
		Call: func(ctx context.Context, actions []map[string]any) (map[string]any, error) {
			return callRes(ctx, caller, name, appWith(t, actions))
		},
	}, nil
}

// appWith is the with of a call of probe-ios or probe-android, which take
// the same, that takes actions on the app of t.
func appWith(t appReq, actions []map[string]any) map[string]any {
	list := make([]any, len(actions))
	for i, a := range actions {
		list[i] = a
	}
	with := map[string]any{"app": t.App, "actions": list}
	if t.Device != "" {
		with["device"] = t.Device
	}
	if t.Timeout > 0 {
		with["timeout"] = t.Timeout.String()
	}
	return with
}

// httpAction is the http action of Probe, which an HTTP API is called
// through.
const httpAction = "http"

// httpTarget returns the API of t, called through the http action of
// Probe, which the Probe that runs the agent starts. The OpenAPI document is
// read here, for the tester to be given it, so that one that cannot be
// read fails the step before the model is called.
func httpTarget(caller *probe.ActionCaller, actions map[string]string, t httpReq) (*scenario.HTTP, error) {
	if err := builtinCallable(actions, httpAction, "v1.37.0"); err != nil {
		return nil, err
	}
	api := &scenario.HTTP{URL: t.URL, Fixed: slices.Sorted(maps.Keys(t.Headers))}
	if len(t.Cookies) > 0 && !slices.ContainsFunc(api.Fixed, func(n string) bool { return strings.EqualFold(n, "cookie") }) {
		api.Fixed = append(api.Fixed, "Cookie")
	}
	if spec := t.spec(); spec != "" {
		b, err := os.ReadFile(spec)
		if err != nil {
			return nil, fmt.Errorf("targets.http.openapi: %w", err)
		}
		api.OpenAPI = string(b)
	}
	api.Call = func(ctx context.Context, req scenario.HTTPRequest) (scenario.HTTPResponse, error) {
		cr, err := caller.Call(ctx, httpAction, httpWith(t, req))
		if err != nil {
			return scenario.HTTPResponse{}, err
		}
		return httpResponse(cr)
	}
	return api, nil
}

// builtinCallable returns an error when the action of Probe name cannot be
// called: when the Probe that runs the agent does not say where it is, or
// when the workflow gives its name to another action. since is the
// version of Probe the target needs.
func builtinCallable(actions map[string]string, name, since string) error {
	if os.Getenv(probe.ExecutableEnv) == "" {
		return fmt.Errorf("targets.%s calls the %s action of Probe, which needs Probe %s or later: %s is not set", name, name, since, probe.ExecutableEnv)
	}
	if uses, ok := actions[name]; ok {
		return fmt.Errorf("targets.%s calls the %s action of Probe, but actions of the workflow give the name %s to %s; give it another name", name, name, name, uses)
	}
	return nil
}

// grpcAction is the grpc action of Probe, which a gRPC API is called
// through.
const grpcAction = "grpc"

// grpcTarget returns the API of t, called through the grpc action of
// Probe, which lists its services by reflection from Probe v1.38.0.
func grpcTarget(caller *probe.ActionCaller, actions map[string]string, t grpcReq) (*scenario.GRPC, error) {
	if err := builtinCallable(actions, grpcAction, "v1.38.0"); err != nil {
		return nil, err
	}
	rpc := &scenario.GRPC{Addr: t.Addr, Fixed: slices.Sorted(maps.Keys(t.Metadata)), Proto: t.Proto != nil}
	rpc.List = func(ctx context.Context, service string) (map[string]any, error) {
		with := grpcWith(t, nil)
		with["list"] = true
		if service != "" {
			with["service"] = service
		}
		return callRes(ctx, caller, grpcAction, with)
	}
	rpc.Call = func(ctx context.Context, req scenario.GRPCRequest) (scenario.GRPCResponse, error) {
		cr, err := caller.Call(ctx, grpcAction, grpcWith(t, &req))
		if err != nil {
			return scenario.GRPCResponse{}, err
		}
		return grpcResponse(cr)
	}
	return rpc, nil
}

// grpcWith is the with of a call of the grpc action to the API of t, with
// the metadata t fixes: of req, or of a listing when req is nil.
func grpcWith(t grpcReq, req *scenario.GRPCRequest) map[string]any {
	with := maps.Clone(t.Conn)
	if with == nil {
		with = map[string]any{}
	}
	with["addr"] = t.Addr
	md := map[string]any{}
	if req != nil {
		with["service"], with["method"] = req.Service, req.Method
		if req.Body != "" {
			with["body"] = req.Body
		}
		for k, v := range req.Metadata {
			md[k] = v
		}
	}
	for k, v := range t.Metadata {
		md[k] = v
	}
	if len(md) > 0 {
		with["metadata"] = md
	}
	if t.Proto != nil {
		with["proto"] = maps.Clone(t.Proto)
	}
	return with
}

// grpcResponse reads the answer of a call of the grpc action, and what the
// .proto files made of it.
func grpcResponse(cr *probe.CallResult) (scenario.GRPCResponse, error) {
	res, ok := cr.Result["res"].(map[string]any)
	if !ok {
		return scenario.GRPCResponse{}, fmt.Errorf("%s returned no res", grpcAction)
	}
	var out scenario.GRPCResponse
	out.Code, _ = res["status_code"].(string)
	out.Message, _ = res["status_message"].(string)
	if md, ok := res["metadata"].(map[string]any); ok {
		out.Metadata = map[string]string{}
		for k, v := range md {
			out.Metadata[k] = fmt.Sprint(v)
		}
	}
	// The body comes as JSON text, or parsed, with the text in rawbody.
	if raw, ok := res["rawbody"].(string); ok {
		out.Body = raw
	} else if body, ok := res["body"].(string); ok {
		out.Body = body
	} else if res["body"] != nil {
		b, err := json.Marshal(res["body"])
		if err != nil {
			return scenario.GRPCResponse{}, err
		}
		out.Body = string(b)
	}
	// A method whose responses do not stream may come with an empty list
	// of them, which is no stream.
	if msgs, ok := res["messages"].([]any); ok && len(msgs) > 0 {
		out.Messages = []string{}
		for _, m := range msgs {
			b, err := json.Marshal(m)
			if err != nil {
				return scenario.GRPCResponse{}, err
			}
			out.Messages = append(out.Messages, string(b))
		}
	}
	if c := cr.Contract; c != nil {
		if c.Matched != nil {
			out.Operation = c.Matched.Operation
		}
		for _, v := range c.Violations {
			out.Violations = append(out.Violations, scenario.Violation{In: v.In, Field: v.Field, Reason: v.Reason, Message: v.Message})
		}
	}
	return out, nil
}

// apiGuard returns g kept to the host of the base URL raw, for the http
// action, which keeps to the guard, to refuse a request, or a redirect, to
// any other host. A run that does not allow the host keeps its own hosts,
// which refuse it all the same.
func apiGuard(g actionrpc.Guard, raw string) actionrpc.Guard {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return g
	}
	// The port is named, that of the scheme when the URL names none, so
	// that a redirect cannot go to another port, nor from https to http.
	host := u.Host
	if u.Port() == "" {
		port := "443"
		if u.Scheme == "http" {
			port = "80"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	if len(g.AllowHosts) == 0 || g.AllowsHost(host) {
		g.AllowHosts = []string{host}
	}
	// The http action keeps to both kinds of guard, as its package
	// declares, which a guard of a run with none may not hold.
	return g.WithKeeps(httpAction, []string{actionrpc.KindReadOnly, actionrpc.KindAllowHost})
}

// httpWith is the with of a call of the http action that sends req to the
// API of t, with the headers and the cookies t fixes, and keeps the cookies
// the API sets for the next call.
func httpWith(t httpReq, req scenario.HTTPRequest) map[string]any {
	headers := map[string]any{}
	for k, v := range req.Headers {
		headers[k] = v
	}
	for k, v := range t.Headers {
		headers[k] = v
	}
	with := map[string]any{
		"method":       req.Method,
		"url":          req.URL,
		"keep_cookies": true,
	}
	if len(headers) > 0 {
		with["headers"] = headers
	}
	if req.Body != "" {
		with["body"] = req.Body
	}
	if len(t.Cookies) > 0 {
		cookies := map[string]any{}
		for k, v := range t.Cookies {
			cookies[k] = v
		}
		with["cookies"] = cookies
	}
	if t.OpenAPI != nil {
		with["openapi"] = maps.Clone(t.OpenAPI)
	}
	if t.Timeout > 0 {
		with["timeout"] = t.Timeout.String()
	}
	return with
}

// httpResponse reads the response of a call of the http action, and what
// the OpenAPI document made of it.
func httpResponse(cr *probe.CallResult) (scenario.HTTPResponse, error) {
	res, ok := cr.Result["res"].(map[string]any)
	if !ok {
		return scenario.HTTPResponse{}, fmt.Errorf("%s returned no res", httpAction)
	}
	var out scenario.HTTPResponse
	out.Code, _ = toInt(res["code"])
	out.Status, _ = res["status"].(string)
	if h, ok := res["headers"].(map[string]any); ok {
		out.Headers = map[string]string{}
		for k, v := range h {
			out.Headers[k] = fmt.Sprint(v)
		}
	}
	out.File, _ = res["filepath"].(string)
	// A JSON body comes parsed, with the text it was parsed from in
	// rawbody.
	if raw, ok := res["rawbody"].(string); ok {
		out.Body = raw
	} else if body, ok := res["body"].(string); ok {
		out.Body = body
	} else if res["body"] != nil {
		b, err := json.Marshal(res["body"])
		if err != nil {
			return scenario.HTTPResponse{}, err
		}
		out.Body = string(b)
	}
	if c := cr.Contract; c != nil {
		if c.Matched != nil {
			out.Operation = c.Matched.Operation
		}
		for _, v := range c.Violations {
			out.Violations = append(out.Violations, scenario.Violation{In: v.In, Field: v.Field, Reason: v.Reason, Message: v.Message})
		}
	}
	return out, nil
}

// actionNamed returns the name actions give the action of repo, which
// what says it drives its target through, as the example name does.
func actionNamed(actions map[string]string, repo, what, example string) (string, error) {
	var names []string
	for name, uses := range actions {
		ref, err := actionref.Parse(uses)
		if err == nil && ref.Local == "" && "github.com/"+ref.Owner+"/"+ref.Repo == repo && ref.Dir == "" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	short := repo[strings.LastIndex(repo, "/")+1:]
	switch len(names) {
	case 0:
		return "", fmt.Errorf("%s through %s: name it under actions of the workflow, as %s: %s@<commit SHA>", what, short, example, repo)
	case 1:
		return names[0], nil
	}
	return "", fmt.Errorf("%s through one action, but %s under actions each name %s", what, strings.Join(names, " and "), repo)
}

// newModel returns the model of the provider r names. The API key of a
// provider other than anthropic is read from its environment variable
// here, so that a test without one ends before the browser starts; the
// Anthropic SDK finds its own, as in ANTHROPIC_API_KEY, except an OIDC token
// of GitHub Actions, which is asked for anew for each exchange.
func newModel(r req) (llm.Model, error) {
	if r.Provider == anthropic {
		var opts []option.RequestOption
		if r.BaseURL != "" {
			opts = append(opts, option.WithBaseURL(r.BaseURL))
		}
		if fed, ok := llm.GitHubActionsFederation(os.Getenv, http.DefaultClient); ok {
			opts = append(opts, fed)
		}
		return llm.NewAnthropic(r.Model, r.Effort, opts...), nil
	}
	p := llm.Providers[r.Provider]
	key := os.Getenv(p.KeyEnv)
	if key == "" {
		return nil, fmt.Errorf("the provider %s needs its API key in the environment variable %s", r.Provider, p.KeyEnv)
	}
	if r.BaseURL != "" {
		p.BaseURL = r.BaseURL
	}
	return llm.NewCompatible(p, r.Model, r.Effort, key, r.Vision), nil
}

// result is what the action returns: req, res, rt and status, as Probe
// reads them. It is turned to JSON and back, so that every value has a
// type the connection to Probe carries.
func result(r req, o scenario.Outcome, f files, took time.Duration) (map[string]any, error) {
	res := map[string]any{
		"mode":           o.Mode,
		"verdict":        o.Verdict,
		"reason":         o.Reason,
		"evidence":       o.Evidence,
		"stopped":        o.Stopped,
		"summary":        o.Summary,
		"steps":          o.Steps,
		"url":            o.URL,
		"title":          o.Title,
		"targets":        o.Targets,
		"console_errors": o.ConsoleErrors,
		"network_errors": o.NetworkErrors,
		"metrics":        o.Metrics,
		"usage":          o.Usage,
		"model":          o.Model,
		"filepaths": map[string]string{
			"report":     f.Report,
			"trace":      f.Trace,
			"screenshot": o.FinalScreenshot,
		},
		"report": shortReport(r, o, f),
		// The notes are for people, for a later step to post where they
		// work, as on a pull request or in a chat.
		"notes": notesOf(o),
	}
	if o.Mode == scenario.ModePersona {
		res["findings"] = o.Findings
	}
	if c := cacheOf(r, o, f.Recording != ""); c != nil {
		res["cache"] = c
	}
	reqMap := map[string]any{
		"targets":   r.kinds(),
		"provider":  r.Provider,
		"model":     r.Model,
		"effort":    r.Effort,
		"max_steps": r.MaxSteps,
		"timeout":   r.Timeout.String(),
	}
	if r.Browser {
		reqMap["url"] = r.URL
	}
	if r.GRPC != nil {
		var addr strings.Builder
		_, _ = io.WriteString(secretsHidden(&addr, r.Secrets), r.GRPC.Addr)
		reqMap["addr"] = addr.String()
	}
	if r.HTTP != nil {
		// The base URL can hold a secret, as a token in its query.
		var api strings.Builder
		_, _ = io.WriteString(secretsHidden(&api, r.Secrets), r.HTTP.URL)
		reqMap["api"] = api.String()
	}
	// The app and the device are of the iOS app, or of the Android one in
	// a test without an iOS app.
	for _, a := range []*appReq{r.Android, r.IOS} {
		if a != nil {
			reqMap["app"] = a.App
			delete(reqMap, "device")
			if a.Device != "" {
				reqMap["device"] = a.Device
			}
		}
	}
	if o.Mode == scenario.ModePersona {
		reqMap["persona"], reqMap["goal"] = r.Persona, r.Goal
	} else {
		reqMap["task"], reqMap["expect"] = r.Task, r.Expect
	}
	ret := map[string]any{"req": reqMap, "res": res, "rt": took.String(), "status": 0}
	b, err := json.Marshal(ret)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// verdictMark is the mark of verdict in what Probe prints under the step,
// as Probe marks a step: a test that passed as one that passed, one that
// failed as one that failed, and one that could not be carried out, or a
// verdict of no other kind, as a warning.
func verdictMark(verdict string) style.Mark {
	switch verdict {
	case scenario.Pass:
		return style.Success
	case scenario.Fail:
		return style.Failure
	default:
		return style.Warning
	}
}

// severityMark is the mark of a finding of severity in what Probe prints
// under the step: the mark of the level of its note.
func severityMark(severity string) style.Mark {
	return style.NoteLevel(levels[severity])
}

// shortReport is what Probe prints under the step: the verdict, why, and
// where the full report is. It is marked as Probe marks a step, in color
// when Probe prints to a terminal.
func shortReport(r req, o scenario.Outcome, f files) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %s\n", verdictMark(o.Verdict), o.Verdict, oneLine(o.Reason))
	for _, e := range o.Evidence {
		if e.Verified {
			fmt.Fprintf(&b, "  evidence: %q\n", e.Quote)
		}
	}
	if o.Mode == scenario.ModePersona {
		fmt.Fprintf(&b, "  %d findings\n", len(o.Findings))
		for _, f := range o.Findings {
			unverified := ""
			if !f.Verified {
				unverified = " (unverified)"
			}
			fmt.Fprintf(&b, "  %s %s: %s%s\n", severityMark(f.Severity), f.Severity, oneLine(f.Title), unverified)
		}
	}
	if c := cacheOf(r, o, f.Recording != ""); c != nil {
		fmt.Fprintf(&b, "  cache: %s", c.Status)
		switch c.Status {
		case scenario.ReplayHit:
			fmt.Fprintf(&b, ", %d steps replayed without the model", c.Replayed)
		case scenario.ReplayPartial:
			if o.Stopped == scenario.StoppedStale {
				fmt.Fprintf(&b, ", %d steps replayed, stale at step %d: %s", c.Replayed, c.DivergedAt, c.Reason)
			} else {
				fmt.Fprintf(&b, ", %d steps replayed, then the tester from step %d: %s", c.Replayed, c.DivergedAt, c.Reason)
			}
		case scenario.ReplayMiss:
			if c.Reason != "" {
				fmt.Fprintf(&b, " (%s)", c.Reason)
			}
		}
		if c.Saved {
			fmt.Fprintf(&b, ", recorded to %s", c.Path)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "  %d steps, stopped: %s, %s\n", len(o.Steps), o.Stopped, o.Duration.Round(time.Second))
	fmt.Fprintf(&b, "  report: %s", f.Report)
	return b.String()
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// secretsHidden returns a writer to w that hides the values of secrets,
// each as <secret:NAME>, in each record written to it: in any case, as an
// application can give an email address in lower case, and as JSON or Go
// quote them, short ones too, as a PIN, though a short one can hide what
// is no secret.
func secretsHidden(w io.Writer, secrets map[string]string) io.Writer {
	names := slices.Sorted(maps.Keys(secrets))
	// The longest first, so that one inside another is not hidden first.
	slices.SortStableFunc(names, func(a, b string) int { return len(secrets[b]) - len(secrets[a]) })
	h := &hidingWriter{w: w}
	for _, name := range names {
		v := secrets[name]
		if v == "" {
			continue
		}
		forms := []string{regexp.QuoteMeta(v)}
		for _, q := range []string{jsonQuoted(v), jsonQuoted(jsonQuoted(v)), strconv.Quote(v)} {
			if inner := q[1 : len(q)-1]; inner != v {
				forms = append(forms, regexp.QuoteMeta(inner))
			}
		}
		h.res = append(h.res, regexp.MustCompile("(?i)"+strings.Join(forms, "|")))
		h.labels = append(h.labels, "<secret:"+name+">")
	}
	return h
}

func jsonQuoted(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// hidingWriter writes to w what is written to it, with what res match
// replaced by their labels.
type hidingWriter struct {
	w      io.Writer
	res    []*regexp.Regexp
	labels []string
}

func (h *hidingWriter) Write(p []byte) (int, error) {
	s := string(p)
	for i, re := range h.res {
		s = re.ReplaceAllLiteralString(s, h.labels[i])
	}
	if _, err := io.WriteString(h.w, s); err != nil {
		return 0, err
	}
	return len(p), nil
}
