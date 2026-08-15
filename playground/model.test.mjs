import test from "node:test";
import assert from "node:assert/strict";
import * as playgroundModel from "./model.js";

import {
  advanceRuntimeFlow,
  buildFlowModel,
  buildFlowModels,
  createASTFlowModel,
  createRuntimeFlowModel,
  flowScrollTarget,
  formatBytes,
  layoutASTFlowGraph,
  layoutFlowGraph,
  liveControlView,
  nodeOutput,
  normalizeCommands,
  executionOccurrenceLabel,
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

test("runtime flow grows when control and command events arrive", () => {
  const runtime = createRuntimeFlowModel();
  runtime.append({ kind: "node_discovered", node: node(1, 0, "loop", "for item in values") });
  runtime.append(event(1, "statement_started", 1, 1));
  runtime.append({ kind: "node_discovered", node: node(2, 0, "command", "echo live") });
  runtime.append(commandStarted(2, 2, "echo", [{ kind: 0, value: "live" }]));

  assert.deepEqual(runtime.model.nodes.map((item) => item.definition.snippet), ["for item in values", "echo live"]);
  assert.equal(runtime.model.nodes[1].commandResult, null);

  runtime.append(commandFinished(3, 2, { exitCode: 0, stdout: "live\n", outputCaptured: true }));

  assert.equal(runtime.model.nodes.length, 2);
  assert.equal(runtime.model.nodes[1].commandResult.stdout, "live\n");
  assert.equal(runtime.model.maximumSequence, 3);
});

test("runtime flow records every loop and condition re-entry in execution order", () => {
  const runtime = createRuntimeFlowModel([
    node(1, 0, "loop", "for i in one two three"),
    node(2, 1, "condition", "if (( i == 1 ))"),
    node(3, 1, "command", "lark-cli send"),
  ]);
  const events = [
    event(1, "statement_started", 1, 1),
    event(2, "statement_started", 2, 1),
    event(3, "statement_finished", 2, 1),
    commandStarted(4, 3, "lark-cli", [{ kind: 0, value: "send" }]),
    commandFinished(5, 3),
    event(6, "statement_activated", 1, 1),
    event(7, "statement_started", 2, 1),
    event(8, "statement_finished", 2, 1),
    commandStarted(9, 3, "lark-cli", [{ kind: 0, value: "send" }]),
    commandFinished(10, 3),
  ];
  for (const current of events) runtime.append(current);

  assert.deepEqual(runtime.model.nodes.map((item) => item.definition.kind), [
    "loop", "condition", "command", "loop", "condition", "command",
  ]);
  assert.deepEqual(runtime.model.nodes.map((item) => item.definition.snippet), [
    "for i in one two three", "if (( i == 1 ))", "lark-cli send",
    "for i in one two three", "if (( i == 1 ))", "lark-cli send",
  ]);
  assert.deepEqual(runtime.model.edges.map((edge) => [edge.from, edge.to]), [
    ["runtime:1", "runtime:2"],
    ["runtime:2", "runtime:4"],
    ["runtime:4", "runtime:6"],
    ["runtime:6", "runtime:7"],
    ["runtime:7", "runtime:9"],
  ]);
});

test("advanceRuntimeFlow reveals at most one runtime node per visual step", () => {
  const runtime = createRuntimeFlowModel();
  const events = [
    { kind: "node_discovered", node: node(1, 0, "loop", "while ready") },
    event(1, "statement_started", 1, 1),
    commandStarted(2, 2, "first"),
    commandFinished(3, 2),
    commandStarted(4, 3, "second"),
    commandFinished(5, 3),
  ];

  const first = advanceRuntimeFlow(runtime, events, 0);
  assert.deepEqual(first, { next: 2, changed: true, revealed: true });
  assert.deepEqual(runtime.model.nodes.map((item) => item.definition.kind), ["loop"]);

  const second = advanceRuntimeFlow(runtime, events, first.next);
  assert.deepEqual(second, { next: 3, changed: true, revealed: true });
  assert.deepEqual(runtime.model.nodes.map((item) => item.definition.kind), ["loop", "command"]);

  const third = advanceRuntimeFlow(runtime, events, second.next);
  assert.deepEqual(third, { next: 5, changed: true, revealed: true });
  assert.deepEqual(runtime.model.nodes.map((item) => item.definition.kind), ["loop", "command", "command"]);

  const finished = advanceRuntimeFlow(runtime, events, third.next);
  assert.deepEqual(finished, { next: 6, changed: true, revealed: false });
});

test("buildFlowModels separates complete AST syntax from actual runtime execution", () => {
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
  assert.deepEqual(models.runtime.nodes.map((item) => item.definition.kind), ["condition", "command"]);
  assert.equal(models.runtime.nodes[1].invocation.name, "echo");
});

test("AST folds embedded evaluation details while runtime keeps executed commands", () => {
  const models = buildFlowModels({
    nodes: [
      { ...node(1, 0, "loop", "for i in $(seq 1 3)"), flowCanSkip: true },
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
  assert.deepEqual(models.runtime.nodes.map((item) => item.definition.kind), ["loop", "command", "condition", "command"]);
  assert.deepEqual(models.runtime.nodes.filter((item) => item.invocation).map((item) => item.invocation.name), ["seq", "lark-cli"]);
});

test("AST connects visible syntax through an embedded structural parent", () => {
  const model = buildFlowModel({
    nodes: [
      node(1, 0, "operator", "producer | { consumer; }"),
      { ...node(2, 1, "block", "{ consumer; }"), embedded: true, flowGroup: 1 },
      node(3, 2, "command", "consumer"),
    ],
  });

  assert.deepEqual(model.nodes.map((item) => item.nodeID), [1, 3]);
  assert.ok(model.edges.some((edge) => edge.from === "ast:1" && edge.to === "ast:3" && edge.flowGroup === 1));
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

test("AST control flow rejoins every alternative before the following statement", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "loop", "for i in $(seq 1 3)"), flowCanSkip: true },
      node(2, 1, "condition", "if (( i == 1 ))"),
      { ...node(3, 2, "command", "text=first"), flowGroup: 0 },
      { ...node(4, 2, "command", "text=other"), flowGroup: 1 },
      node(5, 1, "command", "lark-cli send"),
      node(6, 0, "command", "echo done"),
    ],
  });

  assert.deepEqual(edgePairs(model), [
    ["ast:1", "ast:2"],
    ["ast:2", "ast:3"],
    ["ast:2", "ast:4"],
    ["ast:3", "ast:5"],
    ["ast:4", "ast:5"],
    ["ast:5", "ast:6"],
  ]);
});

