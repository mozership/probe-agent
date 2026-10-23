# probe-agent

A [Probe](https://github.com/mozership/probe) action that tests a web application from a task written in words. A model, the tester, uses the application through a real Chrome to carry out the task; another call of the model, the judge, then decides from what the pages showed whether the expectation holds.

```yaml
name: Sign-up
actions:
  agent: github.com/mozership/probe-agent@<commit SHA>
secrets:
  - ANTHROPIC_API_KEY
  - TEST_PASSWORD
jobs:
- name: sign-up
  steps:
  - name: A new user signs up
    uses: agent
    with:
      url: https://staging.example.com/
      task: |
        As a new user of this service, sign up with the email address
        {secret:EMAIL} and the password {secret:PASSWORD}.
      expect: |
        The account is created, and the dashboard greets the user.
      secrets:
        EMAIL: "qa+{{random_str(8)}}@example.com"
        PASSWORD: "{{TEST_PASSWORD}}"
    timeout: 5m
    test: res.verdict == "pass"
```

A test in words is not deterministic: the same step can take another path, or come to another verdict, from one run to the next. Check what matters with a deterministic step after it where you can, such as an `http` or `db` step that the account exists, and count the passes over runs with [`repeat.test`](https://github.com/mozership/probe) rather than trusting one run.

## How it works

1. Chrome opens `url`.
2. The tester is given `task` and `expect`, and the [browser toolset](https://platform.claude.com/docs/en/agents-and-tools/tool-use/browser-use-tool) of the Claude API: it reads the page as a tree of elements with refs, clicks, types, fills in forms and takes screenshots, until it calls `finish`.
3. The judge is given the task, the expectation, the steps, the texts the pages showed, the final page and its screenshot, and the console errors and failed requests. It gives `pass` or `fail`, quoting the pages.
4. Each quote is looked for in what the pages showed. A `pass` that quotes nothing the pages showed is not taken, and the verdict is `blocked`.

The tester is told to describe what it saw, not to judge, so that the verdict comes from a call that did not do the work.

## Parameters

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `url` | String | Yes | - | The page the browser opens first, `http` or `https` |
| `task` | String | Yes | - | What the tester does, in words |
| `expect` | String | Yes | - | What has to hold afterwards, in words |
| `secrets` | Object | No | - | Values the model never sees, by name. Write them as `{secret:NAME}` in `task`; see [Secrets](#secrets) |
| `allow_hosts` | List or string | No | the host of `url` | The hosts the tester may open pages of: `host`, `host:port`, or `*.domain`. A list, or a string apart by commas |
| `model` | String | No | `claude-opus-5-5` | The Claude model |
| `effort` | String | No | `medium` | `low`, `medium`, `high`, `xhigh` or `max` |
| `max_steps` | Integer | No | `40` | The calls the tester may make to the browser |
| `timeout` | Duration | No | `4m` | Limit for the whole test, the judge included, as `90s` or a number of seconds. Keep it below the step's `timeout`, which is `5m` unless set |
| `headless` | Boolean | No | `true` | Run Chrome without a window |
| `window_w`, `window_h` | Integer | No | `1280`, `800` | The viewport, in pixels |
| `evidence_dir` | String | No | the temporary directory | Where the screenshots and the report are written, each test in a directory of its own |

The model is called with the credentials the [Anthropic SDK](https://github.com/anthropics/anthropic-sdk-go) finds, such as `ANTHROPIC_API_KEY`, which the action takes from Probe's environment. List it under `secrets` of the workflow so that Probe hides it in its output.

## Result

| Field | Type | Description |
|---|---|---|
| `res.verdict` | String | `pass`, `fail`, or `blocked` when the test could not be judged |
| `res.reason` | String | Why, in the judge's words |
| `res.evidence` | List | What the judge quoted: `quote`, and `verified`, whether a page showed it |
| `res.stopped` | String | How the tester stopped: `done`, `blocked`, `max_steps`, `timeout`, `no_finish` or `refused` |
| `res.summary` | String | What the tester said it did and saw |
| `res.steps` | List | Each call to the browser: `index`, `tool`, `input`, `result` or `error`, `url`, `screenshot` and the tester's `notes` |
| `res.url`, `res.title` | String | The page the tester left the browser on |
| `res.console_errors` | List | The errors the pages wrote to their console, as the browser action reports them |
| `res.network_errors` | List | The requests that failed or were answered with 400 or more: `method`, `url`, `status`, `error` |
| `res.usage` | Object | The `requests` made to the model and the tokens they used |
| `res.filepaths` | Object | `report`, a Markdown report with a screenshot of each step; `trace`, the result as JSON; `screenshot`, the final page |
| `res.report` | String | A few lines Probe prints under the step |
| `status` | Integer | `0`; the verdict is for the step's `test` |

`blocked` is kept apart from `fail`: it is what the tester could not get past, such as a CAPTCHA or information the task does not give, rather than what the application did wrong.

## Secrets

The model reads `{secret:NAME}` where a secret is. When it types the placeholder, the browser is given the value; what the pages show the model, and what the action returns, have each value replaced with its placeholder again. A placeholder of a secret that was not given is an error the model is told of, not text it types.

## Guard

The action keeps to no guard: it cannot tell what a page will do, nor what the model will click. Under `--read-only` or `--allow-host`, a step that uses it is refused unless `--allow-action` names it. The tester is kept to `allow_hosts`: a page on another host is not opened, and the browser goes back from one a click led to.

## Development

```bash
go test ./...                      # the page tests start Chrome, and are skipped without it
go -C e2e/server run . &           # the sign-up the e2e workflow tests
go build -o e2e/action/probe-agent . && probe e2e/workflow.yml   # needs ANTHROPIC_API_KEY
```
