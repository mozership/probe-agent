package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/anthropics/anthropic-sdk-go/option"
)

// anthropicAudience is the audience of an OIDC token the Claude API takes.
const anthropicAudience = "https://api.anthropic.com"

// GitHubActionsFederation returns the option by which the Anthropic SDK
// exchanges an OIDC token of the job of GitHub Actions for an access token,
// through workload identity federation, asking GitHub for a new token for
// each exchange. The API takes a token once alone, refusing it again as
// jti_reused, and every step of a workflow runs the agent anew; a token
// kept in a file, as ANTHROPIC_IDENTITY_TOKEN_FILE names one, serves only
// the first exchange.
//
// It applies when the job may request a token, with id-token: write, and
// ANTHROPIC_FEDERATION_RULE_ID and ANTHROPIC_ORGANIZATION_ID name the rule,
// with ANTHROPIC_SERVICE_ACCOUNT_ID and ANTHROPIC_WORKSPACE_ID beside them
// when the rule wants them. A credential the SDK finds itself, such as
// ANTHROPIC_API_KEY or a token named by ANTHROPIC_IDENTITY_TOKEN_FILE, is
// left to it, and ok is false then.
func GitHubActionsFederation(getenv func(string) string, client *http.Client) (opt option.RequestOption, ok bool) {
	for _, own := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_PROFILE", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_IDENTITY_TOKEN"} {
		if getenv(own) != "" {
			return nil, false
		}
	}
	rule, org := getenv("ANTHROPIC_FEDERATION_RULE_ID"), getenv("ANTHROPIC_ORGANIZATION_ID")
	requestURL, requestToken := getenv("ACTIONS_ID_TOKEN_REQUEST_URL"), getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN")
	if rule == "" || org == "" || requestURL == "" || requestToken == "" {
		return nil, false
	}
	fetch := func(ctx context.Context) (string, error) {
		return githubIDToken(ctx, client, requestURL, requestToken)
	}
	return option.WithFederationTokenProvider(fetch, option.FederationOptions{
		FederationRuleID: rule,
		OrganizationID:   org,
		ServiceAccountID: getenv("ANTHROPIC_SERVICE_ACCOUNT_ID"),
		WorkspaceID:      getenv("ANTHROPIC_WORKSPACE_ID"),
	}), true
}

// githubIDToken asks GitHub Actions for an OIDC token of the job for the
// audience of the Claude API.
func githubIDToken(ctx context.Context, client *http.Client, requestURL, requestToken string) (string, error) {
	u, err := url.Parse(requestURL)
	if err != nil {
		return "", fmt.Errorf("the URL to request an OIDC token of GitHub Actions: %w", err)
	}
	q := u.Query()
	q.Set("audience", anthropicAudience)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "bearer "+requestToken)
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("requesting an OIDC token of GitHub Actions: %w", err)
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("reading the OIDC token of GitHub Actions: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("requesting an OIDC token of GitHub Actions: %s", res.Status)
	}
	var token struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(body, &token); err != nil {
		return "", fmt.Errorf("reading the OIDC token of GitHub Actions: %w", err)
	}
	if token.Value == "" {
		return "", fmt.Errorf("GitHub Actions returned no OIDC token")
	}
	return token.Value, nil
}