test("AST control flow keeps statements sequential inside each alternative", () => {
  const model = buildFlowModel({
    nodes: [
      node(1, 0, "condition", "case $mode in"),
      { ...node(2, 1, "command", "prepare inline"), flowGroup: 0 },
      { ...node(3, 1, "command", "send inline"), flowGroup: 0 },
      { ...node(4, 1, "command", "send staged"), flowGroup: 1 },
      node(5, 0, "command", "echo done"),
    ],
  });

  assert.deepEqual(edgePairs(model), [
    ["ast:1", "ast:2"],
    ["ast:1", "ast:4"],
    ["ast:2", "ast:3"],
    ["ast:3", "ast:5"],
    ["ast:4", "ast:5"],
  ]);
});

test("AST function bodies are entered by calls instead of declarations", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "function", "f() { body; }"), flowFunction: "f" },
      { ...node(2, 1, "command", "body"), flowCommand: "body" },
      { ...node(3, 0, "command", "after"), flowCommand: "after" },
      { ...node(4, 0, "command", "f"), flowCommand: "f" },
      { ...node(5, 0, "command", "done"), flowCommand: "done" },
    ],
  });

  assert.deepEqual(edgePairs(model), [
    ["ast:1", "ast:3"],
    ["ast:2", "ast:5"],
    ["ast:3", "ast:4"],
    ["ast:4", "ast:2"],
  ]);
});

