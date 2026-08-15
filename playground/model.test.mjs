import test from "node:test";
import assert from "node:assert/strict";
import * as playgroundModel from "./model.js";

import {
  advanceRuntimeFlow,
  buildFlowModel,
  buildFlowModels,
  createASTFlowModel,
  createRuntimeFlowModel,
  formatBytes,
  layoutASTFlowGraph,
  layoutFlowGraph,
  liveControlView,
  nodeOutput,
  normalizeCommands,
  parseEnvironment,
  tokenizeBash,
} from "./model.js";

test("live controls distinguish idle running and paused sessions", () => {
  assert.deepEqual(liveControlView(false, false), {
    icon: "▶",
    label: "Run simulation",
    running: false,
    stopVisible: false,
  });
  assert.deepEqual(liveControlView(true, false), {
    icon: "Ⅱ",
    label: "Pause",
    running: true,
    stopVisible: true,
  });
  assert.deepEqual(liveControlView(true, true), {
    icon: "▶",
    label: "Resume",
    running: true,
    stopVisible: true,
  });
});

test("runtime flow grows when command events arrive", () => {
  const runtime = createRuntimeFlowModel();
  runtime.append({ kind: "node_discovered", node: node(1, 0, "command", "echo live") });
  runtime.append(commandStarted(1, 1, "echo", [{ kind: 0, value: "live" }]));

  assert.equal(runtime.model.nodes.length, 1);
  assert.equal(runtime.model.nodes[0].definition.snippet, "echo live");
  assert.equal(runtime.model.nodes[0].commandResult, null);

  runtime.append(commandFinished(2, 1, { exitCode: 0, stdout: "live\n", outputCaptured: true }));

  assert.equal(runtime.model.nodes.length, 1);
  assert.equal(runtime.model.nodes[0].commandResult.stdout, "live\n");
  assert.equal(runtime.model.maximumSequence, 2);
});

test("advanceRuntimeFlow reveals at most one command node per visual step", () => {
  const runtime = createRuntimeFlowModel();
  const events = [
    commandStarted(1, 1, "first"),
    commandFinished(2, 1),
    commandStarted(3, 2, "second"),
    commandFinished(4, 2),
  ];

  const first = advanceRuntimeFlow(runtime, events, 0);
  assert.deepEqual(first, { next: 1, changed: true, revealed: true });
  assert.deepEqual(runtime.model.nodes.map((item) => item.invocation.name), ["first"]);

  const second = advanceRuntimeFlow(runtime, events, first.next);
  assert.deepEqual(second, { next: 3, changed: true, revealed: true });
  assert.deepEqual(runtime.model.nodes.map((item) => item.invocation.name), ["first", "second"]);

  const finished = advanceRuntimeFlow(runtime, events, second.next);
  assert.deepEqual(finished, { next: 4, changed: true, revealed: false });
});

test("buildFlowModels separates complete AST syntax from actual runtime commands", () => {
  const models = buildFlowModels({
    nodes: [
      node(1, 0, "condition", "if feature-enabled"),
      node(2, 1, "command", "echo yes"),
      node(3, 1, "command", "echo no"),
    ],
    events: [
      event(1, "statement_started", 1, 1),
      {
        sequence: 2,
        kind: "command_started",
        nodeId: 2,
        pathId: 1,
        invocation: { name: "echo", args: [{ kind: 0, value: "yes" }] },
      },
      {
        sequence: 3,
        kind: "command_finished",
        nodeId: 2,
        pathId: 1,
        commandResult: { exitCode: 0 },
      },
      event(4, "statement_finished", 1, 1),
    ],
  });

  assert.deepEqual(models.ast.nodes.map((item) => item.nodeID).sort((left, right) => left - right), [1, 2, 3]);
  assert.equal(models.ast.nodes.find((item) => item.nodeID === 2).executed, true);
  assert.equal(models.ast.nodes.find((item) => item.nodeID === 3).state, "not-executed");
  assert.deepEqual(models.runtime.nodes.map((item) => item.invocation.name), ["echo"]);
});

