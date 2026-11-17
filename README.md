# probe-agent

A [Probe](https://github.com/mozership/probe) action that tests an application from a task written in natural language. A model, the tester, uses the application through a real Chrome, an app on an iOS simulator, or both, to carry out the task; another call of the model, the judge, then decides from what they showed whether the expectation holds. See [Targets](#targets) for an iOS app.

Chrome or Chromium has to be installed where Probe runs, and the model is reached with the API key of its provider. It is an [external action](https://probe.linyo.ws/guide/concepts/actions#external-actions): Probe downloads it the first time a workflow uses it, and runs the executable whose SHA-256 the `action.yml` at the pinned commit names. It drives Chrome through [probe-browser](https://github.com/mozership/probe-browser), and an iOS app through [probe-ios](https://github.com/mozership/probe-ios), which the workflow names under `actions` as it names the agent; it calls them as a step of the run would, which needs Probe v1.37.0 or later.

It has two modes:

- **Scenario**, with `task` and `expect`: the tester does the task, and the judge decides whether the expectation holds. For checks that should pass, as before a merge.
- **Persona**, with `persona` and `goal`: the tester plays the persona pursuing the goal, and notes what is wrong on the way; the reviewer decides whether the persona got there, and which of the notes, and of what the browser recorded, are findings. For looking for what nobody wrote a test of, as on a staging environment again and again.

It does not replace deterministic steps. Check what can be stated exactly, such as a status, a row or a mail, with them, and let them gate a merge. The agent action is for what they cannot state: whether a user gets through, or whether the application still holds up on staging once data and settings pile up. Run its persona tests with [`repeat`](https://probe.linyo.ws/reference/yaml-configuration#repeat) against staging, and they keep using the application as people do.

A workflow pins the action by a full commit SHA. The notes of each [release](https://github.com/mozership/probe-agent/releases) start with the action and its commit to copy. The examples here give it a name once, under [`actions`](https://probe.linyo.ws/reference/yaml-configuration#actions), which needs Probe v1.24.0 or later; with an earlier one, write the action in full in each `uses`. A step reads an environment variable, such as a password, only through [`vars`](https://probe.linyo.ws/reference/yaml-configuration#vars).

```yaml
name: Sign-up
actions:
  agent: github.com/mozership/probe-agent@<commit SHA>
  browser: github.com/mozership/probe-browser@<commit SHA>
secrets:
  - ANTHROPIC_API_KEY
  - TEST_PASSWORD
vars:
  test_password: "{{TEST_PASSWORD}}"
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
        PASSWORD: "{{vars.test_password}}"
    timeout: 5m
    test: res.verdict == "pass"
```

A persona test:

```yaml
  - name: A first-time visitor writes a first note
    uses: agent
    with:
      url: https://staging.example.com/
      persona: |
        Carol, who has never used a notes app and is not technical. She signs
        up with {secret:EMAIL} and the password {secret:PASSWORD}.
      goal: Create an account, and write a first note that says "Buy milk".
      secrets:
        EMAIL: "qa+{{random_str(8)}}@example.com"
        PASSWORD: "{{vars.test_password}}"
    timeout: 5m
    test: len(filter(res.findings, .severity == "high" && .verified)) == 0
```

A test in natural language is not deterministic: the same step can take another path, or come to another verdict, from one run to the next. Check what matters with a deterministic step after it where you can, such as an `http` or `db` step that the account exists, and count the passes over runs with [`repeat`](https://probe.linyo.ws/reference/yaml-configuration#repeat) rather than trusting one run.

## How it works

1. Chrome opens `url`.
2. The tester is given `task` and `expect`, and the [browser toolset](https://platform.claude.com/docs/en/agents-and-tools/tool-use/browser-use-tool) of the Claude API: it reads the page as a tree of elements with refs, clicks, types, fills in forms and takes screenshots, until it calls `finish`.
3. The judge is given the task, the expectation, the steps, the texts the pages showed, the final page and its screenshot, and the console errors and failed requests. It gives `pass` or `fail`, quoting the pages.
4. Each quote is looked for in what the pages showed. A `pass` that quotes nothing the pages showed is not taken, and the verdict is `blocked`.

The tester is told to describe what it saw, not to judge, so that the verdict comes from a call that did not do the work.

In a persona test, the tester also has `note_finding`, which it calls as soon as it comes across something wrong. The reviewer, in place of the judge, is given the notes besides the record, and the slowest requests. It keeps a note only where the record bears it out, drops those that come from the tester's own slips, and adds what the record shows that the tester did not note, such as an error of the server. Each finding quotes the record, and is `verified` when a quote is found in it.

## Parameters

| Parameter | Type | Required | Default | Description |
|---|---|---|---|---|
| `targets` | Object | No | the browser of `url` | What the test drives: a browser, an iOS app, or both; see [Targets](#targets) |
| `url` | String | Without `targets` | - | The page the browser opens first, `http` or `https`. The short form of `targets.browser.url` |
| `task` | String | Scenario | - | What the tester does, in natural language |
| `expect` | String | Scenario | - | What has to hold afterwards, in natural language |
| `persona` | String | Persona | - | Who the tester plays, in natural language: what they know, and how they go about things |
| `goal` | String | Persona | - | What the persona sets out to do, in natural language |
| `secrets` | Object | No | - | Values the model never sees, by name. Write them as `{secret:NAME}` in `task` or `persona`; see [Secrets](#secrets) |
| `storage_state` | String | No | - | A file of cookies and localStorage the browser starts with, as the browser action's `save_storage_state` writes; see [Starting signed in](#starting-signed-in) |
| `allow_hosts` | List or string | No | the host of `url` | The hosts the tester may open pages of: `host`, `host:port`, or `*.domain`. A list, or a string apart by commas. The short form of `targets.browser.allow_hosts` |
| `provider` | String | No | `anthropic` | `anthropic`, `openai`, `xai`, `gemini` or `sakura`; see [Models](#models) |
| `model` | String | Not with `anthropic` | `claude-opus-5-5` with `anthropic` | The model's ID |
| `effort` | String | No | `medium` | `low`, `medium`, `high`, `xhigh` or `max` |
| `base_url` | String | No | the provider's | The URL the provider's API is under, as for a proxy, or another API compatible with OpenAI's Responses API under `openai` |
| `vision` | Boolean | No | `false` with `sakura`, else `true` | Whether the model sees images. Not with `anthropic` |
| `max_steps` | Integer | No | `40` | The calls the tester may make to the targets |
| `timeout` | Duration | No | `4m` | Limit for the whole test, the judge included, as `90s` or a number of seconds. Keep it below the step's `timeout`, which is `5m` unless set |
| `headless` | Boolean | No | `true` | Run Chrome without a window. The short form of `targets.browser.headless` |
| `window_w`, `window_h` | Integer | No | `1280`, `800` | The viewport, in pixels. The short form of `targets.browser.window_w` and `window_h` |
| `cache` | String | No | - | A file to keep the steps of a scenario test that passed in, and to replay them from; see [Replay](#replay) |
| `cache_mode` | String | No | `auto` | `auto`, `read`, `strict` or `refresh`; see [Replay](#replay) |
| `evidence_dir` | String | No | the temporary directory | Where the screenshots and the report are written, each test in a directory of its own |

The model is called with the API key of its provider, which the action takes from Probe's environment. List it under `secrets` of the workflow so that Probe hides it in its output.

## Targets

`targets` names what the test drives, by kind, with what the workflow fixes for each. A test drives one target or more:

| Key | Target | What goes in it | Driven by |
|---|---|---|---|
| `browser` | Chrome | `url` (required), `allow_hosts`, `storage_state`, `headless`, `window_w`, `window_h`, as in [Parameters](#parameters) | [probe-browser](https://github.com/mozership/probe-browser) |
| `ios` | An app on an iOS simulator | `app` (required), the bundle identifier; `device`, the simulator by its name or UDID, which has to be given when more than one is booted; `timeout`, see below | [probe-ios](https://github.com/mozership/probe-ios) |

Without `targets`, `url`, `allow_hosts`, `storage_state`, `headless`, `window_w` and `window_h` at the top of `with` are the short form of `targets.browser`, as before there were targets; the action warns that they move under it. A step with both `targets` and one of them is refused.

### A browser

The agent drives Chrome through probe-browser, which it calls as a step of the run would, under the guard of the run, one call for each tool the tester uses, keeping the browser open from one to the next. Name probe-browser under `actions` of the workflow, by any name; the agent finds it by its reference, and the workflow pins its version as it pins every action. It needs probe-browser v0.10.0 or later, and Probe v1.37.0 or later.

```yaml
actions:
  agent: github.com/mozership/probe-agent@<commit SHA>
  browser: github.com/mozership/probe-browser@<commit SHA>
```

A workflow of an earlier agent, which drove Chrome itself, names probe-browser too: without it, a test of a browser fails at once, and says so. Probe stops the browser once the run is over. A link that would open a window opens its page in the tab the tester drives, as probe-browser's `one_tab` has it, and the requests and the console errors of the page are those probe-browser reads while each tool runs, and from one to the next.

### An iOS app

The agent drives the app through probe-ios, which it calls as a step of the run would, under the guard of the run. Name probe-ios under `actions` of the workflow, by any name; the agent finds it by its reference, and the workflow pins its version as it pins every action. It needs Probe v1.37.0 or later, and macOS with what [probe-ios needs](https://github.com/mozership/probe-ios#requirements): Xcode, a booted simulator, and the app installed on it, which an earlier step sees to.

```yaml
actions:
  agent: github.com/mozership/probe-agent@<commit SHA>
  iphone: github.com/mozership/probe-ios@<commit SHA>
jobs:
- name: settings
  steps:
  - name: Boot the simulator
    uses: shell
    with:
      cmd: xcrun simctl boot "iPhone 17" || true
  - name: A user turns on dark mode
    uses: agent
    timeout: 15m
    with:
      task: Turn on dark mode
      expect: The appearance is dark
      targets:
        ios:
          app: com.apple.Preferences
          device: iPhone 17
    test: res.verdict == "pass"
```

probe-ios builds its runner the first time, which takes minutes: the agent opens the app before the model starts, within the step, so give the step `timeout: 15m`, beside a `with.timeout` that leaves the test the time it needs.

The tester reads the app by its accessibility tree, and names an element by a [selector](https://github.com/mozership/probe-ios#selectors) of it:

| Tool | What it does |
|---|---|
| `ios_tree` | Reads the accessibility tree |
| `ios_tap`, `ios_type` | Taps an element, or taps a field and types into it, and returns the tree after it |
| `ios_wait`, `ios_text` | Waits for an element, or reads its label or value |
| `ios_launch`, `ios_activate`, `ios_terminate` | Launches the app afresh, brings it to the foreground, or ends it |
| `ios_screenshot` | Sees the screen, for a model that sees images |

Each tool is one call of probe-ios. `targets.ios.timeout`, as `5s` or a number of seconds, bounds the actions of each call, from the first to the last: a tap on an element that is not there fails once it runs out, and the tester reads the tree again. It is 30 seconds by default, as in probe-ios, which a guess of the model at a selector can spend whole; a few seconds are enough for an app that answers at once. It does not count building the runner, which the step's `timeout` bounds. The agent opens the app with the same timeout, and XCUITest waits for the app to settle before it: on a simulator just booted, or on a slow machine such as a CI runner, that takes longer than a few seconds, so keep the default there, or give more. The first call on a simulator just booted can run out of even the default, so a step of probe-ios that launches the app before the agent helps; on a GitHub Actions runner the opening ran out of the default now and then even after it, so [e2e/ios.yml](e2e/ios.yml) does both and gives `2m`.

The judge checks its quotes against the trees and the texts the app showed, and is given the last tree, and its screenshot when the model sees images. With images, each move keeps a screenshot as evidence, which `report.md` shows. A `cache` is refused: a recording is of the steps of a browser.

### A browser and an app

```yaml
  - name: A code shown on the web signs in the app
    uses: agent
    timeout: 15m
    with:
      task: Get a sign-in code on the web account page, and sign in to the app with it
      expect: The app shows the account name
      targets:
        browser:
          url: https://staging.example.com/account
        ios:
          app: com.example.app
```

The tester is offered the tools of both, and acts on one at a time; each step says which target it used. What passes from one to the other is what the model reads on one and enters on the other: Chrome on the Mac and Safari on the simulator are two browsers, so a link on the web does not open the app.

## Models

The agent is built for Claude, which is trained to use a browser through the browser toolset of the Claude API. The other providers are reached through their APIs compatible with OpenAI's Chat Completions, or through OpenAI's Responses API for `openai`, by which its models both reason and call tools; their models are offered the same members of the toolset as tools of the agent's own, so that they run the same tests; how well they carry out a task depends on the model.

| `provider` | API key | Notes |
|---|---|---|
| `anthropic` | `ANTHROPIC_API_KEY`, or what else the [Anthropic SDK](https://github.com/anthropics/anthropic-sdk-go) finds, or the OIDC token of a job of GitHub Actions, as below | The default |
| `openai` | `OPENAI_API_KEY` | The Responses API, keeping nothing on OpenAI's side. `max` is sent as `xhigh` |
| `xai` | `XAI_API_KEY` | `max` is sent as `xhigh` |
| `gemini` | `GEMINI_API_KEY` | `xhigh` and `max` are sent as `high` |
| `sakura` | `SAKURA_AI_ENGINE_TOKEN` | The Sakura AI Engine. Its models differ in what they take, so `effort` is not sent, and they see no images unless `vision` is `true`. Choose one that calls tools, such as `gpt-oss-120b` |

In GitHub Actions, the Claude API can be called without an API key, with the OIDC token of the job, through workload identity federation. Give the job `id-token: write`, and set `ANTHROPIC_FEDERATION_RULE_ID` and `ANTHROPIC_ORGANIZATION_ID` of the federation rule, with `ANTHROPIC_SERVICE_ACCOUNT_ID` and `ANTHROPIC_WORKSPACE_ID` when the rule wants them, as the e2e jobs in [.github/workflows/test.yml](.github/workflows/test.yml) do. The agent asks GitHub for a token for the audience `https://api.anthropic.com` each time it exchanges one for an access token, as the Claude API takes a token once alone, refusing it again as `jti_reused`, and each step of a workflow starts the agent anew. The rule matches the `sub` of the token, which starts with `repo:<owner>/<repo>:`, or with the IDs of both, as `repo:<owner>@<ID>/<repo>@<ID>:`, where the repository uses immutable subjects; a pattern of it ends in `:*` to take both pull requests and pushes. An API key, or an identity token the SDK reads itself from `ANTHROPIC_IDENTITY_TOKEN_FILE`, takes precedence over this.

A model that sees no images reads the pages by their elements and text alone: it is not offered `screenshot` or `zoom`, acts on elements rather than points, and the judge is not shown the final page as an image. The screenshots are still kept as evidence.

```yaml
secrets:
  - OPENAI_API_KEY
jobs:
- name: sign-up
  steps:
  - name: A new user signs up
    uses: agent
    with:
      provider: openai
      model: gpt-5.4
      url: https://staging.example.com/
      task: Sign up with {secret:EMAIL} and the password {secret:PASSWORD}.
      expect: The dashboard greets the user.
```

A recording is of the steps, not of the model, so a test recorded with one model is replayed with another.

## Result

| Field | Type | Description |
|---|---|---|
| `res.mode` | String | `scenario` or `persona` |
| `res.verdict` | String | `pass`, `fail`, or `blocked` when the test could not be judged. In a persona test, whether the persona reached the goal |
| `res.findings` | List | In a persona test, what is wrong with the application, the most severe first: `title`, `severity` (`high`, `medium` or `low`), `category` (`error`, `functional`, `usability`, `performance`, `content` or `accessibility`), `expected`, `observed`, `evidence`, `steps`, `url`, `screenshot`, and `verified`, whether any of the evidence was found in the record |
| `res.notes` | List | The [notes](https://probe.linyo.ws/reference/yaml-configuration#notes) the test leaves for people, which later steps read as `notes`: one for each finding of a persona test, and one for a scenario test that failed or was blocked. See [Notes](#notes) |
| `res.reason` | String | Why, in the judge's words |
| `res.evidence` | List | What the judge quoted: `quote`, and `verified`, whether a page showed it |
| `res.stopped` | String | How the tester stopped: `done`, `blocked`, `max_steps`, `timeout`, `no_finish`, `refused`, or `stale` for a recording that no longer matched under `cache_mode: strict` |
| `res.summary` | String | What the tester said it did and saw |
| `res.steps` | List | Each call to a target: `index`, `tool`, `target` (`browser` or `ios`), `input`, `result` or `error`, `url`, `screenshot` and the tester's `notes` |
| `res.targets` | List | The last state of each target: `kind`; `url` and `title` of the browser; `app` and `device` of the app; and `screenshot` |
| `res.url`, `res.title` | String | The page the tester left the browser on, empty without a browser |
| `res.console_errors` | List | The errors the pages wrote to their console, as the browser action reports them |
| `res.network_errors` | List | The requests that failed or were answered with 400 or more: `method`, `url`, `status`, `error` |
| `res.cache` | Object | With `cache`, how the test used it: `path`, `mode`, `status` (`miss`, `hit` or `partial`), `replayed`, the steps replayed, `diverged_at` and `reason`, where and why the recording stopped, `stale`, whether it stopped because the application no longer matches it, and `saved`, whether the run was recorded |
| `res.metrics` | Object | Measures that do not depend on the model, to compare runs by: `steps`, `duration_ms`, `requests`, `failed_requests`, `console_errors`, and `slowest_requests`, the five requests that took longest, with `method`, `url`, `status` and `duration_ms` |
| `res.usage` | Object | The `requests` made to the model and the tokens they used. `cache_write_tokens` is counted by `anthropic` alone |
| `res.model` | String | The model's ID |
| `res.filepaths` | Object | `report`, a Markdown report with a screenshot of each step; `trace`, the result as JSON; `screenshot`, the final page |
| `res.report` | String | A few lines Probe prints under the step |
| `status` | Integer | `0`; the verdict is for the step's `test` |

`blocked` is kept apart from `fail`: it is what the tester could not get past, such as a CAPTCHA or information the task does not give, rather than what the application did wrong.

## Notes

The test leaves what people should know of it in `res.notes`, and later steps read it as the [`notes`](https://probe.linyo.ws/reference/yaml-configuration#notes) of the run, to post where people work. This needs Probe v1.32.0 or later. Probe itself posts the notes nowhere. Actions such as [probe-github-notes](https://github.com/mozership/probe-github-notes) and [probe-slack](https://github.com/mozership/probe-slack) post them.

| Test | Notes |
|---|---|
| Persona | One for each finding, at the level of its severity: `critical` for `high`, `warning` for `medium` and `info` for `low` |
| Scenario that failed | One `critical` note, `The expectation did not hold`, with the reason of the judge |
| Scenario that was blocked | One `warning` note, `The test could not be carried out`, with why |
| Scenario that passed | None |

The title of a note of a finding is that of the finding. Its body gives:

- what was expected and observed
- the category, the steps and the URL
- the quotes found in the record

A finding none of whose evidence was found in the record is still a note. Its body says so, as the finding may be a misreading of the model. The quotes not found are left out of a note.

A last job reads the notes of the others, and puts them on the pull request. Added to the sign-up workflow above, with the job given an `id` to name in `needs`:

```yaml
actions:
  agent: github.com/mozership/probe-agent@<commit SHA>
  github-notes: github.com/mozership/probe-github-notes@80357adece0113f73e9c8ad31b12350bd885ddec # v0.1.1
secrets:
  - ANTHROPIC_API_KEY
  - TEST_PASSWORD
  - GITHUB_TOKEN
vars:
  test_password: "{{TEST_PASSWORD}}"
  github_token: "{{GITHUB_TOKEN}}"
jobs:
- name: sign-up
  id: sign-up
  steps:
  # The steps of the sign-up, as above.

- name: Notes
  needs: [sign-up]
  always: true
  steps:
  - name: Put the notes on the pull request
    uses: github-notes
    with:
      to: comment
      token: "{{vars.github_token}}"
      notes: "{{ notes }}"
```

A note of a finding:

```markdown
**Expected:** A form to write the note in.

**Observed:** The page says "Something went wrong" after New note is clicked.

error · step 9 · https://staging.example.com/notes/new

> Something went wrong
```

## Replay

A scenario test that passes the same way every run need not ask the model how to do it every run. With `cache`, the steps of a run that passed are kept in a file, and the next run replays them without the model; only the judge is asked, so that what the pages show is still judged.

```yaml
    with:
      url: https://staging.example.com/
      task: Sign up with {secret:EMAIL} and the password {secret:PASSWORD}.
      expect: The dashboard greets the new user.
      cache: .probe-agent/signup.json
      cache_mode: "{{CACHE_MODE ?? 'auto'}}"
```

A replay:

1. Begins only on the page the recording began on, as the start URL may redirect elsewhere, such as to a login.
2. Finds each element a step acted on by its role and its name, such as `button "Create account"`. Where other elements of the page had both, it counts them too, and takes the same one of as many; another number of them is ambiguous, and the replay stops rather than guess. An element with no name among others of its role is not recorded: only its order would tell it, and rows that reorder would give the step to another.
3. Checks after each step that the page is on the route it reached when recorded, and shows no alert the recording did not. A route is a page's path and query, with what looks minted per record, such as an ID, a token or a date, read as one: `/orders/42` and `/orders/7` are one route, and `/companies` and `/companies?tab=notes` are two.
4. Ends without the model, or hands the test to the tester where it stopped, telling it what was replayed and why it stopped. Once the test passes, the run is recorded again.

A value the tester typed that the task spells out, or that it made up, such as a name, is typed again. A value it may have read off the screen, such as a code the application showed, or a date, belongs to that run: its step is recorded as a gap, without the value, and the replay hands the test to the tester there. A step at a point of the viewport, rather than on an element, is replayed only at the viewport it was recorded at.

The recording is of the task, the expectation and the start URL; when one of them changes, it is not replayed. It keeps the secrets as placeholders, and the URLs on the host of `url` as paths, so that a recording made against one host is replayed against another. It is written only when it changes, so that a committed one stays clean while it replays. A persona test takes no `cache`: it is to take another way each run.

| `cache_mode` | Replays | Records | Where the recording stops matching |
|---|---|---|---|
| `auto` | yes | a run that passed, if it changes the recording | the tester carries on |
| `read` | yes | never | the tester carries on |
| `strict` | yes | never | the test is blocked without the model, as `stopped: stale` |
| `refresh` | no | a run that passed | |

Commit the file, and run CI with `strict`, so that a recording that no longer matches fails the test rather than paying the tester on every run until someone records it again. Record it again with `auto` where someone looks at the change. A gap is not a change of the application: `strict` hands it to the tester too.

## Starting signed in

A test of what a signed-in user does need not have the tester sign in first. Sign in with a deterministic step of the [browser action](https://github.com/mozership/probe-browser), save the login with its `save_storage_state`, and name the file in `storage_state`: the agent's browser starts with its cookies, `HttpOnly` ones included, and its localStorage, and the tester begins at `url` signed in.

```yaml
actions:
  browser: github.com/mozership/probe-browser@<commit SHA>
  agent: github.com/mozership/probe-agent@<commit SHA>
secrets:
  - ANTHROPIC_API_KEY
  - TEST_EMAIL
  - TEST_PASSWORD
vars:
  email: "{{TEST_EMAIL}}"
  password: "{{TEST_PASSWORD}}"
jobs:
- name: orders
  steps:
  - name: Sign in
    id: login
    uses: browser
    with:
      actions:
        - name: navigate
          url: https://staging.example.com/login
        - name: type
          selector: "#email"
          value: "{{vars.email}}"
        - name: type
          selector: "#password"
          value: "{{vars.password}}"
        - name: click
          selector: "button[type='submit']"
        - name: wait_visible
          selector: "#dashboard"
        - name: save_storage_state
          id: state
    test: res.code == 0
    outputs:
      state: res.filepaths.state

  - name: A signed-in user cancels an order
    uses: agent
    with:
      url: https://staging.example.com/orders
      storage_state: "{{outputs.login.state}}"
      task: Cancel the most recent order.
      expect: The order shows as cancelled, and the list no longer offers to cancel it.
    timeout: 5m
    test: res.verdict == "pass"
```

The file has the shape of [Playwright's storage state](https://playwright.dev/docs/auth), so one Playwright saves serves too. Before the first page, the browser takes the cookies, and opens a page of each origin with items in it to write the localStorage there. Those pages are answered with an empty one inside the browser, so they reach no server, run none of the application's scripts, are not counted in `res.metrics`, and need not be in `allow_hosts`. A file that is missing, or is not a storage state with a list of `cookies` and one of `origins`, empty or not, fails the step before the browser starts, as when the step that saves it failed, or when the file `save_passkeys` writes is given in its place.

The model is not given the file: it sees only what the pages show. The cookies sign in as the user, so keep the file out of version control, and out of the files a CI run uploads. A login expires: save it in the same run, rather than committing one.

## Secrets

The model reads `{secret:NAME}` where a secret is. When it types the placeholder, the browser is given the value; what the pages show the model, and what the action returns, have each value replaced with its placeholder again, in any case, as an application can show an email address in lower case. A placeholder of a secret that was not given is an error the model is told of, not text it types.

## Running in CI

- Install Chrome or Chromium on the runner, and keep `headless: true`.
- Set the API key from a secret of the CI, and list it under `secrets` of the workflow.
- Upload `evidence_dir` as an artifact, so that the report and the screenshots of a failed test can be read.
- Run a test with a `cache` under `cache_mode: strict`, so that the model is asked only to judge while the recording matches.

## Guard

The action keeps to no [guard](https://probe.linyo.ws/guide/concepts/guard): it cannot tell what a page will do, nor what the model will click. Under `--read-only` or `--allow-host`, a step that uses it is refused unless `--allow-action` names it. The tester is kept to `allow_hosts`: a page on another host is not opened, and the browser goes back from one a click led to.

probe-ios keeps to no guard either, and the agent calls it under the guard of the run: under `--read-only` or `--allow-host`, `--allow-action` has to name both. When it names the agent alone, the call of probe-ios is refused, and so is the step.

## Development

```bash
go test ./...                      # the page tests build probe-browser and drive Chrome through it, skipped without Chrome
go -C e2e/server run . &           # the sign-up the e2e workflow tests
go build -o e2e/action/probe-agent . && probe e2e/workflow.yml   # needs ANTHROPIC_API_KEY
```