test("AST uses one static forward sequence for every loop form", () => {
  for (const snippet of [
    "for item in values",
    "for ((i=0; i<3; i++))",
    "while condition",
    "until condition",
    "select item in values",
  ]) {
    const model = buildFlowModel({
      nodes: [
        { ...node(1, 0, "loop", snippet), flowCanSkip: true },
        { ...node(2, 1, "command", "body"), flowCommand: "body" },
        { ...node(3, 0, "command", "after"), flowCommand: "after" },
      ],
    });

    assert.deepEqual(edgePairs(model), [
      ["ast:1", "ast:2"],
      ["ast:2", "ast:3"],
    ], snippet);
  }
});

test("AST loop syntax remains forward-only regardless of runtime reachability", () => {
  const withoutBreak = buildFlowModel({
    nodes: [
      { ...node(1, 0, "loop", "for ((;;))"), flowCanSkip: false },
      node(2, 1, "command", "body"),
      node(3, 0, "command", "after"),
    ],
  });
  assert.deepEqual(edgePairs(withoutBreak), [
    ["ast:1", "ast:2"],
    ["ast:2", "ast:3"],
  ]);

  const withBreak = buildFlowModel({
    nodes: [
      { ...node(1, 0, "loop", "for ((;;))"), flowCanSkip: false },
      { ...node(2, 1, "command", "break"), flowCommand: "break" },
      node(3, 0, "command", "after"),
    ],
  });
  assert.deepEqual(edgePairs(withBreak), [
    ["ast:1", "ast:2"],
    ["ast:2", "ast:3"],
  ]);
});

test("runtime-only transitions never alter AST topology", () => {
  const ast = createASTFlowModel([
    { ...node(1, 0, "loop", "for item in values"), flowCanSkip: true },
    node(2, 1, "command", "body"),
    node(3, 0, "command", "after"),
  ]);

  ast.append(event(1, "statement_started", 1, 1));
  ast.append(event(2, "statement_started", 2, 1));
  ast.append(event(3, "statement_finished", 2, 1));
  ast.append({ ...event(4, "statement_finished", 1, 1), status: 0 });
  ast.append(event(5, "statement_started", 3, 1));

  assert.deepEqual(edgePairs(ast.model), [
    ["ast:1", "ast:2"],
    ["ast:2", "ast:3"],
  ]);
  assert.equal(ast.model.edges.some((edge) => edge.from === "ast:2" && edge.to === "ast:1"), false);
  assert.equal(ast.model.edges.some((edge) => edge.from === "ast:1" && edge.to === "ast:3"), false);
});

test("AST break leaves a loop without a synthetic return to its header", () => {
  const ast = createASTFlowModel([
    { ...node(1, 0, "loop", "while true"), flowCanSkip: true },
    { ...node(2, 1, "command", "break"), flowCommand: "break" },
    node(3, 0, "command", "after"),
  ]);

  ast.append(event(1, "statement_started", 1, 1));
  ast.append(event(2, "statement_started", 2, 1));
  ast.append(event(3, "statement_finished", 2, 1));
  ast.append({ ...event(4, "statement_finished", 1, 1), status: 0 });
  ast.append(event(5, "statement_started", 3, 1));

  assert.equal(edgeState(ast.model, "ast:2", "ast:3"), "executed");
  assert.equal(ast.model.edges.some((edge) => edge.from === "ast:2" && edge.to === "ast:1"), false);
});

