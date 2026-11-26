# nvidia-health-validation-mcp

A read-only MCP server for NVIDIA health-validation
([`github.com/dsx-ai-factory/health-validation`](https://github.com/dsx-ai-factory/health-validation)),
written in Go like health-validation itself. An MCP client such as Claude Code can browse the check
catalog, ask what a check or a remediation step means, and, when the operator allows it, ask the
node's `health-validation serve` process for one verdict.

health-validation only judges a node and recommends. It never acts on it, and neither does this
server: it has no tool that changes anything. Cordon, drain and GPU reset belong to the scheduler or
to a remediation system.

## How it works

```text
MCP client --stdio--> nvidia-health-validation-mcp --HTTP, loopback only--> health-validation serve --> GPU, DCGM, journal
                      |
                      +-- the health-validation catalog, compiled in (no node access)
```

- The catalog tools read hv's pure-Go `catalog` package, built into this binary at
  `v0.4.0-rc.1`. They never touch the node, so they work anywhere, a laptop included.
- `hv_service_status` and `hv_get_verdict` call `health-validation serve`, which the operator already
  runs on the node on a loopback address. This server holds no GPU, journal or DCGM access, builds with
  `CGO_ENABLED=0`, and runs as a non-root user.
- `hv_get_verdict` is off unless `HV_MCP_ALLOW_EVALUATE=true`, as each call starts real collection on
  the node.

## Test results

[docs/test-results/fixture](docs/test-results/fixture) holds what the built binary returned over stdio
in `make e2e`, one file per case: a healthy GB200, NVLink down, Xid 79, an unknown architecture, a
threshold override, a discovery failure, a busy source, serve stopped or killed mid-call, a refused
second call, a refused port owner, and evaluation left off. **These come from synthetic measurements:**
`lab/hvfixture` runs health-validation's real verdict server and catalog, but feeds it made-up readings,
not a GPU. No real serve has been run yet; see [What is not tested](#what-is-not-tested).

## Standards

Each requirement, its status, the test that proves it and the known gaps are in
[docs/mcp-standards.md](docs/mcp-standards.md), with links to check for newer versions.

| Standard | Version |
|---|---|
| [Model Context Protocol](https://modelcontextprotocol.io/specification/latest) | 2026-07-28, through go-sdk v1.8.0, which also offers older revisions back to 2024-11-05 (not tested) |
| [MCP Registry `server.json`](https://github.com/modelcontextprotocol/registry/blob/main/docs/reference/server-json/generic-server-json.md) | 2025-12-11, valid, not published yet |
| [AGENTS.md](https://agents.md) | [AGENTS.md](AGENTS.md), imported by `CLAUDE.md` |
| [Agent Skills](https://agentskills.io/home) | [skills/nvidia-health-validation](skills/nvidia-health-validation/SKILL.md) |
| health-validation verdict schema | `0.7` only; any other version gives `NO_VERDICT` |

It has not been run through the official [conformance suite](https://github.com/modelcontextprotocol/conformance),
which only tests HTTP servers. MCP has no standard for written docs, so [docs/tools.md](docs/tools.md) is
generated from the server.

## Tools

| Tool | Reads the node | What it does |
|---|---|---|
| `hv_service_status` | serve's `/healthz` only | Whether serve answers, stuck source calls, whether evaluation is on and when it may next run, the pinned catalog and schema versions. Liveness is not node health |
| `hv_list_checks` | no | The compiled-in catalog, 25 checks per page, filtered by category, architecture, cadence and maturity |
| `hv_describe_check` | no | One check's signals, rules, thresholds, dependencies and recommended action |
| `hv_explain_remediation` | no | What a remediation step means, and which checks recommend it |
| `hv_get_verdict` | yes, through serve | One evaluation: `scheduling_advice`, severity, node state, ordered remediation steps, and a coverage block. Only with `HV_MCP_ALLOW_EVALUATE=true` |

Every argument is an enum or a bounded integer. `hv_get_verdict` takes `cadence`, `detail` and
`xid_lookback_minutes`; it does not take a node ID, an execution context or an Xid end time, so serve's
own defaults apply.

**Read `scheduling_advice` and `coverage`, not only `overall_severity`.** A `PASS` where no GPU, NVLink
or Xid check ran, as on a GPU that health-validation does not know, comes back as `READY_LOW_COVERAGE`.
That is not healthy. The values are `READY`, `READY_LOW_COVERAGE`, `MONITOR`, `DRAIN`, `RETRY_COLLECTION`
and `NO_VERDICT`.

Every tool's inputs, outputs and annotations: [docs/tools.md](docs/tools.md), generated from what the
server returns for `tools/list`, as is the raw [docs/tools.json](docs/tools.json). A test fails if they no
longer match the server; `make docs` regenerates them.

## Install

There is no release yet. Until there is, build it with Go 1.27.3 or later:

```shell
go install github.com/omerfsen/nvidia-health-validation-mcp/cmd/nvidia-health-validation-mcp@latest
```

or from a checkout, `make build`, which writes `bin/nvidia-health-validation-mcp`.

Once released, each release will carry one binary per platform, with `checksums.txt` and an SBOM:

| Platform | File |
|---|---|
| Linux, x86-64 | `nvidia-health-validation-mcp-linux-amd64` |
| Linux, Arm | `nvidia-health-validation-mcp-linux-arm64` |
| macOS, Intel | `nvidia-health-validation-mcp-darwin-amd64` |
| macOS, Apple silicon | `nvidia-health-validation-mcp-darwin-arm64` |
| Windows, x86-64 | `nvidia-health-validation-mcp-windows-amd64.exe` |
| Windows, Arm | `nvidia-health-validation-mcp-windows-arm64.exe` |

On Linux and macOS, set `FILE` to yours:

```shell
FILE=nvidia-health-validation-mcp-linux-amd64
BASE=https://github.com/omerfsen/nvidia-health-validation-mcp/releases/latest/download
curl -fsSLO "$BASE/$FILE"
curl -fsSL "$BASE/checksums.txt" | grep " $FILE\$" | sha256sum -c -   # macOS: shasum -a 256 -c -
gh attestation verify "$FILE" --repo omerfsen/nvidia-health-validation-mcp
install -m 755 "$FILE" ~/.local/bin/nvidia-health-validation-mcp
```

On Windows, in PowerShell:

```powershell
$File = "nvidia-health-validation-mcp-windows-amd64.exe"
$Base = "https://github.com/omerfsen/nvidia-health-validation-mcp/releases/latest/download"
Invoke-WebRequest "$Base/$File" -OutFile "$HOME\bin\nvidia-health-validation-mcp.exe"
```

On macOS and Windows only the catalog tools are useful: `health-validation serve` runs on Linux GPU
nodes, and this server only talks to a serve on the same host. `HV_MCP_SERVE_UID` needs Linux.

Check it with `nvidia-health-validation-mcp --version`, which also prints the pinned catalog version.
Run with no flags, it logs one line to stderr and then waits for an MCP client on stdin, so it looks
idle: that is normal, and Ctrl-C stops it. `--help` lists the flags and settings, and `--debug` logs each
call to serve on stderr: method, path, HTTP status, duration and error kind, never the body.

**Add it to your client.** For Claude Code, catalog tools only:

```shell
claude mcp add health-validation -- nvidia-health-validation-mcp
```

With evaluation on, on a node that runs serve:

```shell
claude mcp add health-validation -e HV_MCP_ALLOW_EVALUATE=true -- nvidia-health-validation-mcp
```

Other clients take the same command in their MCP config:

```json
{ "mcpServers": { "health-validation": { "command": "nvidia-health-validation-mcp" } } }
```

**The image.** `docker.io/omerfsen/nvidia-health-validation-mcp` will be published with the first
release: one static binary on distroless, user 65532, about 31 MB. Build it locally with `make image`,
which tags it `nvidia-health-validation-mcp:local`. It must share serve's network namespace to reach its
loopback address:

```shell
docker run --rm -i --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --memory=128m --cpus=1 --pids-limit=64 --network=host \
  -e HV_MCP_ALLOW_EVALUATE=true \
  nvidia-health-validation-mcp:local
```

Use `--network=container:<serve container>` instead of `--network=host` when serve runs in a
container without host networking. It needs no devices, mounts or journal. `server.json` gives the
Registry the same flags, `--network=host` included.

**Optional: the skill**, which tells an agent how to read verdicts safely. It holds only instructions:

```shell
gh skill install omerfsen/nvidia-health-validation-mcp nvidia-health-validation
npx skills add omerfsen/nvidia-health-validation-mcp --skill nvidia-health-validation
```

## Run health-validation serve beside it

`hv_service_status` and `hv_get_verdict` need `health-validation serve` on the same host, on a numeric
loopback address. Serve is not part of this repo; build or pull it from health-validation, and run it as
its [agent-mode guide](https://github.com/dsx-ai-factory/health-validation/blob/main/docs/agent-mode.md)
says, with the GPU and journal access it needs:

```shell
health-validation serve --address 127.0.0.1:8080 --execution-context OPERATOR_INFRA
```

- The port must match `HV_MCP_SERVE_PORT` (default 8080). For `--address [::1]:8080`, set
  `HV_MCP_SERVE_HOST=::1`.
- This server never sends an execution context, so serve's `--execution-context` decides which checks
  run. Serve's default is `TENANT_GUEST`; health-validation's own examples use `OPERATOR_INFRA` on the node.
- Serve's `--config` (disabled checks, threshold overrides) applies to every verdict. The catalog tools
  show the compiled-in defaults, not that config.
- Serve's default `--request-timeout` is 2 minutes. `HV_MCP_EVALUATE_TIMEOUT_SECONDS` (default 150) should
  stay above it, or this server gives up first and reports `timeout`.
- Serve has no authentication: any local process can call it, and could also listen on its port if serve
  is down. Set `HV_MCP_SERVE_UID` to the user serve runs as, and this server checks, on Linux, that the
  listening socket belongs to that user before each call.
- In Kubernetes, run this server as a sidecar in serve's pod, so both share loopback.

Pin serve to the same health-validation version as this server's catalog (`v0.4.0-rc.1`). If they
differ, a verdict says `version_skew: true`, and the catalog tools may describe checks that serve does
not run.

## Settings

| Variable | Default | What it does |
|---|---|---|
| `HV_MCP_SERVE_HOST` | `127.0.0.1` | Where serve listens: exactly `127.0.0.1` or `::1` |
| `HV_MCP_SERVE_PORT` | `8080` | Serve's port, 1024 to 65535 |
| `HV_MCP_SERVE_UID` | off | If set, the numeric user ID that must own serve's listening socket. Linux only |
| `HV_MCP_ALLOW_EVALUATE` | off | Exactly `true` adds `hv_get_verdict`. Any other value leaves it off and is logged |
| `HV_MCP_EVALUATE_INTERVAL_SECONDS` | `10` | At most one evaluation per this many seconds, 5 to 600 |
| `HV_MCP_EVALUATE_TIMEOUT_SECONDS` | `150` | How long to wait for a verdict, 10 to 900 |

A bad value for any setting but `HV_MCP_ALLOW_EVALUATE` stops the server at start. A second evaluation
while one runs is refused, not queued, and all tool calls share a limit of 10 per second, burst 20.
Nothing is retried: a busy serve (HTTP 429) is reported as `busy`.

## What is not tested

Everything below was proven only with synthetic measurements judged by health-validation's own engine
(in unit tests against a fake serve, and in `make e2e` against `lab/hvfixture`), or not at all:

- **Any real serve.** Not a real `health-validation serve` on the Pi (no GPU) or on the build machine
  (RTX 4080 SUPER, an architecture health-validation does not know). `make e2e-serve` is written for
  it but has not been run.
- **The published image:** `make e2e-image` passed once against a locally built image on the Pi, with
  the SIGTERM test skipped, since a signal reaches the docker client, not the server. Nothing is
  published yet. `make scan` is not written yet and exits 1.
- **Any architecture-scoped check**, as no available machine has a GPU health-validation knows.
- **DCGM signals:** power, power limit, performance state, throttle, NVLink fabric.
- **NVLink, InfiniBand and P2P** checks.
- **Real Xid detection** from a kernel journal.
- **A hardware-fault CRITICAL** from a real node.
- **A real `collection_source_busy`** verdict from serve.
- **Load across a fleet**, or a real HTTP 429 from serve.
- **The race detector on arm64:** it cannot start on the Pi's kernel. It ran in an emulated amd64 container.
- **The binaries for anything but Linux on Arm:** the others are cross-compiled and not run.
- **Evaluations** (questions a model answers through the tools), Cisco `mcp-scanner`, Trivy and OSV.

Every step, failure and dead end is in [docs/build-log.md](docs/build-log.md).

## License

Apache-2.0, like health-validation, which this server compiles in.
