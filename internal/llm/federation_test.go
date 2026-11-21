package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// actionsEnv is the environment of a job of GitHub Actions with id-token:
// write and the IDs of a federation rule.
func actionsEnv(requestURL string) map[string]string {
	return map[string]string{
		"ANTHROPIC_FEDERATION_RULE_ID":   "fdrl_1",
		"ANTHROPIC_ORGANIZATION_ID":      "org-1",
		"ANTHROPIC_SERVICE_ACCOUNT_ID":   "svac_1",
		"ANTHROPIC_WORKSPACE_ID":         "wrkspc_1",
		"ACTIONS_ID_TOKEN_REQUEST_URL":   requestURL,
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN": "request-token",
	}
}

func getenv(env map[string]string) func(string) string {
	return func(k string) string { return env[k] }
}

func TestGitHubActionsFederation_AppliesOnlyInAJobWithTheRule(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  map[string]string
		want bool
	}{
		{name: "a job with the rule", want: true},
		{name: "no id-token: write", set: map[string]string{"ACTIONS_ID_TOKEN_REQUEST_URL": "", "ACTIONS_ID_TOKEN_REQUEST_TOKEN": ""}},
		{name: "no rule", set: map[string]string{"ANTHROPIC_FEDERATION_RULE_ID": ""}},
		{name: "no organization", set: map[string]string{"ANTHROPIC_ORGANIZATION_ID": ""}},
		{name: "an API key", set: map[string]string{"ANTHROPIC_API_KEY": "sk"}},
		{name: "an auth token", set: map[string]string{"ANTHROPIC_AUTH_TOKEN": "t"}},
		{name: "a profile", set: map[string]string{"ANTHROPIC_PROFILE": "p"}},
		{name: "a token file", set: map[string]string{"ANTHROPIC_IDENTITY_TOKEN_FILE": "/tmp/t"}},
		{name: "a token", set: map[string]string{"ANTHROPIC_IDENTITY_TOKEN": "jwt"}},
	} {
		env := actionsEnv("https://token.actions.test/?api-version=2.0")
		maps.Copy(env, tt.set)
		if _, ok := GitHubActionsFederation(getenv(env), http.DefaultClient); ok != tt.want {
			t.Errorf("%s: ok %v, want %v", tt.name, ok, tt.want)
		}
	}
}

// Each exchange sends a token GitHub issued for it, as the Claude API
// refuses a token it has taken once.
func TestGitHubActionsFederation_ExchangesANewTokenEachTime(t *testing.T) {
	var mu sync.Mutex
	var issued int
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "bearer request-token" {
			t.Errorf("authorization %q", got)
		}
		if got := r.URL.Query(); got.Get("audience") != "https://api.anthropic.com" || got.Get("api-version") != "2.0" {
			t.Errorf("query %v", got)
		}
		mu.Lock()
		issued++
		n := issued
		mu.Unlock()
		fmt.Fprintf(w, `{"count":1,"value":"jwt-%d"}`, n)
	}))
	t.Cleanup(github.Close)

	var assertions []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/oauth/token":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["federation_rule_id"] != "fdrl_1" || body["organization_id"] != "org-1" || body["service_account_id"] != "svac_1" || body["workspace_id"] != "wrkspc_1" {
				t.Errorf("exchange %v", body)
			}
			mu.Lock()
			assertions = append(assertions, body["assertion"])
			mu.Unlock()
			fmt.Fprintf(w, `{"access_token":"access-for-%s","token_type":"Bearer","expires_in":3600}`, body["assertion"])
		case "/v1/messages":
			if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer access-for-jwt-") {
				t.Errorf("authorization %q", got)
			}
			fmt.Fprint(w, endReply)
		default:
			t.Errorf("path %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(api.Close)

	// Two exchanges, as by the agents of two steps of a workflow.
	for range 2 {
		fed, ok := GitHubActionsFederation(getenv(actionsEnv(github.URL+"/?api-version=2.0")), http.DefaultClient)
		if !ok {
			t.Fatal("not applied")
		}
		c := anthropic.NewClient(option.WithoutEnvironmentDefaults(), option.WithBaseURL(api.URL), option.WithMaxRetries(0), fed)
		_, err := c.Messages.New(context.Background(), anthropic.MessageNewParams{
			Model: "claude-opus-5-5", MaxTokens: 16,
			Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock("hi"))},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(assertions) != 2 || assertions[0] == assertions[1] {
		t.Errorf("assertions %v, want a token of its own for each exchange", assertions)
	}
}

func TestGitHubActionsFederation_SaysWhyGitHubGaveNoToken(t *testing.T) {
	github := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	t.Cleanup(github.Close)
	_, err := githubIDToken(context.Background(), http.DefaultClient, github.URL+"/?api-version=2.0", "request-token")
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Errorf("err %v", err)
	}
}
