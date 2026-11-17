# Running OSWorld

`hcu.osworld` runs OSWorld (V1) or OSWorld-V2 (V2) tasks. Each task gets a
fresh official Docker/QEMU guest, and Codex, Claude Code, Gemini CLI or Grok
Build runs inside it as the task agent. The official evaluator scores the
guest afterwards. Both versions use an OSWorld-V2 checkout on the host; the
guest image decides which desktop the task sees.

One task run:

1. Starts `happysixd/osworld-docker` on a disposable overlay of the guest
   image.
2. Removes inherited agent configuration from the guest and uploads the agent
   CLI, `computer-use`, the agent configuration and the instructions (see
   [Isolation and instructions](#isolation-and-instructions)).
3. Runs the task setup, then checks the gateway, the executables and the MCP
   tool list.
4. Runs the agent until it finishes, reports the task infeasible, or hits the
   model-call budget or the timeout.
5. Runs the evaluator, writes `result.json` and removes the container.

## Isolation and instructions

Each task uses a fresh guest, agent session, MCP process and result directory.
Before starting the agent, the runner removes inherited harness state
(`.codex`, `.claude`, `.claude.json`, `.gemini`, `.grok`, `AGENTS.md`,
`CLAUDE.md`, `GEMINI.md` and `.mcp.json`) and injects only:

- `instructions/base.md` (common safety and outcome policy);
- one selected interaction guide (`instructions/hybrid.md` by default, or
  `ax-only.md` / `gui-only.md` for channel ablations; `--code-mode` appends
  `instructions/hybrid-code.md`; `--instructions-file ""` omits the guide while
  keeping `base.md`);
- the `TASK_COMPLETE`, `TASK_INFEASIBLE` and `ASK_USER` runner protocol.

| Agent | Gateway | Default model | Default effort | Base delivery | Profile delivery |
| --- | --- | --- | --- | --- | --- |
| `codex` | OpenAI Responses | `gpt-5.5` | `xhigh` | `model_instructions_file` | guest `AGENTS.md` (standalone: `developer_instructions`) |
| `claude-code` | Anthropic | `claude-opus-5` | `xhigh` | `--system-prompt-file` | appended to system prompt |
| `gemini-cli` | Gemini | `gemini-3.8-flash` | `high` | `GEMINI_SYSTEM_MD` | global `GEMINI.md` |
| `grok-build` | OpenAI Responses | `grok-4.7` | `xhigh` | `--system-prompt-override` | appended to override |

Model credentials remain in the host-side gateway; the guest receives only a
placeholder token and a task-scoped endpoint. When Codex uses its browser
channel over CDP (`127.0.0.1:9222`), the V2 overlay retries `connect_over_cdp`
with bounded backoff rather than launching a second browser instance.

## Install

Requirements: Linux x86-64 with `/dev/kvm`, Docker that can run privileged
containers, the `happysixd/osworld-docker` image, `qemu-img`, Python 3.10 or
later, and about 4 GiB of RAM per concurrent task.

From the repository root:

```bash
git clone https://github.com/xlang-ai/OSWorld-V2 external/OSWorld-V2
git -C external/OSWorld-V2 checkout d578d2d4e0dc82b43e270fdaa7fa89d9708cd154
python3 -m venv external/OSWorld-V2/.venv
external/OSWorld-V2/.venv/bin/pip install -e external/OSWorld-V2
python3 scripts/install-osworld-v2.py external/OSWorld-V2
make build-local          # writes artifacts/computer-use
```

Put the agent CLI in `artifacts/` (`codex`, `claude-code`, `grok`, or
`gemini-cli.tgz` plus `node`). Reinstall the overlay whenever you replace the
checkout.

Run every command below from the repository root with
`external/OSWorld-V2/.venv/bin/python` (written `$PY`). The runner finds the
checkout from that interpreter. Set `OSWORLD_V2_ROOT` only if you use a
different interpreter. The runner uses `DOCKER_HOST` when you set it and the
default Docker socket otherwise.

Guest images:

| | V1 | V2 |
| --- | --- | --- |
| Image | official `Ubuntu.qcow2` (`xlangai/ubuntu_osworld` on Hugging Face) | the OSWorld-V2 image (see its README) |
| `client_password` | `password` | `osworld-public-evaluation` (default) |
| `volume_size` | 64 | 100 |

`volume_size` must not exceed the image's virtual size; the runner grows the
guest filesystem into it but never enlarges the image. V2 task classes are
gated: download them with the checkout's
`scripts/tools/download_osworld_v2_tasks.py` before running V2 tasks.

## Setup assets

Task setup copies input files into the guest. Setup uses a local path as is.
For an HTTP(S) source, setup looks in the asset cache (default `assets/` in
this repository) at `<cache>/<host>/<url path>` and downloads the file into
the cache the first time it is missing. You need not prepare anything per
task.

To forbid downloads during a run, fill the cache first (for example with one
canary per task) and pass `--offline-assets` (`"offline_assets": true` in a
config). A missing asset then fails setup instead of fetching it. The browser
and applications inside the guest still reach the network as the task
requires.

## Model gateway

Agents never receive model credentials. They call a gateway on the host at
`<gateway>/t/<task-id>/...`. The gateway adds the credential, appends one JSONL
row per request to its token log and enforces the per-task budget of
successful responses. Bind it to the Docker bridge address that the guests
reach (`docker network inspect bridge`, usually `172.17.0.1`), not to
loopback, and firewall it.

```bash
# Codex or Grok Build (OpenAI Responses), Azure OpenAI with `az login`:
python3 -m hcu.gateway.openai_proxy --host 172.17.0.1 --ports 8611 \
  --upstreams <resource>.openai.azure.com,<resource>.openai.azure.com@<deployment>,<resource2>.openai.azure.com \
  --out runs/<run_id>/gateway-tokens.jsonl --max-requests-per-task 100

# Same, behind a local relay that picks the deployment within a resource:
python3 -m hcu.gateway.openai_proxy --host 172.17.0.1 --ports 8611 \
  --upstreams 127.0.0.1:8541 --upstream-kind openai --upstream-scheme http \
  --upstream-token unused --pin-resources <resource>.openai.azure.com,<resource2>.openai.azure.com \
  --out runs/<run_id>/gateway-tokens.jsonl --max-requests-per-task 100

# Claude Code:
OSW_ANTHROPIC_UPSTREAM_BASE=https://<host> OSW_ANTHROPIC_UPSTREAM_TOKEN=<secret> \
python3 -m hcu.gateway.anthropic_proxy --host 172.17.0.1 --port 8537 \
  --out runs/<run_id>/gateway-tokens.jsonl --max-requests-per-task 100

# Gemini CLI:
OSW_GEMINI_UPSTREAM_TOKEN=<secret> \
python3 -m hcu.gateway.gemini_proxy --host 172.17.0.1 --port 8571 \
  --out runs/<run_id>/gateway-tokens.jsonl --max-requests-per-task 100
```

`run_queue CONFIG --dry-run` prints the exact `--out` and
`--max-requests-per-task` values for a config. The runner checks the
gateway's budget through `/healthz` before the agent starts and stops if the
two differ. `--upstream-kind openai --upstream-token <secret>` serves any
OpenAI-compatible Responses endpoint without Azure. Use a fresh token log per
run: the gateway restores its counters from the log on restart.

The budget counts complete successful generations only. Failed requests,
disconnections and token-count calls are free. The last permitted response
may finish its tools; the next request is rejected and the task ends as
`max_model_calls`. Five consecutive failed generations (rate limits do not
count) end the task as an error.

### Rate limits

Azure reports token-rate limits as HTTP 429 or as an HTTP 200 stream that ends
in an error event. The gateway and the runner handle both before the agent
sees them:

1. The gateway waits (the upstream's `retry-after` or `x-ratelimit-reset-*`
   hint, otherwise exponential backoff with jitter up to 60 s) and resends.
   It waits at most `--rate-limit-max-wait` seconds per request (default 300).
2. A task never changes Azure resource: agents replay encrypted reasoning
   that only the producing resource can decrypt (another resource answers
   400 `invalid_encrypted_content`). Within that resource, a request without
   `previous_response_id` may go at once to another deployment of the same
   model outside its cooldown, and the task stays there. A request that
   continues server-side state never moves. With `--pin-resources`, each task
   gets one resource round-robin, every retry sends it as `X-HCU-Resource`,
   and the relay picks the deployment. New tasks start on the upstream with
   the most `x-ratelimit-remaining-tokens`.
3. The gateway logs each wait. The runner adds the waits to the task's timeout and
   no-progress limit, so waiting never truncates a task. Waits never use the
   budget.
4. If a turn still ends on a rate limit, the runner resumes the same agent
   session after 60 s, at most twice. If the task still fails, `result.json`
   records `failure_classification: upstream_rate_limit`. The queue then moves
   the attempt to `retries/`, pauses new launches for 5 minutes and runs the
   task again, at most three times. A rate-limited task is never scored as an
   agent failure.

A single GPT-5.4 xhigh task can use more tokens per minute than one 47k-TPM
deployment allows. Give each resource two or more deployments in
`--upstreams` (`host@deployment` selects a deployment), spread tasks over
several resources, and size `max_active_tasks` to the total quota.

## Setup canary

A canary starts the guest and runs the task setup without an agent or model.
It also fills the asset cache. Run one before spending model quota:

```bash
$PY -m hcu.osworld.setup_canary --benchmark-version v1 \
  --task-id 1d17d234-e39d-4ed7-b46f-4417922a4e7c --task-domain libreoffice_calc \
  --guest-image /path/to/osworld-v1-64g.qcow2 --volume-size 64 \
  --client-password password --result-dir runs/canary-calc --run-evaluator
```

`report.json` in the result directory shows `"status": "passed"` for a
healthy guest. `--run-evaluator` also scores the untouched setup state, which
is usually 0.

Before starting an agent, `run_task` also probes the MCP transport from the
rendered client configuration, including its command, arguments and environment.
It checks the negotiated protocol and tool list, then calls AX apps, observe
and find for enabled accessibility channels, and GUI screenshot for GUI.
The probe has a 90-second limit and uses no model calls. A tool error or
missing required response evidence stops preparation.

An empty desktop can pass discovery without exercising observe or find;
the report records those checks as skipped. Before evaluating a changed
build, also run these reads against a known open fixture window and require
every check to pass. A setup canary alone does not verify the MCP response contract.

## Run one task

```bash
$PY -m hcu.osworld.run_task --benchmark-version v1 \
  --task-id 1d17d234-e39d-4ed7-b46f-4417922a4e7c --task-domain libreoffice_calc \
  --guest-image /path/to/osworld-v1-64g.qcow2 --volume-size 64 \
  --client-password password --agent codex --model gpt-5.4 --effort xhigh \
  --max-model-calls 100 --token-log runs/single/gateway-tokens.jsonl
```

The gateway defaults to the Docker bridge address at the agent's port;
`--gateway URL` overrides it. Results go to `runs/single/<run-tag>/`, or to
`--result-dir`. `--channels a11y,keyboard` or `gui,keyboard` exposes
one channel only; `--instructions-file ""` runs without a profile.

`--ablate` removes HCU mechanisms from the computer-use server: a
comma-separated subset of `batching`, `wait`, `diff` and `guard`. With
`diff` removed, an `act` returns the whole current tree of its window in
place of the change report and diff, and `observe` always repeats the tree.
`effect_unknown` in the act's evidence and the message of a settle wait
step are kept: the first is execution evidence and the second belongs to
`wait`.
The runner
sets `COMPUTER_USE_ABLATE` in the MCP server environment of every harness and
gives the agent copies of `instructions/base.md` and `instructions/hybrid.md`
without the passages that describe the removed mechanisms. For each of
`batching`, `wait` and `diff` the guide instead gets one sentence that
states how the ablated `act` behaves, as the AX tool description does, so
the agent does not learn the change from rejected calls. It writes the
copies to `ablated-instructions/` in the task's result directory. Other
guides, `--instructions-file ""` and `--code-mode` are refused with
`--ablate`, because they can still describe a removed mechanism. With `guard`
removed, the server logs the guard decisions in the guest, and the runner
copies the log to `guard.jsonl` in the result directory. Without `--ablate`
the harness configurations do not change. The preflight probe also checks
that the server applies the ablation: it sends one call per removed
mechanism and requires each to be refused, and it requires the server to
refuse an unknown `COMPUTER_USE_ABLATE` value. A build without the switches
therefore fails preparation instead of running full HCU under an ablation
label. `meta.json` records the SHA-256 of the MCP binary.

## Run a panel

A run is one JSON config. Relative paths in it are relative to the config
file; omitted paths default to the repository's `artifacts/`, `runs/`,
`assets/` and `instructions/`. See
[`configs/osworld-v1-codex-queue.example.json`](../configs/osworld-v1-codex-queue.example.json)
for the 20-task V1 panel. [`configs/ablation/`](../configs/ablation/) holds
the same panel for each mechanism-ablation group. Groups that share one
gateway must share its `--out` file: set the same `token_log` in each config.
A task fails before its agent starts when the gateway writes another file.

| Key | Meaning |
| --- | --- |
| `run_id` | Run directory name; defaults to the config file name |
| `benchmark_version`, `agent`, `model`, `effort` | What runs |
| `tasks` | Task ids, or objects with `id` and optional `task_domain`, `volume_size`, `ram_size`, `stratum` |
| `guest_image`, `client_password`, `volume_size`, `ram_size`, `task_domain` | Guest defaults |
| `max_model_calls`, `timeout_seconds` | Per-task budget and per-phase timeout (0 = none) |
| `max_active_tasks`, `launch_stagger_seconds` | Concurrency |
| `gateway`, `token_log`, `results_dir`, `artifact_dir`, `asset_cache`, `offline_assets` | Optional overrides |
| `agent_binary`, `mcp_binary`, `instructions_file`, `base_instructions_file`, `channels` | Optional agent overrides |
| `ablate` | Removed HCU mechanisms, as a list or a comma-separated string (see `--ablate`) |

```bash
$PY -m hcu.osworld.run_queue configs/osworld-v1-codex-queue.example.json --dry-run
$PY -m hcu.osworld.run_queue configs/osworld-v1-codex-queue.example.json
```

The queue keeps `max_active_tasks` runs going and rewrites
`runs/<run_id>/QUEUE_STATUS.md` every poll. Stopping the queue leaves running
tasks alive; running the same command again adopts them, keeps finished
results and retries interrupted attempts. The queue refuses a different config
with the same `run_id`; only `max_active_tasks` and `launch_stagger_seconds`
may change.

## Read results

```text
runs/<run_id>/
  config.json            the config this directory belongs to
  gateway-tokens.jsonl   gateway token log (default location)
  tasks/<task-id>/
    result.json          the outcome: status, score, termination_reason, usage, ablate
    meta.json            guest and runtime diagnostics
    agent.jsonl, phase-*-turn-*-*  agent traces per turn
    final-*.png          screenshots before and after evaluation
    guard.jsonl          guard decisions, only with guard ablated
  logs/<task-id>.log     runner output
  retries/<task-id>-<n>/ attempts interrupted by infrastructure, not scored
  QUEUE_STATUS.md
```

`result.json` `status` is `completed`, `evaluator_error` or `error`.
`termination_reason` is `completed`, `max_model_calls` or `timeout`.

```bash
python3 -m hcu.osworld.summarize runs/<run_id>
```

This writes `summary/summary.json`, `tasks.csv` and `summary.md`. The
denominator is every task in the config. A task that hit the budget or the
timeout scores 0 even if the evaluator passed it. A task without a usable
score (missing, evaluator error, runner error) withholds the final score;
`lower_bound` counts it as 0. Token counts appear only when the gateway
accounted for every generation of the task.

## Notes

- Guests cannot reach host loopback. If every VM fails to start with a
  `docker0` error, the Docker bridge is gone; the provider only uses the
  default `bridge` network.
- A passing evaluator does not prove that the agent did the task as asked;
  read the trace and screenshots too.
- This repository's `AGENTS.md` is for developers and must never reach an
  evaluated agent.