test("AST folds embedded evaluation details while runtime keeps executed commands", () => {
  const models = buildFlowModels({
    nodes: [
      node(1, 0, "loop", "for i in $(seq 1 3)"),
      { ...node(2, 1, "command", "seq 1 3"), embedded: true },
      node(3, 1, "condition", "if (( i == 1 ))"),
      { ...node(4, 3, "arithmetic", "(( i == 1 ));"), embedded: true },
      node(5, 3, "command", "text=first"),
      node(6, 1, "command", "lark-cli send"),
    ],
    events: [
      event(1, "statement_started", 1, 1),
      commandStarted(2, 2, "seq", [{ kind: 0, value: "1" }, { kind: 0, value: "3" }]),
      commandFinished(3, 2),
      event(4, "statement_started", 3, 1),
      { sequence: 5, kind: "path_forked", nodeId: 4, pathId: 1, childPathIds: [2, 3] },
      commandStarted(6, 6, "lark-cli", [{ kind: 0, value: "send" }], 2),
      commandFinished(7, 6, { exitCode: 0 }, 2),
    ],
  });

  assert.deepEqual(models.ast.nodes.map((item) => item.nodeID), [1, 3, 5, 6]);
  assert.equal(models.ast.nodes.find((item) => item.nodeID === 3).state, "unresolved");
  assert.deepEqual(models.runtime.nodes.map((item) => item.invocation.name), ["seq", "lark-cli"]);
});

test("AST connects visible syntax through an embedded structural parent", () => {
  const model = buildFlowModel({
    nodes: [
      node(1, 0, "operator", "producer | { consumer; }"),
      { ...node(2, 1, "block", "{ consumer; }"), embedded: true },
      node(3, 2, "command", "consumer"),
    ],
  });

  assert.deepEqual(model.nodes.map((item) => item.nodeID), [1, 3]);
  assert.ok(model.edges.some((edge) => edge.from === "ast:1" && edge.to === "ast:3"));
});

test("streamed events cannot reveal an embedded AST node before the run finishes", () => {
  const ast = createASTFlowModel([
    node(1, 0, "loop", "for i in $(seq 1 3)"),
    { ...node(2, 1, "command", "seq 1 3"), embedded: true },
  ]);

  ast.append(commandStarted(1, 2, "seq", [{ kind: 0, value: "1" }, { kind: 0, value: "3" }]));

  assert.deepEqual(ast.model.nodes.map((item) => item.nodeID), [1]);
  assert.equal(ast.model.definitions.get(2).embedded, true);
});

test("AST syntax identity and topology stay stable when execution is overlaid", () => {
  const nodes = [
    node(1, 0, "condition", "if feature-enabled"),
    node(2, 1, "command", "echo yes"),
    node(3, 1, "command", "echo no"),
  ];
  const preview = buildFlowModel({ nodes });
  const executed = buildFlowModel({
    nodes,
    events: [
      event(1, "statement_started", 1, 1),
      event(2, "statement_started", 2, 1),
      event(3, "statement_finished", 2, 1),
      event(4, "statement_finished", 1, 1),
    ],
  });

  assert.deepEqual(executed.nodes.map((item) => item.id), preview.nodes.map((item) => item.id));
  assert.deepEqual(edgePairs(executed), edgePairs(preview));
  assert.equal(executed.nodes.find((item) => item.nodeID === 2).executed, true);
  assert.equal(executed.nodes.find((item) => item.nodeID === 3).state, "not-executed");
});