test("AST uses an executed wrapper's control command when leaving a loop", () => {
  const ast = createASTFlowModel([
    { ...node(1, 0, "loop", "while true"), flowCanSkip: true },
    { ...node(2, 1, "command", "command break"), flowCommand: "command" },
    node(3, 0, "command", "after"),
  ]);

  ast.append(event(1, "statement_started", 1, 1));
  ast.append(event(2, "statement_started", 2, 1));
  ast.append(commandStarted(3, 2, "break"));
  ast.append(commandFinished(4, 2));
  ast.append(event(5, "statement_finished", 2, 1));
  ast.append({ ...event(6, "statement_finished", 1, 1), status: 0 });
  ast.append(event(7, "statement_started", 3, 1));

  assert.equal(edgeState(ast.model, "ast:2", "ast:3"), "executed");
  assert.equal(ast.model.edges.some((edge) => edge.from === "ast:2" && edge.to === "ast:1"), false);
});

test("AST case fallthrough enters the next body and an exhaustive default removes no-match flow", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "condition", "case value in"), flowCanSkip: false },
      { ...node(2, 1, "command", "first"), flowGroup: 0, flowGroupExit: ";&" },
      { ...node(3, 1, "command", "fallback"), flowGroup: 1, flowGroupExit: ";;", flowGroupDefault: true },
      node(4, 0, "command", "after"),
    ],
  });

  assert.deepEqual(edgePairs(model), [
    ["ast:1", "ast:2"],
    ["ast:1", "ast:3"],
    ["ast:2", "ast:3"],
    ["ast:3", "ast:4"],
  ]);
});

test("AST case resume can test every later item but cannot bypass a later default", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "condition", "case value in"), flowCanSkip: false },
      { ...node(2, 1, "command", "first"), flowGroup: 0, flowGroupExit: ";;&" },
      { ...node(3, 1, "command", "second"), flowGroup: 1, flowGroupExit: ";;" },
      { ...node(4, 1, "command", "fallback"), flowGroup: 2, flowGroupExit: ";;", flowGroupDefault: true },
      node(5, 0, "command", "after"),
    ],
  });

  assert.deepEqual(edgePairs(model), [
    ["ast:1", "ast:2"],
    ["ast:1", "ast:3"],
    ["ast:1", "ast:4"],
    ["ast:2", "ast:3"],
    ["ast:2", "ast:4"],
    ["ast:3", "ast:5"],
    ["ast:4", "ast:5"],
  ]);
});

test("AST control transfers do not connect to unreachable sequential statements", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "command", "exit 7"), flowCommand: "exit" },
      node(2, 0, "command", "never"),
    ],
  });

  assert.deepEqual(edgePairs(model), []);
});

test("AST invalid top-level loop and return controls continue after their Bash error", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "command", "break"), flowCommand: "break" },
      { ...node(2, 0, "command", "continue"), flowCommand: "continue" },
      { ...node(3, 0, "command", "return"), flowCommand: "return" },
      node(4, 0, "command", "after"),
    ],
  });

  assert.deepEqual(edgePairs(model), [
    ["ast:1", "ast:2"],
    ["ast:2", "ast:3"],
    ["ast:3", "ast:4"],
  ]);
});

test("AST loop controls preserve the static forward sequence", () => {
  const breaking = buildFlowModel({
    nodes: [
      { ...node(1, 0, "loop", "while condition"), flowCanSkip: true },
      { ...node(2, 1, "command", "break"), flowCommand: "break" },
      node(3, 1, "command", "never"),
      node(4, 0, "command", "after"),
    ],
  });
  assert.deepEqual(edgePairs(breaking), [
    ["ast:1", "ast:2"],
    ["ast:2", "ast:4"],
  ]);

  const continuing = buildFlowModel({
    nodes: [
      { ...node(1, 0, "loop", "while condition"), flowCanSkip: true },
      { ...node(2, 1, "command", "continue"), flowCommand: "continue" },
      node(3, 1, "command", "never"),
      node(4, 0, "command", "after"),
    ],
  });
  assert.deepEqual(edgePairs(continuing), [
    ["ast:1", "ast:2"],
    ["ast:2", "ast:4"],
  ]);
});

