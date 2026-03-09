# spex

A generic parallel process runner with a live terminal UI. Runs N shell commands in parallel, shows live output tails per process, and reports structured JSON results.

## Demo

Run the included demo to see spex in action with 8 simulated services (~13 seconds, one intentional failure):

```bash
./demo.sh
```

This runs 8 services with `--max-parallel 4`. One service (`mailer`) exits non-zero on purpose to demonstrate failure handling.

Early in the run, all processes are shown with their live log tails:

![Early run](docs/img1.png)

As processes complete they are bumped to the top, with failed ones marked in red:

![Mid run](docs/img2.png)

## Installation

**Homebrew (macOS):**

```bash
brew install quantumcycle/tap/spex
```

**Install script (Linux and macOS):**

```bash
curl -fsSL https://raw.githubusercontent.com/quantumcycle/spex/main/install.sh | bash
```

Installs the latest release to `/usr/local/bin/spex`, using `sudo` if needed.

**Build from source:**

```bash
go build -o spex .
```

## Usage

Commands are read from **stdin**, one per line, tab-separated:

```
name<TAB>command
```

```bash
spex [flags] <<EOF
api-server    bash run-service.sh api-server
auth-service  bash run-service.sh auth-service
worker-a      bash run-service.sh worker-a
EOF
```

### Flags

| Flag | Default | Description |
|------|---------|-------------|
| `--max-parallel` / `-p` | `4` | Max number of concurrent processes |
| `--tail` / `-n` | `10` | Number of log lines shown per process in the status board |
| `--fail-fast` | `false` | Kill all running processes as soon as one exits non-zero |
| `--output` / `-o` | `errors` | Comma-separated output tokens: `errors`, `success`, `all`. A single token prints output immediately for that category (`errors` = failures only, `success` = successes only, `all` = everything). Multiple tokens buffer all output and flush in the given order at the end (e.g. `success,errors` prints successes before failures). `all` must appear alone. |
| `--log-dir` | _(none)_ | Directory for per-runner log files. Each runner writes to `<dir>/<name>-<seed>.log` where the seed is shared across all runners in one invocation. The path appears as `log_file` in JSON output. |

## Output

**stderr** — human-readable output (live TUI in interactive mode, streaming lines in CI mode).

**stdout** — JSON summary written once all processes finish.

This separation lets you capture the JSON without interference from status output:

```bash
result=$(spex --max-parallel 4 < runners.txt)
echo "$result" | jq -r '.runners[] | select(.success) | .name'
```

### JSON schema

```json
{
  "success": true,
  "duration": "2m14s",
  "duration_ms": 134000,
  "runners": [
    {
      "name": "api-server",
      "success": true,
      "exit_code": 0,
      "duration": "45s",
      "duration_ms": 45000,
      "log_file": "/tmp/spex-logs/api-server-6831e410.log"
    },
    {
      "name": "mailer",
      "success": false,
      "exit_code": 1,
      "duration": "12s",
      "duration_ms": 12000,
      "log_file": "/tmp/spex-logs/mailer-6831e410.log"
    }
  ]
}
```

`log_file` is omitted when `--log-dir` is not set.

## Interactive mode (TTY)

When running in a terminal, spex renders a live status board on stderr:

```
spex  3/8 done • 2 running • 3 pending

  ✓ api-server    45s
  ⠸ auth-service  12s
    ↳ [INFO] received request method=GET path=/api/v1/status
    ↳ [INFO] db query table=events rows=42 latency=120ms
    ↳ [INFO] cache HIT key=session:456
  ⠸ worker-a      8s
    ↳ [INFO] queue depth=15 jobs pending
    ↳ [DEBUG] goroutine pool size=8 active=3
  · worker-b      pending
  · cache-loader  pending
  · mailer        pending
```

Status icons:

- `·` pending
- `⠸` running (cycling spinner)
- `✓` exited 0
- `✗` exited non-zero

When all processes finish, the board is replaced by a final summary. Output display is governed by `--output`: `errors` (default) prints output only for failures, `all` prints output for every process, and a token list like `success,errors` buffers and flushes in the given order. JSON is then written to stdout.

## CI mode (no TTY / `NO_COLOR`)

When stdout is not a TTY or `NO_COLOR` is set, spex writes plain lines to stderr as events happen:

```
[api-server]   starting
[auth-service] starting
[mailer]       starting
[api-server]   ✓ done in 45s
[auth-service] ✓ done in 38s
[mailer]       ✗ exited 1 in 12s

--- mailer output ---
<full buffered output>
---

3 done, 1 error, 2m14s
```

Output display is governed by `--output` (default `errors`): only failures are shown immediately. Use `--output all` to print output for every process as it finishes, or a token list like `--output success,errors` to buffer all output and flush in the given order at the end.

## Signal handling

On `SIGINT` or `SIGTERM`:

1. Forward the signal to all running child processes
2. Wait up to 5 seconds for them to exit
3. Force-kill any that remain
4. Write partial JSON to stdout (unfinished runners marked `"success": false, "exit_code": null`)
5. Exit with code 130 (SIGINT) or 143 (SIGTERM)