test("AST source-order edges continue after the previous top-level subtree", () => {
  const model = buildFlowModel({
    nodes: [
      node(1, 0, "condition", "if true"),
      node(2, 1, "command", "echo nested"),
      node(3, 0, "command", "echo after"),
    ],
  });

  assert.ok(model.edges.some((edge) => edge.from === "ast:2" && edge.to === "ast:3"));
  assert.ok(!model.edges.some((edge) => edge.from === "ast:1" && edge.to === "ast:3"));
});

test("AST flow can overlay streamed execution without replacing its syntax nodes", () => {
  const ast = createASTFlowModel([
    node(1, 0, "condition", "if true"),
    node(2, 1, "command", "echo reached"),
    node(3, 1, "command", "echo skipped"),
  ]);

  assert.deepEqual(ast.model.nodes.map((item) => item.state), ["not-executed", "not-executed", "not-executed"]);
  ast.append(event(1, "statement_started", 1, 1));
  ast.append(event(2, "statement_started", 2, 1));

  assert.equal(ast.model.nodes.find((item) => item.nodeID === 1).executed, true);
  assert.equal(ast.model.nodes.find((item) => item.nodeID === 2).executed, true);
  assert.equal(ast.model.nodes.find((item) => item.nodeID === 3).state, "not-executed");
});

test("runtime flow uses expanded command order and hides eval containers", () => {
  const models = buildFlowModels({
    nodes: [
      node(1, 0, "command", `eval "$(echo encoded | base64 -d)"`),
      node(2, 1, "command", "echo encoded"),
      node(3, 1, "command", "base64 -d"),
      node(4, 1, "command", "lark-cli im second"),
    ],
    events: [
      commandStarted(1, 2, "echo", [{ kind: 0, value: "encoded" }]),
      commandFinished(2, 2),
      commandStarted(3, 3, "base64", [{ kind: 0, value: "-d" }]),
      commandFinished(4, 3),
      commandStarted(5, 1, "eval", [{ kind: 0, value: "lark-cli im second" }]),
      commandStarted(6, 4, "lark-cli", [{ kind: 0, value: "im" }, { kind: 0, value: "second" }]),
      commandFinished(7, 4),
      commandFinished(8, 1),
    ],
  });

  const runtime = models.runtime;
  assert.deepEqual(runtime.nodes.map((item) => item.invocation.name), ["echo", "base64", "lark-cli"]);
  assert.equal(runtime.nodes[2].definition.snippet, "lark-cli im second");
  assert.deepEqual(runtime.edges.map((edge) => [
    runtime.nodes.find((item) => item.id === edge.from).invocation.name,
    runtime.nodes.find((item) => item.id === edge.to).invocation.name,
  ]), [["echo", "base64"], ["base64", "lark-cli"]]);
});

test("AST flow retains syntax discovered while eval is executing", () => {
  const dynamic = node(2, 1, "command", "lark-cli im messages-send");
  const model = buildFlowModel({
    nodes: [node(1, 0, "command", "eval \"$decoded\"")],
    events: [
      { sequence: 1, kind: "node_discovered", node: dynamic },
      event(2, "statement_started", 2, 1),
      event(3, "statement_finished", 2, 1),
    ],
  });

  const executed = model.nodes.find((item) => item.nodeID === 2 && item.executed);
  assert.equal(executed.definition.snippet, "lark-cli im messages-send");
  assert.equal(model.definitions.get(2).snippet, "lark-cli im messages-send");
  assert.ok(model.edges.some((edge) => edge.from === "ast:1" && edge.to === "ast:2"));
});

test("runtime flow marks an unregistered command unresolved", () => {
  const models = buildFlowModels({
    nodes: [node(1, 0, "command", "external-command value")],
    events: [
      commandStarted(1, 1, "external-command", [{ kind: 0, value: "value" }]),
      commandFinished(2, 1, { unresolved: true, exitCodeUnresolved: true }),
    ],
  });

  assert.equal(models.runtime.nodes.length, 1);
  assert.equal(models.runtime.nodes[0].state, "unresolved");
});