test("AST function return rejoins the caller and skips the rest of the body", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "function", "f() { return; never; }"), flowFunction: "f" },
      { ...node(2, 1, "command", "return"), flowCommand: "return" },
      node(3, 1, "command", "never"),
      { ...node(4, 0, "command", "f"), flowCommand: "f" },
      node(5, 0, "command", "after"),
    ],
  });

  assert.deepEqual(edgePairs(model), [
    ["ast:1", "ast:4"],
    ["ast:2", "ast:5"],
    ["ast:4", "ast:2"],
  ]);
});

test("AST merge edges remain dashed when their source alternative was not executed", () => {
  const model = buildFlowModel({
    nodes: [
      node(1, 0, "condition", "if enabled"),
      { ...node(2, 1, "command", "text=enabled"), flowGroup: 0 },
      { ...node(3, 1, "command", "text=disabled"), flowGroup: 1 },
      node(4, 0, "command", "lark-cli send"),
    ],
    events: [
      event(1, "statement_started", 1, 1),
      event(2, "statement_started", 2, 1),
      event(3, "statement_finished", 2, 1),
      event(4, "statement_started", 4, 1),
      event(5, "statement_finished", 4, 1),
      event(6, "statement_finished", 1, 1),
    ],
  });

  assert.equal(edgeState(model, "ast:2", "ast:4"), "executed");
  assert.equal(edgeState(model, "ast:3", "ast:4"), "not-executed");
});

test("AST edges use observed transitions when different loop iterations take different branches", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "condition", "if enabled"), flowCanSkip: true },
      node(2, 1, "command", "body"),
      node(3, 0, "command", "after"),
    ],
    events: [
      event(1, "statement_started", 1, 1),
      event(2, "statement_started", 2, 1),
      event(3, "statement_finished", 2, 1),
      event(4, "statement_started", 3, 1),
      event(5, "statement_finished", 3, 1),
      event(6, "statement_started", 1, 1),
      event(7, "statement_started", 3, 1),
    ],
  });

  assert.equal(edgeState(model, "ast:1", "ast:2"), "executed");
  assert.equal(edgeState(model, "ast:2", "ast:3"), "executed");
  assert.equal(edgeState(model, "ast:1", "ast:3"), "executed");
});

test("AST conditions without an else retain their direct fallthrough edge", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "condition", "if enabled"), flowCanSkip: true },
      node(2, 1, "command", "lark-cli log"),
      node(3, 0, "loop", "for item in values"),
    ],
    events: [
      event(1, "statement_started", 1, 1),
      event(2, "statement_started", 3, 1),
      event(3, "statement_finished", 3, 1),
      event(4, "statement_finished", 1, 1),
    ],
  });

  assert.deepEqual(edgePairs(model), [
    ["ast:1", "ast:2"],
    ["ast:1", "ast:3"],
    ["ast:2", "ast:3"],
  ]);
  assert.equal(edgeState(model, "ast:2", "ast:3"), "not-executed");
  assert.equal(edgeState(model, "ast:1", "ast:3"), "executed");
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

test("AST live playback activates a repeated loop node on every execution", () => {
  const ast = createASTFlowModel([node(1, 0, "condition", "if (( i == 1 ))")]);

  const first = ast.append(event(1, "statement_started", 1, 1));
  ast.append(event(2, "statement_finished", 1, 1));
  const second = ast.append(event(3, "statement_started", 1, 1));

  assert.equal(first.activated, true);
  assert.equal(first.revealed, true);
  assert.equal(second.activated, true);
  assert.equal(second.revealed, false);
  assert.equal(ast.model.nodes[0].executionCount, 2);
});

