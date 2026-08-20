# libcommand Playground

English | [简体中文](README.zh-CN.md)

The playground runs `libcommand` as WebAssembly inside a Web Worker, so Bash
source and simulation inputs stay in the browser. It does not execute host
commands or send scripts to the HTTP server.

## Build and run

Build the ordinary executable containing the UI and WebAssembly runtime:

```bash
make playground
```

The retained Playground product is `target/playground/playground`. Run it and open
`http://localhost:8080`:

```bash
./target/playground/playground -addr 127.0.0.1:8080
```

The resulting file is self-contained and has no Python, Node, or Go runtime
dependency. The build requires the Go toolchain selected by the environment.
Use `-addr :8080` only when the playground should listen on every interface.

Run `make all` to build every deliverable command. The example CLI is written
to `target/libcommand/libcommand`. The `cmd/playground-wasm` command is an
internal Playground asset and is embedded by `make playground`, rather than
being retained as a separate product.

Remove the binary and any interrupted embed staging directory with:

```bash
make clean
```

## Guided tour

### Live Runtime flow

The Message loop example shows the Runtime graph being constructed from actual
expanded command calls. Nodes appear as trace events arrive, the active call is
highlighted and followed automatically, and selecting `lark-cli` exposes that
node's concrete invocation and result in the inspector.

![The Playground builds a live Runtime execution graph and inspects one lark-cli result](../assets/playground/runtime-flow.gif)

### Live AST walkthrough

The recording selects the AST perspective first and clicks **Run simulation**.
The parse-only topology remains in place while reached nodes gain their actual
path, execution steps, and logical memory; syntax that no path reaches remains
dashed. Nested condition commands, substitutions, and pipeline operands are
folded into their owning statement; branch and loop bodies remain visible.
Commands parsed dynamically by `eval` or `source` appear in Runtime, while the
initial AST remains unchanged.

![The Playground highlights a stable AST while the simulation executes](../assets/playground/ast-flow.gif)

## What the graph means

- **Runtime** is the default perspective. It shows only expanded command calls
  in execution order, including commands produced by substitutions, `eval`, or
  `source`. Control statements and expansion containers are omitted, so a
  decoded call is shown as the commands that produced it followed by the command
  that ran. Every call is a separate occurrence; commands from alternative paths
  under one unresolved fork share a row. Unregistered commands are retained as
  purple unresolved nodes. Runtime nodes come from Worker trace events received
  while simulation is active. The first command appears immediately and later
  commands are revealed one per selected speed interval, rather than replacing
  the canvas with a complete graph.
- **AST** is available before simulation. A parse-only Worker request builds the
  current source's static syntax skeleton without executing Shell code or
  command handlers. Simulation updates those same nodes in place but never adds
  dynamically parsed `eval` or `source` content to the tree. It folds trace
  nodes marked `Embedded`—condition evaluation,
  substitutions, and pipeline operands already represented by their parent—so
  control-flow bodies and calls remain readable. The complete detail remains in
  the trace, while Runtime retains its concrete command calls. Statements that
  were never reached remain visible for comparing syntax and reachability.
- Solid nodes were reached by the simulator.
- Purple forks are distinct paths explored because a value or status was
  unresolved.
- Dashed nodes were found in the parsed syntax but were not reached by any
  simulated path.
- Every actual visit re-highlights the corresponding AST node. Loop headers and
  body statements therefore activate again on each iteration, while their
  execution count and inspector context retain the latest occurrence. Runtime
  records every concrete command call as a separate occurrence.
- AST graphs start at the top center. Sequential and loop bodies stay on the
  main vertical line whenever possible; true alternative bodies share a row and
  rejoin before the following statement.
  A condition without an exhaustive alternative retains a direct fallthrough
  edge. Loops remain one forward syntax sequence from header through body to
  the following statement; iteration back edges and zero-iteration bypasses
  are not added to the AST. Runtime transition events update existing AST
  execution state but never synthesize AST edges.
  Live following keeps the active node horizontally centered and scrolls
  vertically only enough to keep it visible with context.

The memory figures are `libcommand`'s logical retained-state accounting used by
`MaxMemoryBytes`. They are useful for understanding path growth and budget
failures, but they are not measurements of the Go or WebAssembly heap.

The speed control beside zoom paces the selected Runtime or AST perspective
from `0.1x` to `2.0x`; there is no separate post-run replay. During a live
session the primary button pauses or resumes visualization. Shell simulation
may finish in the Worker while visualization is paused, with trace events
retained in the queue. Stop terminates an executing Worker, discards unrevealed
events, and retains the nodes already visible. When simulation finishes faster
than visualization, the status remains `Drawing simulation flow` until the
queue drains.

Registered command definitions in the Commands tab can return configured
stdout, stderr, and exit code; return an error; or run a synchronous JavaScript
function body against the expanded invocation. A JavaScript handler receives
`invocation.name`, typed `invocation.args`, `invocation.env`, `invocation.dir`,
decoded `invocation.stdin`, and unresolved metadata. It must return an object
containing optional `stdout`, `stderr`, and `exitCode` fields. Promises are not
supported. Commands that are not listed use the library's unresolved fallback.

The Playground installs its command-analysis registry as one Builder
middleware. Consequently matching builtins, configured handlers, and fallback
calls all pass through analysis without separate wrapping. Analysis results are
discarded; a classified risk error is recorded without stopping the simulation,
and the selected command's result or error remains authoritative. Detected AST
nodes and the matching Runtime occurrences are outlined in red, and selecting
one shows the full detection error in the inspector. The default registry
covers destructive root removal, system power commands, executable netcat
variants, `socat` execution addresses, and expanded `/dev/tcp` or `/dev/udp`
redirections for those commands.

JavaScript handlers run only inside the same disposable browser Worker as the
WebAssembly simulator. They cannot execute a server or host command, and the
browser timeout terminates infinite loops by replacing the Worker. This is not
a general hostile-JavaScript sandbox: public deployments should use a dedicated
origin without credentials or sensitive browser storage. Page load only
prepares the selected example or shared state; simulation starts after Run is
pressed. Shared URLs containing JavaScript retain the additional review notice.

The node inspector exposes Overview, Input, and Output tabs. Command inputs show
the current invocation's name, categorized arguments, directory, stdin, and
exported environment alongside the node's relevant Shell state. Internal
`OPTIND` state is omitted from the Variables display. Output shows only the
current command result or statement stream delta, rather than inherited
path-wide stdout and stderr. Empty streams remain concrete empty values.
Snapshot and command-output collection is enabled only by the Playground and
has a shared 4 MiB display budget; ordinary `Simulate` calls do not enable
tracing.

The browser additionally caps requests, trace events, rendered graph nodes,
execution time, and memory so a shared public playground remains responsive.