test("concreteDisplayValue renders an empty value as empty", () => {
  assert.equal(typeof playgroundModel.concreteDisplayValue, "function");
  assert.equal(playgroundModel.concreteDisplayValue(""), "");
  assert.equal(playgroundModel.concreteDisplayValue(null), "");
  assert.equal(playgroundModel.concreteDisplayValue(0), "0");
});

test("containsJavaScriptCommands identifies shared code requiring review", () => {
  assert.equal(typeof playgroundModel.containsJavaScriptCommands, "function");
  assert.equal(playgroundModel.containsJavaScriptCommands([
    { name: "fixed", outcome: "resolved" },
    { name: "dynamic", outcome: "javascript", javascript: "return {};" },
  ]), true);
  assert.equal(playgroundModel.containsJavaScriptCommands([
    { name: "fixed", outcome: "resolved" },
    { name: "broken", outcome: "error" },
  ]), false);
});

test("startup mode always waits for an explicit run", () => {
  assert.equal(typeof playgroundModel.startupMode, "function");
  assert.equal(playgroundModel.startupMode(false, []), "ready");
  assert.equal(playgroundModel.startupMode(true, [
    { name: "lark-cli", outcome: "javascript", javascript: "return {};" },
  ]), "review");
  assert.equal(playgroundModel.startupMode(true, [
    { name: "lark-cli", outcome: "resolved" },
  ]), "ready");
});

test("layoutFlowGraph places execution successors below their parent", () => {
  const layout = layoutFlowGraph([
    { id: "parent", pathID: 1 },
    { id: "successor", pathID: 1 },
  ], [
    { from: "parent", to: "successor" },
  ]);

  const parent = layout.positions.get("parent");
  const successor = layout.positions.get("successor");
  assert.equal(successor.x, parent.x);
  assert.ok(successor.y > parent.y);
});

test("layoutFlowGraph centers a narrow graph within the requested canvas width", () => {
  const layout = layoutFlowGraph([
    { id: "only", pathID: 1 },
  ], [], 1000);

  assert.equal(layout.width, 1000);
  assert.equal(layout.positions.get("only").x, 402);
  assert.equal(layout.positions.get("only").y, 40);
});

test("layoutFlowGraph separates forked paths into horizontal lanes", () => {
  const layout = layoutFlowGraph([
    { id: "parent", pathID: 1 },
    { id: "left", pathID: 2 },
    { id: "right", pathID: 3 },
  ], [
    { from: "parent", to: "left" },
    { from: "parent", to: "right" },
  ]);

  const parent = layout.positions.get("parent");
  const left = layout.positions.get("left");
  const right = layout.positions.get("right");
  assert.equal(left.y, right.y);
  assert.ok(left.y > parent.y);
  assert.notEqual(left.x, right.x);
});

test("layoutASTFlowGraph follows syntax preorder instead of placing siblings beside each other", () => {
  const nodes = [
    { id: "ast:1", nodeID: 1, definition: { parentId: 0 } },
    { id: "ast:2", nodeID: 2, definition: { parentId: 1 } },
    { id: "ast:3", nodeID: 3, definition: { parentId: 2 } },
    { id: "ast:4", nodeID: 4, definition: { parentId: 2 } },
    { id: "ast:5", nodeID: 5, definition: { parentId: 1 } },
    { id: "ast:6", nodeID: 6, definition: { parentId: 0 } },
  ];
  const edges = [
    { from: "ast:1", to: "ast:2", structural: true },
    { from: "ast:2", to: "ast:3", structural: true },
    { from: "ast:2", to: "ast:4", structural: true },
    { from: "ast:1", to: "ast:5", structural: true },
    { from: "ast:5", to: "ast:6", structural: false },
  ];

  const layout = layoutASTFlowGraph(nodes, edges, 1000, 800);
  const positions = nodes.map((node) => layout.positions.get(node.id));

  assert.deepEqual(positions.map((position) => position.y), [...positions.map((position) => position.y)].sort((left, right) => left - right));
  assert.ok(positions[1].x > positions[0].x);
  assert.ok(positions[2].x > positions[1].x);
  assert.equal(positions[4].x, positions[1].x);
  assert.ok(positions[4].y > positions[3].y);
});