test("AST live playback activates the loop header when control re-enters it", () => {
  const ast = createASTFlowModel([
      { ...node(1, 0, "loop", "for item in values"), flowCanSkip: true },
    node(2, 1, "command", "body"),
  ]);

  ast.append(event(1, "statement_started", 1, 1));
  ast.append(event(2, "statement_started", 2, 1));
  const reentered = ast.append(event(3, "statement_activated", 1, 1));

  assert.equal(reentered.activated, true);
  assert.equal(ast.model.nodes.find((item) => item.nodeID === 1).executionCount, 2);
  assert.equal(ast.model.edges.some((edge) => edge.from === "ast:2" && edge.to === "ast:1"), false);
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

test("layoutASTFlowGraph aligns alternative branches and advances after their subtree", () => {
  const nodes = [
    { id: "ast:1", nodeID: 1, definition: { parentId: 0 } },
    { id: "ast:2", nodeID: 2, definition: { parentId: 0 } },
    { id: "ast:3", nodeID: 3, definition: { parentId: 2, flowGroup: 0 } },
    { id: "ast:4", nodeID: 4, definition: { parentId: 0 } },
    { id: "ast:5", nodeID: 5, definition: { parentId: 4, flowGroup: 0 } },
    { id: "ast:6", nodeID: 6, definition: { parentId: 5, flowGroup: 0 } },
    { id: "ast:7", nodeID: 7, definition: { parentId: 5, flowGroup: 1 } },
    { id: "ast:8", nodeID: 8, definition: { parentId: 4, flowGroup: 0 } },
    { id: "ast:9", nodeID: 9, definition: { parentId: 0 } },
  ];
  const edges = [
    { from: "ast:1", to: "ast:2", structural: false },
    { from: "ast:2", to: "ast:3", structural: true, flowGroup: 0 },
    { from: "ast:3", to: "ast:4", structural: false },
    { from: "ast:4", to: "ast:5", structural: true, flowGroup: 0 },
    { from: "ast:5", to: "ast:6", structural: true, flowGroup: 0 },
    { from: "ast:5", to: "ast:7", structural: true, flowGroup: 1 },
    { from: "ast:4", to: "ast:8", structural: true, flowGroup: 0 },
    { from: "ast:8", to: "ast:9", structural: false },
  ];

  const layout = layoutASTFlowGraph(nodes, edges, 1000, 800);
  const positions = nodes.map((node) => layout.positions.get(node.id));

  assert.equal(positions[5].y, positions[6].y);
  assert.notEqual(positions[5].x, positions[6].x);
  assert.ok(positions[7].y > positions[5].y);
  assert.ok(positions[8].y > positions[7].y);
  assert.equal(positions[0].x, positions[1].x);
  assert.equal(positions[1].x, positions[3].x);
  assert.equal(positions[3].x, positions[8].x);
});

test("layoutASTFlowGraph keeps a skippable condition on the main line and moves its body right", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "condition", "if missing"), flowCanSkip: true },
      node(2, 1, "command", "lark-cli log"),
      node(3, 0, "loop", "for item in values"),
    ],
  });
  const layout = layoutASTFlowGraph(model.nodes, model.edges, 1000, 800);
  const condition = layout.positions.get("ast:1");
  const optional = layout.positions.get("ast:2");
  const following = layout.positions.get("ast:3");

  assert.equal(condition.x, following.x);
  assert.ok(optional.x > condition.x);
  const fallthrough = model.edges.find((edge) => edge.from === "ast:1" && edge.to === "ast:3");
  assert.equal(layout.edgeRoutes.has(fallthrough.id), false);
  const center = condition.x + layout.nodeWidth / 2;
  const fromBottom = condition.y + layout.nodeHeight;
  const bend = Math.max(30, (following.y - fromBottom) * .5);
  assert.equal(
    playgroundModel.flowEdgePath(fallthrough, condition, following, layout),
    `M ${center} ${fromBottom} C ${center} ${fromBottom + bend}, ${center} ${following.y - bend}, ${center} ${following.y}`,
  );
});

test("layoutASTFlowGraph does not add runtime rails to static nested loops", () => {
  const model = buildFlowModel({
    nodes: [
      { ...node(1, 0, "loop", "outer"), flowCanSkip: true },
      { ...node(2, 1, "loop", "inner"), flowCanSkip: true },
      node(3, 2, "command", "body"),
      node(4, 0, "command", "after"),
    ],
  });
  const layout = layoutASTFlowGraph(model.nodes, model.edges, 1000, 800);

  assert.equal(layout.edgeRoutes.size, 0);
});

test("layoutASTFlowGraph handles deeply nested syntax without recursive stack growth", () => {
  const nodes = Array.from({ length: 2000 }, (_, index) => ({
    id: `ast:${index + 1}`,
    nodeID: index + 1,
    syntaxParentID: index ? `ast:${index}` : null,
    syntaxFlowGroup: 0,
    definition: { parentId: index, flowGroup: 0 },
  }));

  const layout = layoutASTFlowGraph(nodes, [], 1000, 800);
  assert.equal(layout.positions.size, nodes.length);
  assert.ok(layout.positions.get("ast:2000").y > layout.positions.get("ast:1").y);
});

test("flow layouts start at the top and center their first node horizontally", () => {
  const nodes = [
    { id: "ast:1", nodeID: 1, definition: { parentId: 0 } },
    { id: "ast:2", nodeID: 2, definition: { parentId: 0 } },
  ];
  const layout = layoutASTFlowGraph(nodes, [{ from: "ast:1", to: "ast:2", structural: false }], 1000, 800);
  const first = layout.positions.get("ast:1");
  const last = layout.positions.get("ast:2");

  assert.equal(first.x, (1000 - layout.nodeWidth) / 2);
  assert.equal(first.y, 40);
  assert.ok(last.y > first.y);

  const runtime = layoutFlowGraph([{ id: "runtime:1", pathID: 1 }], [], 1000, 800);
  assert.equal(runtime.positions.get("runtime:1").y, 40);
});

test("flowScrollTarget centers horizontally and follows vertically without centering", () => {
  const viewport = { width: 600, height: 500, scrollTop: 0 };
  const target = flowScrollTarget({ left: 700, top: 620, width: 200, height: 100 }, viewport);

  assert.equal(target.left, 500);
  assert.equal(target.top, 268);
  assert.notEqual(target.top + viewport.height / 2, 670);

  assert.deepEqual(flowScrollTarget({ left: 200, top: 180, width: 200, height: 100 }, {
    width: 600,
    height: 500,
    scrollTop: 100,
  }), { left: 0, top: 100 });
  assert.deepEqual(flowScrollTarget({ left: 700, top: 620, width: 200, height: 100 }, viewport, true), {
    left: 500,
    top: 0,
  });
});

test("flowEdgePath routes fallthrough edges around intervening node columns", () => {
  const layout = { nodeWidth: 196, nodeHeight: 94, width: 792, edgeRoutes: new Map() };
  const from = { x: 298, y: 178 };
  const to = { x: 298, y: 454 };

  assert.equal(
    playgroundModel.flowEdgePath?.({ fallthrough: false }, from, to, layout),
    "M 396 272 C 396 363, 396 363, 396 454",
  );
  assert.equal(
    playgroundModel.flowEdgePath?.({ id: "fallthrough", fallthrough: true }, from, to, {
      ...layout,
      edgeRoutes: new Map([["fallthrough", { railX: 776, backEdge: false }]]),
    }),
    "M 396 272 C 396 296, 776 296, 776 296 L 776 430 C 776 430, 396 430, 396 454",
  );
});

test("AST repeated execution context is explicitly identified as the latest occurrence", () => {
  assert.equal(executionOccurrenceLabel({ executionCount: 0 }), "Not executed");
  assert.equal(executionOccurrenceLabel({ executionCount: 1 }), "Execution 1 of 1");
  assert.equal(executionOccurrenceLabel({ executionCount: 3 }), "Latest execution (3 of 3)");
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
      { ...node(3, 1, "command", "echo no"), flowGroup: 1 },
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
  return model.edges
    .map((edge) => [edge.from, edge.to])
    .sort((left, right) => `${left[0]}:${left[1]}`.localeCompare(`${right[0]}:${right[1]}`));
}

function edgeState(model, from, to) {
  return model.edges.find((edge) => edge.from === from && edge.to === to)?.state;
}