test("layoutASTFlowGraph leaves enough margin to center its first and last nodes", () => {
  const nodes = [
    { id: "ast:1", nodeID: 1, definition: { parentId: 0 } },
    { id: "ast:2", nodeID: 2, definition: { parentId: 0 } },
  ];
  const layout = layoutASTFlowGraph(nodes, [{ from: "ast:1", to: "ast:2", structural: false }], 1000, 800);
  const first = layout.positions.get("ast:1");
  const last = layout.positions.get("ast:2");

  assert.equal(first.x, (1000 - layout.nodeWidth) / 2);
  assert.equal(first.y, (800 - layout.nodeHeight) / 2);
  assert.equal(layout.height - last.y - layout.nodeHeight, first.y);
});

test("buildFlowModel keeps known unexecuted syntax dashed", () => {
  const model = buildFlowModel({
    nodes: [
      node(1, 0, "condition", "if true"),
      node(2, 1, "command", "echo yes"),
      node(3, 1, "command", "echo no"),
    ],
    events: [
      event(4, "statement_started", 1, 1),
      event(5, "statement_started", 2, 1),
      event(6, "statement_finished", 2, 1),
      event(7, "statement_finished", 1, 1),
    ],
  });

  const skipped = model.nodes.find((item) => item.nodeID === 3);
  assert.equal(skipped.state, "not-executed");
  assert.equal(skipped.executed, false);
  assert.equal(model.edges.find((edge) => edge.to === skipped.id).state, "not-executed");
});

test("buildFlowModel preserves both actually explored unresolved paths", () => {
  const model = buildFlowModel({
    nodes: [
      node(1, 0, "condition", "if unknown"),
      node(2, 1, "command", "echo yes"),
      node(3, 1, "command", "echo no"),
    ],
    events: [
      event(2, "statement_started", 1, 1),
      { sequence: 3, kind: "path_forked", nodeId: 1, pathId: 1, childPathIds: [2, 3] },
      event(4, "statement_started", 2, 2),
      event(5, "statement_started", 3, 3),
    ],
  });

  const branches = model.nodes.filter((item) => item.nodeID === 2 || item.nodeID === 3);
  const parent = model.nodes.find((item) => item.nodeID === 1);
  assert.deepEqual(branches.map((item) => item.pathID).sort(), [2, 3]);
  assert.ok(branches.every((item) => item.state === "executed"));
  assert.equal(parent.state, "unresolved");
  assert.ok(model.edges.filter((edge) => edge.state === "unresolved").length >= 2);
});

test("buildFlowModel aggregates repeated loop executions on one syntax node", () => {
  const model = buildFlowModel({
    nodes: [node(1, 0, "command", "record")],
    events: [
      event(3, "statement_started", 1, 1),
      event(4, "statement_finished", 1, 1),
      event(8, "statement_started", 1, 1),
      event(9, "statement_finished", 1, 1),
    ],
  });

  const occurrences = model.nodes.filter((item) => item.nodeID === 1);
  assert.equal(occurrences.length, 1);
  assert.equal(occurrences[0].executionCount, 2);
});

test("buildFlowModel attaches command details and final memory to its occurrence", () => {
  const model = buildFlowModel({
    nodes: [node(1, 0, "command", "record value")],
    events: [
      { ...event(2, "statement_started", 1, 1), memory: { stateBytes: 10 } },
      {
        sequence: 3,
        kind: "command_started",
        nodeId: 1,
        pathId: 1,
        invocation: { name: "record", args: [{ kind: 0, value: "value" }] },
      },
      {
        sequence: 4,
        kind: "command_finished",
        nodeId: 1,
        pathId: 1,
        commandResult: { exitCode: 7, stdoutBytes: 4 },
      },
      { ...event(5, "statement_finished", 1, 1), memory: { stateBytes: 22 } },
    ],
  });

  assert.equal(model.nodes[0].invocation.name, "record");
  assert.equal(model.nodes[0].commandResult.exitCode, 7);
  assert.equal(model.nodes[0].memory.stateBytes, 22);
});

test("buildFlowModel attaches input and output state snapshots to an occurrence", () => {
  const input = {
    directory: "/",
    stdin: "line\n",
    variables: [
      { name: "OPTIND", value: "1" },
      { name: "TOKEN", value: "secret", exported: true },
    ],
  };
  const output = { directory: "/", stdout: "done", exitCode: 0 };
  const model = buildFlowModel({
    nodes: [node(1, 0, "command", "printf done")],
    events: [
      { ...event(2, "statement_started", 1, 1), snapshot: input },
      { ...event(3, "statement_finished", 1, 1), snapshot: output },
    ],
  });

  assert.deepEqual(model.nodes[0].inputSnapshot, {
    directory: "/",
    stdin: "line\n",
    variables: [{ name: "TOKEN", value: "secret", exported: true }],
  });
  assert.deepEqual(model.nodes[0].outputSnapshot, output);
});

test("nodeOutput uses the current command result instead of inherited stream uncertainty", () => {
  assert.deepEqual(nodeOutput({
    inputSnapshot: {
      stdout: "",
      stdoutUnresolved: true,
      stderr: "",
      stderrUnresolved: true,
    },
    outputSnapshot: {
      stdout: "simulation complete\n",
      stdoutUnresolved: true,
      stderr: "",
      stderrUnresolved: true,
      exitCode: 0,
    },
    commandResult: {
      outputCaptured: true,
      stdout: "simulation complete\n",
      stderr: "",
      exitCode: 0,
      stdoutUnresolved: false,
      stderrUnresolved: false,
      exitCodeUnresolved: false,
    },
  }), {
    stdout: "simulation complete\n",
    stdoutUnresolved: false,
    stderr: "",
    stderrUnresolved: false,
    exitCode: 0,
    exitCodeUnresolved: false,
    error: "",
    truncated: false,
  });
});

test("nodeOutput does not inherit unresolved state from an unchanged stream", () => {
  assert.deepEqual(nodeOutput({
    inputSnapshot: {
      stdout: "before",
      stdoutUnresolved: true,
      stderr: "",
      stderrUnresolved: true,
    },
    outputSnapshot: {
      stdout: "beforenode",
      stdoutUnresolved: true,
      stderr: "",
      stderrUnresolved: true,
      exitCode: 0,
    },
  }), {
    stdout: "node",
    stdoutUnresolved: true,
    stderr: "",
    stderrUnresolved: false,
    exitCode: 0,
    exitCodeUnresolved: false,
    error: "",
    truncated: false,
  });
});

test("normalizeCommands validates structured per-command outcomes", () => {
  assert.deepEqual(normalizeCommands([
    { name: " lark-cli ", outcome: "resolved", stdout: "sent\n", stderr: "", exitCode: "7" },
    { name: "broken", outcome: "error", error: "handler failed" },
    { name: "dynamic", outcome: "javascript", javascript: "return { exitCode: 3 };" },
  ]), [
    { name: "lark-cli", stdout: "sent\n", stderr: "", exitCode: 7 },
    { name: "broken", error: "handler failed" },
    { name: "dynamic", javascript: "return { exitCode: 3 };" },
  ]);
  assert.throws(() => normalizeCommands([
    { name: "same", outcome: "resolved", exitCode: 0 },
    { name: "same", outcome: "error", error: "failed" },
  ]), /registered more than once/);
  assert.throws(() => normalizeCommands([
    { name: "broken", outcome: "error", error: "" },
  ]), /error message/);
  assert.throws(() => normalizeCommands([
    { name: "legacy", outcome: "unresolved" },
  ]), /unsupported outcome/);
  assert.throws(() => normalizeCommands([
    { name: "empty-code", outcome: "javascript", javascript: "  " },
  ]), /JavaScript handler source/);
});

test("parseEnvironment accepts comments and values containing equals", () => {
  assert.deepEqual(parseEnvironment("# initial values\nTOKEN=a=b\nEMPTY="), {
    TOKEN: "a=b",
    EMPTY: "",
  });
  assert.throws(() => parseEnvironment("bad line"), /KEY=value/);
});

test("formatBytes uses stable binary units", () => {
  assert.equal(formatBytes(0), "0 B");
  assert.equal(formatBytes(1023), "1023 B");
  assert.equal(formatBytes(1536), "1.5 KiB");
  assert.equal(formatBytes(2 << 20), "2 MiB");
});

test("tokenizeBash classifies common shell syntax without changing source", () => {
  const source = `CHAT_ID="oc_demo"
if [[ -n "$CHAT_ID" ]]; then
  lark-cli send --count 30 # observed call
fi`;
  const tokens = tokenizeBash(source);

  assert.equal(tokens.map((token) => token.value).join(""), source);
  assert.deepEqual(valuesOf(tokens, "assignment"), ["CHAT_ID="]);
  assert.deepEqual(valuesOf(tokens, "keyword"), ["if", "[[", "]]", "then", "fi"]);
  assert.deepEqual(valuesOf(tokens, "command"), ["lark-cli"]);
  assert.deepEqual(valuesOf(tokens, "variable"), ["$CHAT_ID"]);
  assert.deepEqual(valuesOf(tokens, "number"), ["30"]);
  assert.deepEqual(valuesOf(tokens, "comment"), ["# observed call"]);
});

test("tokenizeBash keeps comment markers and keywords inside strings literal", () => {
  const source = `echo 'if # literal' "value=$HOME" foo#bar`;
  const tokens = tokenizeBash(source);

  assert.equal(tokens.map((token) => token.value).join(""), source);
  assert.deepEqual(valuesOf(tokens, "command"), ["echo"]);
  assert.deepEqual(valuesOf(tokens, "keyword"), []);
  assert.deepEqual(valuesOf(tokens, "comment"), []);
  assert.deepEqual(valuesOf(tokens, "variable"), ["$HOME"]);
});

test("tokenizeBash keeps an unquoted assignment before its command", () => {
  const tokens = tokenizeBash(`REGION=cn deploy "$REGION"`);

  assert.deepEqual(valuesOf(tokens, "assignment"), ["REGION=cn"]);
  assert.deepEqual(valuesOf(tokens, "command"), ["deploy"]);
  assert.deepEqual(valuesOf(tokens, "variable"), ["$REGION"]);
});

function node(id, parentId, kind, snippet) {
  return {
    id,
    parentId,
    kind,
    snippet,
    source: { name: "command.sh", line: id, column: 1, endLine: id, endColumn: 10 },
  };
}

function event(sequence, kind, nodeId, pathId) {
  return { sequence, kind, nodeId, pathId };
}

function commandStarted(sequence, nodeId, name, args = [], pathId = 1) {
  return {
    sequence,
    kind: "command_started",
    nodeId,
    pathId,
    invocation: { name, args },
  };
}

function commandFinished(sequence, nodeId, commandResult = { exitCode: 0 }, pathId = 1) {
  return {
    sequence,
    kind: "command_finished",
    nodeId,
    pathId,
    commandResult,
  };
}

function valuesOf(tokens, kind) {
  return tokens.filter((token) => token.kind === kind).map((token) => token.value);
}

function edgePairs(model) {
  return model.edges.map((edge) => [edge.from, edge.to]);
}
