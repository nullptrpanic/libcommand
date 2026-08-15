import {
  buildFlowModel,
  buildFlowModels,
  concreteDisplayValue,
  createASTFlowModel,
  createRuntimeFlowModel,
  executionOccurrenceLabel,
  flowEdgePath,
  flowScrollTarget,
  formatBytes,
  layoutASTFlowGraph,
  layoutFlowGraph,
  liveControlView,
  nodeOutput,
  normalizeCommands,
  parseArguments,
  parseEnvironment,
  startupMode,
  tokenizeBash,
} from "./model.js";

const maximumRenderedNodes = 500;
const maximumHighlightedCharacters = 256 << 10;
const defaultJavaScriptHandler = `return {
  stdout: "",
  stderr: "",
  exitCode: 0,
};`;
const larkCLIJavaScriptHandler = `const args = invocation.args.map((argument) => argument.value);

function option(name) {
  const index = args.indexOf(name);
  return index >= 0 ? args[index + 1] : "";
}

return {
  stdout: JSON.stringify({
    command: invocation.name,
    chatId: option("--chat-id"),
    text: option("--text"),
    as: option("--as"),
  }, null, 2) + "\\n",
  stderr: "",
  exitCode: 0,
};`;
const examples = {
  messages: {
    commands: [
      { name: "lark-cli", outcome: "javascript", javascript: larkCLIJavaScriptHandler },
    ],
    source: `CHAT_ID="oc_28ebbd1168a2173f48bb23364a2d88fe"

if [[ -z "$CHAT_ID" ]]; then
  lark-cli log --text "missing chat id"
fi

for i in $(seq 1 3); do
  if (( i == 1 )); then
    text="评测任务已启动"
  else
    text="请各成员确认执行计划"
  fi
  lark-cli im +messages-send \
    --chat-id "$CHAT_ID" --text "$text" --as bot
done

echo "simulation complete"`,
    steps: 10000,
  },
  encoded: {
    commands: [{ name: "lark-cli", outcome: "resolved", stdout: "sent\n", stderr: "", exitCode: 0 }],
    source: `payload=$(printf '%s' 'lark-cli im messages-send --chat-id oc_demo --text decoded' | base64)
decoded=$(printf '%s' "$payload" | base64 --decode)

if [[ -n "$decoded" ]]; then
  eval "$decoded"
fi`,
    steps: 10000,
  },
  limits: {
    commands: [],
    source: `count=0
while true; do
  ((count++))
  printf -v snapshot 'iteration-%05d' "$count"
done

echo "unreachable"`,
    steps: 120,
  },
};

const elements = Object.fromEntries([
  "workspace", "example-select", "share-button", "stop-button", "run-button", "collapse-input", "expand-input",
  "input-resizer", "inspector-resizer", "add-command", "commands-list", "source-input", "environment-input", "arguments-input", "stdin-input",
  "steps-input", "memory-input", "timeout-input", "line-numbers", "line-total", "source-highlight", "flow-empty", "flow-scroll",
  "flow-view", "flow-canvas", "flow-edges", "flow-nodes", "outputs-view", "invocations-view", "diagnostics-view", "raw-output",
  "output-count", "invocation-count", "diagnostic-count", "inspector", "inspector-id", "status-title", "status-duration",
  "status-detail", "speed-input", "speed-value", "summary-calls",
  "summary-paths", "summary-memory", "zoom-out", "zoom-in", "zoom-reset", "zoom-value", "toast",
].map((id) => [id.replaceAll("-", "_"), document.getElementById(id)]));

let worker;
let workerReady = false;
let requestSequence = 0;
let pendingRequest = null;
let currentResponse = null;
let currentModels = null;
let currentModel = null;
let currentFlowPerspective = "runtime";
let selectedNodeID = "";
let activeNodeID = "";
let selectedInspectorTab = "overview";
let zoom = 1;
let recoveryMessage = "";
let sharedCodePendingReview = false;
let liveAST = null;
let liveRuntime = null;
let liveTraceEvents = [];
let liveTraceIndex = 0;
let liveInvocationCount = 0;
let liveFinalResponse = null;
let liveRevealTimer = 0;
let livePaused = false;
let astPreviewTimer = 0;
let astPreviewSequence = 0;
let pendingASTPreview = null;
let astPreviewDeferred = true;
let astPreviewSource = "";
let graphRelayoutFrame = 0;

wireControls();
const restoredSharedState = restoreSharedState();
if (!restoredSharedState) {
  applyExample("messages");
}
sharedCodePendingReview = startupMode(restoredSharedState, readCommandRows()) === "review";
updateLineNumbers();
currentModels = buildFlowModels({ nodes: [], events: [], invocations: [], outputs: [] });
currentModel = currentModels.runtime;
startWorker();

function wireControls() {
  document.querySelectorAll("[data-input-tab]").forEach((button) => {
    button.addEventListener("click", () => selectTab("input", button.dataset.inputTab));
  });
  document.querySelectorAll("[data-result-tab]").forEach((button) => {
    button.addEventListener("click", () => selectTab("result", button.dataset.resultTab));
  });
  document.querySelectorAll("[data-inspector-tab]").forEach((button) => {
    button.addEventListener("click", () => selectInspectorTab(button.dataset.inspectorTab));
  });
  document.querySelectorAll("[data-flow-perspective]").forEach((button) => {
    button.addEventListener("click", () => selectFlowPerspective(button.dataset.flowPerspective));
  });
  elements.example_select.addEventListener("change", () => applyExample(elements.example_select.value));
  elements.run_button.addEventListener("click", () => liveRuntime ? toggleLivePause() : runSimulation());
  elements.stop_button.addEventListener("click", stopLiveRun);
  elements.share_button.addEventListener("click", shareCurrentState);
  elements.source_input.addEventListener("input", () => {
    updateLineNumbers();
    scheduleASTPreview();
  });
  elements.source_input.addEventListener("scroll", syncEditorScroll);
  elements.source_input.addEventListener("keydown", handleEditorKeydown);
  document.addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
      event.preventDefault();
      if (!liveRuntime && workerReady) runSimulation();
    }
  });
  elements.collapse_input.addEventListener("click", () => setInputCollapsed(true));
  elements.expand_input.addEventListener("click", () => setInputCollapsed(false));
  elements.input_resizer.addEventListener("pointerdown", beginInputResize);
  elements.inspector_resizer.addEventListener("pointerdown", beginInspectorResize);
  elements.add_command.addEventListener("click", () => addCommandRow());
  elements.speed_input.addEventListener("input", () => {
    elements.speed_value.textContent = `${Number(elements.speed_input.value).toFixed(1)}×`;
    if (liveRevealTimer && !livePaused) scheduleLiveReveal();
  });
  elements.zoom_out.addEventListener("click", () => setZoom(zoom - .1));
  elements.zoom_in.addEventListener("click", () => setZoom(zoom + .1));
  elements.zoom_reset.addEventListener("click", () => setZoom(1));
  window.addEventListener("resize", updatePaneColumns);
}

function selectTab(group, name) {
  document.querySelectorAll(`[data-${group}-tab]`).forEach((button) => {
    button.classList.toggle("active", button.dataset[`${group}Tab`] === name);
  });
  document.querySelectorAll(`[data-${group}-panel]`).forEach((panel) => {
    panel.classList.toggle("active", panel.dataset[`${group}Panel`] === name);
  });
  if (group === "result") {
    document.querySelectorAll(".result-view").forEach((view) => view.classList.remove("active"));
    document.getElementById(`${name}-view`).classList.add("active");
    if (name === "flow") scheduleGraphRelayout();
  }
}

function applyExample(name) {
  const example = examples[name] || examples.messages;
  elements.example_select.value = name in examples ? name : "messages";
  renderCommandRows(example.commands);
  elements.source_input.value = example.source;
  elements.environment_input.value = "";
  elements.arguments_input.value = "";
  elements.stdin_input.value = "";
  elements.steps_input.value = example.steps;
  elements.memory_input.value = 2;
  updateLineNumbers();
  scheduleASTPreview();
}

function renderCommandRows(commands) {
  elements.commands_list.replaceChildren();
  for (const command of commands || []) addCommandRow(command);
}

function addCommandRow(command = {}) {
  const row = createElement("article", "command-row");
  const head = createElement("div", "command-row-head");
  const name = document.createElement("input");
  name.type = "text";
  name.placeholder = "command name or *";
  name.setAttribute("aria-label", "Command name");
  name.dataset.commandField = "name";
  name.value = command.name || "";
  const outcome = document.createElement("select");
  outcome.setAttribute("aria-label", "Command outcome");
  outcome.dataset.commandField = "outcome";
  for (const [value, label] of [["resolved", "Fixed result"], ["javascript", "JavaScript"], ["error", "Error"]]) {
    const option = document.createElement("option");
    option.value = value;
    option.textContent = label;
    outcome.append(option);
  }
  outcome.value = ["resolved", "javascript", "error"].includes(command.outcome) ? command.outcome : "resolved";
  const remove = createElement("button", "command-row-remove", "×");
  remove.type = "button";
  remove.title = "Remove command";
  remove.setAttribute("aria-label", "Remove command");
  remove.addEventListener("click", () => row.remove());
  head.append(name, outcome, remove);

  const resolved = createElement("div", "command-result-fields");
  resolved.append(
    commandTextField("Stdout", "stdout", command.stdout || ""),
    commandTextField("Stderr", "stderr", command.stderr || ""),
    commandNumberField("Exit code", "exitCode", command.exitCode ?? 0),
  );
  const error = createElement("label", "command-error-field", "Error message");
  const errorInput = document.createElement("input");
  errorInput.type = "text";
  errorInput.placeholder = "handler failed";
  errorInput.dataset.commandField = "error";
  errorInput.value = command.error || "";
  error.append(errorInput);

  const javascript = createElement("label", "command-javascript-field", "JavaScript handler");
  const javascriptInput = document.createElement("textarea");
  javascriptInput.spellcheck = false;
  javascriptInput.setAttribute("aria-label", "JavaScript handler");
  javascriptInput.dataset.commandField = "javascript";
  javascriptInput.value = command.javascript || defaultJavaScriptHandler;
  javascript.append(javascriptInput);

  const updateOutcome = () => { row.dataset.outcome = outcome.value; };
  outcome.addEventListener("change", updateOutcome);
  row.append(head, resolved, error, javascript);
  updateOutcome();
  elements.commands_list.append(row);
}

function commandTextField(label, field, value) {
  const container = createElement("label", "", label);
  const input = document.createElement("textarea");
  input.spellcheck = false;
  input.dataset.commandField = field;
  input.value = value;
  container.append(input);
  return container;
}

function commandNumberField(label, field, value) {
  const container = createElement("label", "", label);
  const input = document.createElement("input");
  input.type = "number";
  input.min = "0";
  input.max = "255";
  input.step = "1";
  input.dataset.commandField = field;
  input.value = String(value);
  container.append(input);
  return container;
}

function readCommandRows() {
  return [...elements.commands_list.querySelectorAll(".command-row")].map((row) => ({
    name: row.querySelector('[data-command-field="name"]').value,
    outcome: row.querySelector('[data-command-field="outcome"]').value,
    stdout: row.querySelector('[data-command-field="stdout"]').value,
    stderr: row.querySelector('[data-command-field="stderr"]').value,
    exitCode: row.querySelector('[data-command-field="exitCode"]').value,
    error: row.querySelector('[data-command-field="error"]').value,
    javascript: row.querySelector('[data-command-field="javascript"]').value,
  }));
}

function updateLineNumbers() {
  const lines = Math.max(1, elements.source_input.value.split("\n").length);
  elements.line_numbers.textContent = Array.from({ length: lines }, (_, index) => index + 1).join("\n");
  elements.line_total.textContent = String(lines);
  renderSourceHighlight();
}

function renderSourceHighlight() {
  const fragment = document.createDocumentFragment();
  const source = elements.source_input.value;
  const tokens = source.length <= maximumHighlightedCharacters ? tokenizeBash(source) : [{ kind: "plain", value: source }];
  for (const token of tokens) {
    if (token.kind === "plain") {
      fragment.append(document.createTextNode(token.value));
      continue;
    }
    fragment.append(createElement("span", `syntax-${token.kind}`, token.value));
  }
  // A trailing character keeps the overlay's final empty line aligned with
  // the textarea without changing the editable source.
  fragment.append(document.createTextNode("\u200b"));
  elements.source_highlight.replaceChildren(fragment);
  syncEditorScroll();
}

function syncEditorScroll() {
  elements.line_numbers.scrollTop = elements.source_input.scrollTop;
  elements.source_highlight.scrollTop = elements.source_input.scrollTop;
  elements.source_highlight.scrollLeft = elements.source_input.scrollLeft;
}

function handleEditorKeydown(event) {
  if (event.key !== "Tab") return;
  event.preventDefault();
  const { selectionStart, selectionEnd, value } = elements.source_input;
  elements.source_input.setRangeText("  ", selectionStart, selectionEnd, "end");
  if (selectionStart !== selectionEnd) elements.source_input.selectionEnd = selectionStart + 2;
  updateLineNumbers();
  scheduleASTPreview();
}

function startWorker() {
  workerReady = false;
  pendingASTPreview = null;
  astPreviewDeferred = true;
  updateLiveControls("Loading WASM");
  setStatus("Initializing WebAssembly", "The Bash source stays in this browser.", "—");
  worker = new Worker("worker.js");
  worker.addEventListener("message", handleWorkerMessage);
  worker.addEventListener("error", (event) => failWorker(event.message || "WebAssembly worker failed"));
}

function handleWorkerMessage({ data }) {
  if (data?.type === "ready") {
    workerReady = true;
    updateLiveControls();
    if (recoveryMessage) {
      setStatus("Simulation issue", recoveryMessage, "READY");
      recoveryMessage = "";
    } else if (sharedCodePendingReview) {
      setStatus("Review JavaScript handlers", "Shared handler code is loaded but has not run. Press Run when ready.", "READY");
    } else {
      setStatus("WebAssembly ready", "Press ⌘↵ or run to simulate locally.", "READY");
    }
    scheduleASTPreview(0);
    return;
  }
  if (data?.type === "error") {
    failWorker(data.error || "WebAssembly failed to initialize");
    return;
  }
  if (data?.type === "ast") {
    applyASTPreview(data);
    return;
  }
  if (!pendingRequest || data?.id !== pendingRequest.id) return;
  if (data?.type === "trace") {
    appendLiveTrace(data.event);
    return;
  }
  clearTimeout(pendingRequest.timeoutID);
  pendingRequest = null;
  if (data.error) {
    renderFailure(data.error);
    return;
  }
  finishLiveSimulation(data.result || {});
}

function scheduleASTPreview(delay = 180) {
  astPreviewDeferred = true;
  if (astPreviewTimer) clearTimeout(astPreviewTimer);
  astPreviewTimer = 0;
  if (!workerReady || liveRuntime) return;
  astPreviewTimer = window.setTimeout(requestASTPreview, delay);
}

function requestASTPreview() {
  astPreviewTimer = 0;
  if (!workerReady || liveRuntime) return;
  const source = elements.source_input.value;
  const id = ++astPreviewSequence;
  pendingASTPreview = { id, source };
  worker.postMessage({ id, type: "parse", request: { source } });
}

function applyASTPreview(data) {
  if (!pendingASTPreview || data?.id !== pendingASTPreview.id) return;
  const preview = pendingASTPreview;
  pendingASTPreview = null;
  if (preview.source !== elements.source_input.value) {
    scheduleASTPreview();
    return;
  }
  astPreviewDeferred = false;
  astPreviewSource = preview.source;
  const response = data.error ? { nodes: [], events: [], error: data.error } : (data.result || {});
  const ast = buildFlowModel(response);
  currentModels = {
    ast,
    runtime: currentModels?.runtime || buildFlowModels({ nodes: [], events: [] }).runtime,
  };
  if (currentFlowPerspective === "ast") {
    currentModel = ast;
    selectedNodeID = "";
    activeNodeID = "";
    renderGraph(ast);
  }
}

function appendLiveTrace(event) {
  if (!liveAST || !liveRuntime || !event) return;
  liveTraceEvents.push(event);
  if (!livePaused && !liveRevealTimer) advanceLiveFlow();
}

function advanceLiveFlow() {
  if (!liveAST || !liveRuntime || livePaused) return;
  let changed = false;
  let revealed = false;
  let revealedNode = null;
  while (liveTraceIndex < liveTraceEvents.length) {
    const event = liveTraceEvents[liveTraceIndex++];
    const runtimeNodeCount = liveRuntime.model.nodes.length;
    const astStep = liveAST.append(event);
    changed = liveRuntime.append(event) || astStep.changed || changed;
    if (event.kind === "command_started") liveInvocationCount++;
    if (currentFlowPerspective === "ast" && astStep.activated) {
      revealed = true;
      revealedNode = liveAST.model.nodes.find((node) => node.nodeID === event.nodeId) || null;
      break;
    }
    if (currentFlowPerspective === "runtime" && liveRuntime.model.nodes.length > runtimeNodeCount) {
      revealed = true;
      revealedNode = liveRuntime.model.nodes.at(-1);
      break;
    }
  }

  const model = currentFlowPerspective === "ast" ? liveAST.model : liveRuntime.model;
  if (revealedNode) {
    selectedNodeID = revealedNode.id;
    activeNodeID = revealedNode.id;
  }
  elements.summary_calls.textContent = String(liveInvocationCount);
  elements.summary_paths.textContent = String(Math.max(liveAST.model.pathCount, liveRuntime.model.pathCount));
  elements.summary_memory.textContent = formatBytes(Math.max(liveAST.model.peakLogicalBytes, liveRuntime.model.peakLogicalBytes));
  currentModel = model;
  if (changed) renderGraph(model, true);

  const perspectiveLabel = currentFlowPerspective === "ast" ? "AST nodes" : "runtime nodes";
  if (liveFinalResponse) {
    setStatus("Drawing simulation flow", `${model.nodes.length} ${perspectiveLabel} visible · simulation already complete`, "DRAWING");
  } else {
    setStatus("Simulating paths", `${liveInvocationCount} command calls · ${Math.max(liveAST.model.pathCount, liveRuntime.model.pathCount)} paths discovered`, "RUNNING");
  }
  if (revealed) {
    scheduleLiveReveal();
  } else if (liveFinalResponse && liveTraceIndex >= liveTraceEvents.length) {
    renderResponse(liveFinalResponse);
  }
}

function scheduleLiveReveal() {
  if (!liveRuntime || livePaused) return;
  if (liveRevealTimer) clearTimeout(liveRevealTimer);
  liveRevealTimer = window.setTimeout(() => {
    liveRevealTimer = 0;
    advanceLiveFlow();
  }, visualStepInterval());
}

function finishLiveSimulation(response) {
  liveFinalResponse = response;
  if (livePaused) {
    const visible = currentFlowPerspective === "ast" ? liveAST.model.nodes.length : liveRuntime.model.nodes.length;
    setStatus("Simulation flow paused", `${visible} nodes visible · simulation complete`, "PAUSED");
    return;
  }
  if (liveTraceIndex < liveTraceEvents.length || liveRevealTimer) {
    const visible = currentFlowPerspective === "ast" ? liveAST.model.nodes.length : liveRuntime.model.nodes.length;
    setStatus("Drawing simulation flow", `${visible} nodes visible · simulation already complete`, "DRAWING");
    return;
  }
  renderResponse(response);
}

function failWorker(message) {
  if (pendingRequest) clearTimeout(pendingRequest.timeoutID);
  pendingRequest = null;
  workerReady = false;
  renderFailure(message);
  updateLiveControls("WASM unavailable");
}

function runSimulation() {
  let request;
  let timeoutMillis;
  try {
    timeoutMillis = boundedInteger(elements.timeout_input, 250, 30000);
    request = {
      source: elements.source_input.value,
      commands: normalizeCommands(readCommandRows()),
      env: parseEnvironment(elements.environment_input.value),
      args: parseArguments(elements.arguments_input.value),
      stdin: elements.stdin_input.value,
      maxExecutionSteps: boundedInteger(elements.steps_input, 1, 100000),
      maxMemoryBytes: boundedInteger(elements.memory_input, 1, 64) * 1024 * 1024,
    };
  } catch (error) {
    renderFailure(error instanceof Error ? error.message : String(error));
    return;
  }

  sharedCodePendingReview = false;
  if (astPreviewTimer) clearTimeout(astPreviewTimer);
  astPreviewTimer = 0;
  pendingASTPreview = null;
  astPreviewDeferred = false;
  const id = ++requestSequence;
  const timeoutID = setTimeout(() => {
    if (!pendingRequest || pendingRequest.id !== id) return;
    worker.terminate();
    pendingRequest = null;
    recoveryMessage = `simulation exceeded browser timeout (${timeoutMillis} ms)`;
    renderFailure(recoveryMessage);
    startWorker();
  }, timeoutMillis);
  pendingRequest = { id, timeoutID };
  beginLiveFlow(request.source);
  updateLiveControls();
  setStatus("Simulating paths", "Expanding commands and exploring unresolved branches…", "RUNNING");
  worker.postMessage({ id, request });
}

function beginLiveFlow(source) {
  if (liveRevealTimer) clearTimeout(liveRevealTimer);
  liveRevealTimer = 0;
  const definitions = astPreviewSource === source ? [...(currentModels?.ast?.definitions?.values() || [])] : [];
  liveAST = createASTFlowModel(definitions);
  astPreviewSource = source;
  liveRuntime = createRuntimeFlowModel();
  liveTraceEvents = [];
  liveTraceIndex = 0;
  liveInvocationCount = 0;
  liveFinalResponse = null;
  livePaused = false;
  currentResponse = null;
  currentModels = {
    ast: liveAST.model,
    runtime: liveRuntime.model,
  };
  currentModel = currentModels[currentFlowPerspective] || liveRuntime.model;
  selectedNodeID = "";
  activeNodeID = "";
  document.querySelectorAll("[data-flow-perspective]").forEach((button) => {
    const active = button.dataset.flowPerspective === currentFlowPerspective;
    button.classList.toggle("active", active);
    button.setAttribute("aria-pressed", String(active));
  });
  renderGraph(currentModel);
  const astActive = currentFlowPerspective === "ast";
  elements.flow_empty.querySelector("h2").textContent = astActive ? "Waiting for AST execution" : "Waiting for runtime execution";
  elements.flow_empty.querySelector("p").textContent = astActive
    ? "Parsed syntax stays visible while reached nodes are highlighted in execution order."
    : "Control statements and expanded command calls appear here as the simulator reaches them.";
  elements.inspector_id.textContent = "NO SELECTION";
  elements.inspector.replaceChildren(createElement("div", "inspector-empty", astActive
    ? "Execution details appear as the simulator reaches each AST node."
    : "The current execution context will appear when the simulator reaches a control statement or command call."));
  renderOutputs([], "");
  renderInvocations([]);
  elements.diagnostics_view.replaceChildren(createElement("div", "inspector-empty", "Diagnostics are available after simulation completes."));
  elements.raw_output.textContent = "";
  elements.invocation_count.textContent = "0";
  elements.diagnostic_count.textContent = "0";
  elements.summary_calls.textContent = "0";
  elements.summary_paths.textContent = "0";
  elements.summary_memory.textContent = "0 B";
}

function toggleLivePause() {
  if (!liveRuntime) return;
  livePaused = !livePaused;
  if (liveRevealTimer) clearTimeout(liveRevealTimer);
  liveRevealTimer = 0;
  updateLiveControls();
  if (livePaused) {
    const phase = liveFinalResponse ? "simulation complete" : "simulation continues in the Worker";
    const visible = currentFlowPerspective === "ast" ? liveAST.model.nodes.length : liveRuntime.model.nodes.length;
    setStatus("Simulation flow paused", `${visible} nodes visible · ${phase}`, "PAUSED");
    return;
  }
  advanceLiveFlow();
}

function stopLiveRun() {
  if (!liveRuntime) return;
  const model = liveRuntime.model;
  const workerWasRunning = Boolean(pendingRequest);
  if (pendingRequest) clearTimeout(pendingRequest.timeoutID);
  pendingRequest = null;
  if (liveRevealTimer) clearTimeout(liveRevealTimer);
  liveRevealTimer = 0;
  if (workerWasRunning) {
    worker.terminate();
    workerReady = false;
  }
  const astModel = liveAST?.model || currentModels?.ast || buildFlowModels({ nodes: [], events: [] }).ast;
  liveAST = null;
  liveRuntime = null;
  livePaused = false;
  liveTraceEvents = [];
  liveTraceIndex = 0;
  liveInvocationCount = model.nodes.filter((node) => node.invocation).length;
  liveFinalResponse = null;
  currentModels = {
    ast: astModel,
    runtime: model,
  };
  currentModel = currentModels[currentFlowPerspective] || model;
  activeNodeID = "";
  renderGraph(currentModel, true);
  setStatus("Simulation stopped", `${model.nodes.length} runtime nodes retained`, "STOPPED");
  updateLiveControls(workerWasRunning ? "Restarting WASM" : "Run simulation");
  showToast("Simulation stopped. Visible runtime nodes were retained.");
  if (workerWasRunning) {
    startWorker();
  } else if (astPreviewDeferred) {
    scheduleASTPreview();
  }
}

function boundedInteger(input, minimum, maximum) {
  const value = Number(input.value);
  if (!Number.isInteger(value) || value < minimum || value > maximum) {
    throw new Error(`${input.closest("label")?.firstChild?.textContent?.trim() || "value"} must be ${minimum}–${maximum}`);
  }
  return value;
}

function renderResponse(response) {
  if (liveRevealTimer) clearTimeout(liveRevealTimer);
  liveRevealTimer = 0;
  const perspective = currentFlowPerspective;
  const streamedAST = liveAST?.finish(response);
  const streamedRuntime = liveRuntime?.finish(response);
  const completeStream = Boolean(streamedAST && streamedRuntime)
    && liveTraceIndex === liveTraceEvents.length
    && liveTraceEvents.length === (response.events || []).length + (response.nodes || []).length;
  liveAST = null;
  liveRuntime = null;
  liveTraceEvents = [];
  liveTraceIndex = 0;
  liveFinalResponse = null;
  livePaused = false;
  currentResponse = response;
  currentModels = completeStream
    ? { ast: streamedAST, runtime: streamedRuntime }
    : buildFlowModels(response);
  selectedNodeID = "";
  activeNodeID = "";
  renderOutputs(response.outputs || [], response.error || "");
  renderInvocations(response.invocations || []);
  renderDiagnostics(currentModels.ast);
  elements.raw_output.textContent = JSON.stringify(response, null, 2);
  elements.invocation_count.textContent = String((response.invocations || []).length);
  elements.summary_calls.textContent = String((response.invocations || []).length);
  elements.summary_paths.textContent = String(currentModels.runtime.pathCount);
  elements.summary_memory.textContent = formatBytes(currentModels.runtime.peakLogicalBytes);
  const duration = formatDuration(response.durationMicros || 0);
  setStatus(response.error ? "Simulation stopped" : "Simulation complete",
    `${currentModels.ast.nodes.length} AST nodes · ${response.events?.length || 0} trace events · ${currentModels.runtime.pathCount} paths`, duration);
  updateLiveControls();
  selectFlowPerspective(currentModels[perspective] ? perspective : "runtime");
  if (astPreviewDeferred) scheduleASTPreview();
}

function selectFlowPerspective(name) {
  const model = currentModels?.[name];
  if (!model) return;
  currentFlowPerspective = name;
  currentModel = model;
  selectedNodeID = "";
  activeNodeID = "";
  document.querySelectorAll("[data-flow-perspective]").forEach((button) => {
    const active = button.dataset.flowPerspective === name;
    button.classList.toggle("active", active);
    button.setAttribute("aria-pressed", String(active));
  });
  renderGraph(model);
}

function renderFailure(message) {
  if (liveRevealTimer) clearTimeout(liveRevealTimer);
  liveRevealTimer = 0;
  liveAST = null;
  liveRuntime = null;
  liveTraceEvents = [];
  liveTraceIndex = 0;
  liveFinalResponse = null;
  livePaused = false;
  currentResponse = { error: message, nodes: [], events: [], invocations: [], outputs: [] };
  currentModels = buildFlowModels(currentResponse);
  currentModel = currentModels[currentFlowPerspective] || currentModels.runtime;
  activeNodeID = "";
  elements.flow_empty.classList.remove("hidden");
  elements.flow_scroll.classList.add("hidden");
  elements.flow_empty.querySelector("h2").textContent = "Simulation unavailable";
  elements.flow_empty.querySelector("p").textContent = message;
  elements.inspector_id.textContent = "NO SELECTION";
  elements.inspector.replaceChildren(createElement("div", "inspector-empty", "Run a valid script, then select a flow node to inspect it."));
  renderOutputs([], message);
  renderInvocations([]);
  renderDiagnostics(currentModel);
  elements.raw_output.textContent = JSON.stringify(currentResponse, null, 2);
  elements.invocation_count.textContent = "0";
  elements.output_count.textContent = "0";
  elements.summary_calls.textContent = "0";
  elements.summary_paths.textContent = "0";
  elements.summary_memory.textContent = "0 B";
  updateLiveControls(workerReady ? "Run simulation" : "WASM unavailable");
  setStatus("Simulation issue", message, "ERROR");
  showToast(message, true);
}

function renderGraph(model, preserveSelection = false) {
  elements.flow_nodes.replaceChildren();
  elements.flow_edges.replaceChildren();
  const renderedNodes = model.nodes.slice(0, maximumRenderedNodes);
  if (renderedNodes.length === 0) {
    elements.flow_empty.querySelector("h2").textContent = "No statements to display";
    elements.flow_empty.querySelector("p").textContent = "Add Bash source in the input workspace and run the simulation.";
    elements.flow_empty.classList.remove("hidden");
    elements.flow_scroll.classList.add("hidden");
    return;
  }
  elements.flow_empty.classList.add("hidden");
  elements.flow_scroll.classList.remove("hidden");
  const minimumWidth = elements.flow_scroll.clientWidth / zoom || undefined;
  const minimumHeight = elements.flow_scroll.clientHeight / zoom || undefined;
  const layout = model.perspective === "ast"
    ? layoutASTFlowGraph(renderedNodes, model.edges, minimumWidth, minimumHeight)
    : layoutFlowGraph(renderedNodes, model.edges, minimumWidth, minimumHeight);
  elements.flow_canvas.style.width = `${layout.width}px`;
  elements.flow_canvas.style.height = `${layout.height}px`;
  elements.flow_edges.setAttribute("width", String(layout.width));
  elements.flow_edges.setAttribute("height", String(layout.height));
  elements.flow_edges.setAttribute("viewBox", `0 0 ${layout.width} ${layout.height}`);

  const renderedIDs = new Set(renderedNodes.map((node) => node.id));
  for (const edge of model.edges) {
    if (!renderedIDs.has(edge.from) || !renderedIDs.has(edge.to)) continue;
    const from = layout.positions.get(edge.from);
    const to = layout.positions.get(edge.to);
    const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
    path.setAttribute("d", flowEdgePath(edge, from, to, layout));
    path.setAttribute("class", `flow-edge ${edge.state}`);
    path.dataset.sequence = String(edge.sequence || 0);
    path.dataset.to = edge.to;
    elements.flow_edges.append(path);
  }

  for (const node of renderedNodes) {
    const position = layout.positions.get(node.id);
    const button = createElement("button", `flow-node ${node.definition.kind} ${node.state}`);
    button.type = "button";
    button.style.left = `${position.x}px`;
    button.style.top = `${position.y}px`;
    button.dataset.nodeId = node.id;
    button.dataset.sequence = String(node.sequence || 0);
    const head = createElement("div", "flow-node-head");
    head.append(createElement("span", "", node.definition.kind), createElement("span", "flow-node-path", node.pathID ? `P${node.pathID}` : "STATIC"));
    const body = createElement("div", "flow-node-body");
    body.append(
      createElement("div", "flow-node-title", node.definition.snippet || node.definition.kind),
      createElement("div", "flow-node-location", sourceLabel(node.definition.source)),
    );
    const stats = createElement("div", "flow-node-stats");
    stats.append(nodeStat("steps", node.executed ? String(node.steps || 0) : "—"), nodeStat("path mem", node.memory ? formatBytes(node.memory.stateBytes) : "—"));
    body.append(stats);
    button.append(head, body);
    button.addEventListener("click", () => selectNode(node.id));
    elements.flow_nodes.append(button);
  }
  const selected = preserveSelection && renderedIDs.has(selectedNodeID)
    ? renderedNodes.find((node) => node.id === selectedNodeID)
    : renderedNodes.find((node) => node.executed) || renderedNodes[0];
  selectNode(selected.id);
  const selectedElement = elements.flow_nodes.querySelector(`[data-node-id="${CSS.escape(selected.id)}"]`);
  if (preserveSelection && activeNodeID) {
    const active = elements.flow_nodes.querySelector(`[data-node-id="${CSS.escape(activeNodeID)}"]`);
    active?.classList.add("current");
    followFlowNode(active);
  } else if (!preserveSelection) {
    followFlowNode(selectedElement, "auto", true);
  }
}

function nodeStat(label, value) {
  const container = createElement("div", "flow-node-stat");
  container.append(createElement("span", "", label), createElement("strong", "", value));
  return container;
}

function selectNode(id) {
  const node = currentModel?.nodes.find((candidate) => candidate.id === id);
  if (!node) return;
  selectedNodeID = id;
  document.querySelectorAll(".flow-node").forEach((element) => element.classList.toggle("selected", element.dataset.nodeId === id));
  renderInspector(node);
}

function renderInspector(node) {
  elements.inspector_id.textContent = `NODE ${node.nodeID}`;
  const overview = createElement("section", "inspector-panel");
  overview.dataset.inspectorPanel = "overview";
  const selected = createElement("div", "inspector-selected");
  const row = createElement("div", "inspector-selected-row");
  row.append(createElement("strong", "", node.definition.snippet), createElement("span", `state-badge ${nodeState(node)}`, nodeStateLabel(node)));
  selected.append(row, createElement("div", "inspector-source", `${sourceLabel(node.definition.source)} · ${node.pathID ? `path ${node.pathID}` : "static syntax"}`));
  overview.append(selected);

  const execution = inspectorSection("Execution");
  execution.append(memoryRow("Occurrence", executionOccurrenceLabel(node)));
  execution.append(memoryRow("Trace sequence", node.executed ? `${node.sequence}–${node.endSequence}` : "not reached"));
  execution.append(memoryRow("Execution steps", node.executed ? String(node.steps || 0) : "—"));
  execution.append(memoryRow("Path status", statusLabel(node.pathStatus ?? node.status)));
  overview.append(execution);

  if (node.memory) overview.append(renderMemory(node.memory));
  if (node.forked) {
    const section = inspectorSection("Branching");
    section.append(createElement("div", "inspector-note", `This node produced ${node.childPathIDs?.length || 0} paths because its result could not be resolved. Every reachable branch was explored independently.`));
    overview.append(section);
  } else if (!node.executed) {
    const section = inspectorSection("Reachability");
    section.append(createElement("div", "inspector-note", "The parser discovered this statement, but no simulated execution path reached it. It is intentionally rendered with a dashed outline."));
    overview.append(section);
  }

  const input = createElement("section", "inspector-panel");
  input.dataset.inspectorPanel = "input";
  input.append(renderInputSnapshot(node.inputSnapshot, node.inputSnapshotTruncated, node.executed, node.invocation));

  const output = createElement("section", "inspector-panel");
  output.dataset.inspectorPanel = "output";
  output.append(renderOutputSnapshot(node));

  elements.inspector.replaceChildren(overview, input, output);
  selectInspectorTab(selectedInspectorTab);
}

function selectInspectorTab(name) {
  selectedInspectorTab = name;
  document.querySelectorAll("[data-inspector-tab]").forEach((button) => {
    button.classList.toggle("active", button.dataset.inspectorTab === name);
  });
  elements.inspector.querySelectorAll("[data-inspector-panel]").forEach((panel) => {
    panel.classList.toggle("active", panel.dataset.inspectorPanel === name);
  });
}

function renderInputSnapshot(snapshot, truncated, executed, invocation) {
  const fragment = document.createDocumentFragment();
  if (invocation) fragment.append(renderCommandInput(invocation));
  if (!snapshot) {
    fragment.append(snapshotUnavailable(truncated, executed, "input"));
    return fragment;
  }

  const process = createElement("section", "inspector-section");
  const table = createElement("div", "memory-table");
  table.append(contextRow("Working directory", snapshot.directory, snapshot.directoryUnresolved));
  table.append(contextRow("Positional args", `${(snapshot.args || []).length}`, snapshot.argsUnresolved));
  process.append(table);
  const argumentsList = createElement("div", "argument-list context-arguments");
  if ((snapshot.args || []).length === 0) {
    argumentsList.append(createElement("span", "empty-value", "no positional arguments"));
  } else {
    for (const argument of snapshot.args) {
      argumentsList.append(createElement("span", `argument-chip ${snapshot.argsUnresolved ? "unresolved" : ""}`, snapshot.argsUnresolved ? `<unresolved: ${quote(argument)}>` : quote(argument)));
    }
  }
  process.append(argumentsList);
  process.append(snapshotField("Remaining stdin", snapshot.stdin, snapshot.stdinUnresolved));
  fragment.append(process);

  const variables = inspectorSection(`Variables (${(snapshot.variables || []).length})`);
  const list = createElement("div", "variable-list");
  for (const variable of snapshot.variables || []) {
    const item = createElement("article", `variable-item ${variable.unresolved ? "unresolved" : ""}`);
    const head = createElement("div", "variable-head");
    head.append(createElement("strong", "", variable.name));
    const badges = createElement("span", "variable-badges");
    if (variable.exported) badges.append(createElement("i", "exported", "exported"));
    if (variable.readOnly) badges.append(createElement("i", "", "readonly"));
    if (variable.kind && variable.kind !== "string") badges.append(createElement("i", "", variable.kind));
    if (variable.unresolved) badges.append(createElement("i", "unresolved", "unresolved"));
    head.append(badges);
    item.append(head, createElement("pre", "variable-value", variable.unresolved ? unresolvedText(variable.value) : variable.value));
    list.append(item);
  }
  if (!list.childElementCount) list.append(createElement("div", "inspector-empty", "No variables are set."));
  variables.append(list);
  fragment.append(variables);
  return fragment;
}

function renderCommandInput(invocation) {
  const unresolved = invocation.unresolved || {};
  const section = inspectorSection("Command invocation");
  const table = createElement("div", "memory-table");
  table.append(contextRow("Command", invocation.name || "", false));
  table.append(contextRow("Working directory", invocation.dir || "", Boolean(unresolved.dir)));
  section.append(table);

  const argumentsList = createElement("div", "argument-list context-arguments");
  for (const argument of invocation.args || []) argumentsList.append(argumentChip(argument));
  if (!argumentsList.childElementCount) argumentsList.append(createElement("span", "empty-value", "no arguments"));
  const argumentsField = createElement("div", "snapshot-field argument-field");
  argumentsField.append(createElement("label", "", "Arguments"), argumentsList);
  section.append(argumentsField);
  section.append(snapshotField("Stdin", decodeInvocationStdin(invocation.stdin), Boolean(unresolved.stdin)));

  const environment = inspectorSection(`Command environment (${Object.keys(invocation.env || {}).length})`);
  const unknownEnvironment = new Set(unresolved.env || []);
  const list = createElement("div", "variable-list");
  for (const name of Object.keys(invocation.env || {}).sort()) {
    const unknown = unknownEnvironment.has(name);
    const item = createElement("article", `variable-item ${unknown ? "unresolved" : ""}`);
    const head = createElement("div", "variable-head");
    head.append(createElement("strong", "", name));
    if (unknown) {
      const badges = createElement("span", "variable-badges");
      badges.append(createElement("i", "unresolved", "unresolved"));
      head.append(badges);
    }
    item.append(head, createElement("pre", "variable-value", unknown ? unresolvedText(invocation.env[name]) : invocation.env[name]));
    list.append(item);
  }
  if (!list.childElementCount) list.append(createElement("div", "inspector-empty", "No exported environment variables."));
  environment.append(list);
  section.append(environment);
  return section;
}

function decodeInvocationStdin(value) {
  if (!value) return "";
  try {
    const bytes = Uint8Array.from(atob(value), (character) => character.charCodeAt(0));
    return new TextDecoder().decode(bytes);
  } catch {
    return String(value);
  }
}

function renderOutputSnapshot(node) {
  const fragment = document.createDocumentFragment();
  const output = nodeOutput(node);
  if (!output) {
    fragment.append(snapshotUnavailable(node.outputSnapshotTruncated, node.executed, "output"));
    return fragment;
  }
  const status = inspectorSection("Node result");
  const table = createElement("div", "memory-table");
  table.append(contextRow("Exit code", String(output.exitCode ?? 0), output.exitCodeUnresolved));
  table.append(contextRow("Error", output.error || "", false));
  status.append(table);
  fragment.append(status);

  const streams = inspectorSection("Node streams");
  streams.append(snapshotField("Stdout", output.stdout, output.stdoutUnresolved));
  streams.append(snapshotField("Stderr", output.stderr, output.stderrUnresolved));
  fragment.append(streams);
  if (output.truncated) fragment.append(createElement("div", "inspector-note", "This node's output was omitted after the trace display budget was reached."));
  return fragment;
}

function snapshotUnavailable(truncated, executed, kind) {
  if (truncated) return createElement("div", "inspector-note", `The ${kind} snapshot was omitted after the 4 MiB trace display budget was reached.`);
  if (!executed) return createElement("div", "inspector-empty", `This statement was not executed, so it has no ${kind} snapshot.`);
  return createElement("div", "inspector-empty", `No ${kind} snapshot is available.`);
}

function contextRow(label, value, unresolved) {
  return memoryRow(label, unresolved ? unresolvedText(value) : concreteDisplayValue(value), unresolved ? "unresolved" : "");
}

function snapshotField(label, value, unresolved) {
  const field = createElement("div", "snapshot-field");
  field.append(createElement("label", "", label));
  field.append(createElement("pre", unresolved ? "unresolved-value" : "", unresolved ? unresolvedText(value) : concreteDisplayValue(value)));
  return field;
}

function unresolvedText(representative) {
  return representative ? `<unresolved>\nrepresentative: ${representative}` : "<unresolved>";
}

function renderMemory(memory) {
  const section = inspectorSection("Logical retained memory");
  const maximum = Math.max(1, memory.maximumBytes || memory.aggregateBytes || 1);
  section.append(memoryMeter(`${memory.retainedPaths || 0} retained paths`, memory.aggregateBytes, maximum, "purple"));
  const table = createElement("div", "memory-table");
  const entries = [
    ["Path snapshot total", memory.stateBytes],
    ["Variables", memory.variablesBytes], ["Virtual files", memory.virtualFileBytes],
    ["Streams", memory.streamBytes], ["Functions", memory.functionBytes],
    ["Substitutions", memory.substitutionBytes],
    ["Runtime state", memory.otherBytes], ["Auxiliary", memory.auxiliaryBytes],
  ];
  for (const [label, value] of entries) table.append(memoryRow(label, formatBytes(value)));
  section.append(table);
  const note = createElement("div", "inspector-note", "Retained budget is the shared limit charge after the initial-state baseline. Path snapshot total includes that baseline. Neither value measures the browser or Go heap.");
  note.style.marginTop = "8px";
  section.append(note);
  return section;
}

function memoryMeter(label, value, maximum, variant) {
  const meter = createElement("div", "meter");
  const header = createElement("div", "meter-label");
  header.append(createElement("span", "", label), createElement("b", "", `${formatBytes(value)} / ${formatBytes(maximum)}`));
  const bar = createElement("div", `meter-bar ${variant}`);
  const fill = document.createElement("i");
  fill.style.width = `${Math.min(100, (Number(value) || 0) / maximum * 100)}%`;
  bar.append(fill);
  meter.append(header, bar);
  return meter;
}

function renderOutputs(outputs, simulationError) {
  const fragment = document.createDocumentFragment();
  if (simulationError) {
    fragment.append(createElement("div", "output-global-error", simulationError));
  }
  if (outputs.length === 0 && !simulationError) {
    fragment.append(createElement("div", "inspector-empty", "No final execution path output is available."));
  }
  for (const output of outputs) {
    const result = output.result || {};
    const card = createElement("article", `output-card ${result.error ? "error" : ""}`);
    const head = createElement("div", "output-card-head");
    head.append(
      createElement("strong", "", `Path ${output.pathId}`),
      createElement("span", "", `${statusLabel(output.status)}${output.truncated ? " · truncated" : ""}`),
    );
    const grid = createElement("div", "output-grid");
    grid.append(
      outputField("Exit code", result.exitCodeUnresolved ? unresolvedText(String(result.exitCode ?? 0)) : String(result.exitCode ?? 0), result.exitCodeUnresolved),
      outputField("Error", result.error || "", false),
      outputField("Stdout", result.stdoutUnresolved ? unresolvedText(result.stdout || "") : concreteDisplayValue(result.stdout), result.stdoutUnresolved, true),
      outputField("Stderr", result.stderrUnresolved ? unresolvedText(result.stderr || "") : concreteDisplayValue(result.stderr), result.stderrUnresolved, true),
    );
    card.append(head, grid);
    fragment.append(card);
  }
  elements.outputs_view.replaceChildren(fragment);
  elements.output_count.textContent = String(outputs.length);
}

function outputField(label, value, unresolved, wide = false) {
  const field = createElement("div", `output-field ${wide ? "wide" : ""}`);
  field.append(createElement("label", "", label), createElement("pre", unresolved ? "unresolved-value" : "", value));
  return field;
}

function renderInvocations(invocations) {
  const fragment = document.createDocumentFragment();
  if (invocations.length === 0) {
    fragment.append(createElement("div", "inspector-empty", "No registered command handler was called."));
  }
  for (const item of invocations) {
    const card = createElement("article", "invocation-card");
    const top = createElement("div", "invocation-top");
    top.append(createElement("strong", "", item.invocation?.name || "command"), createElement("span", "", `path ${item.pathId} · event ${item.sequence}`));
    const argumentsList = createElement("div", "argument-list");
    for (const argument of item.invocation?.args || []) argumentsList.append(argumentChip(argument));
    card.append(top, argumentsList);
    if (item.result) card.append(createElement("div", "inspector-source", `exit ${item.result.exitCode}${item.result.unresolved ? " · unresolved" : ""}`));
    fragment.append(card);
  }
  elements.invocations_view.replaceChildren(fragment);
}

function renderDiagnostics(model) {
  const diagnostics = [];
  if (model.error) diagnostics.push({ level: "error", title: "Simulation stopped", detail: model.error });
  if (model.truncated) diagnostics.push({ level: "warning", title: "Display limit reached", detail: "Simulation continued, but some later trace details, state snapshots, or output bytes were omitted from this visualization." });
  if (model.nodes.length > maximumRenderedNodes) diagnostics.push({ level: "warning", title: "Graph rendering limited", detail: `${model.nodes.length - maximumRenderedNodes} nodes remain available in Raw trace but are omitted from the canvas.` });
  if (!model.error && !model.truncated) diagnostics.push({ level: "", title: "No simulator diagnostics", detail: "The simulation completed within the configured execution and logical memory limits." });
  const fragment = document.createDocumentFragment();
  for (const diagnostic of diagnostics) {
    const card = createElement("article", `diagnostic-card ${diagnostic.level}`);
    card.append(createElement("h3", "", diagnostic.title), createElement("p", "", diagnostic.detail));
    fragment.append(card);
  }
  elements.diagnostics_view.replaceChildren(fragment);
  elements.diagnostic_count.textContent = String(diagnostics.filter((item) => item.level).length);
}

function argumentChip(argument) {
  const unresolved = argument?.kind !== 0;
  return createElement("span", `argument-chip ${unresolved ? "unresolved" : ""}`, unresolved ? "<unresolved>" : quote(argument?.value || ""));
}

function visualStepInterval() {
  return 600 / Number(elements.speed_input.value || 1);
}

function setZoom(value) {
  zoom = Math.max(.5, Math.min(1.7, Math.round(value * 10) / 10));
  elements.flow_canvas.style.zoom = String(zoom);
  elements.zoom_value.textContent = `${Math.round(zoom * 100)}%`;
  scheduleGraphRelayout();
}

function scheduleGraphRelayout() {
  if (graphRelayoutFrame) return;
  graphRelayoutFrame = requestAnimationFrame(() => {
    graphRelayoutFrame = 0;
    if (!currentModel?.nodes.length || !elements.flow_view.classList.contains("active")) return;
    renderGraph(currentModel, true);
  });
}

function followFlowNode(element, behavior = "smooth", resetTop = false) {
  if (!element || elements.flow_scroll.classList.contains("hidden")) return;
  const target = flowScrollTarget({
    left: element.offsetLeft * zoom,
    top: element.offsetTop * zoom,
    width: element.offsetWidth * zoom,
    height: element.offsetHeight * zoom,
  }, {
    width: elements.flow_scroll.clientWidth,
    height: elements.flow_scroll.clientHeight,
    scrollTop: elements.flow_scroll.scrollTop,
  }, resetTop);
  elements.flow_scroll.scrollTo({
    left: target.left,
    top: target.top,
    behavior,
  });
}

function setInputCollapsed(collapsed) {
  elements.workspace.classList.toggle("input-collapsed", collapsed);
  elements.expand_input.classList.toggle("hidden", !collapsed);
  updatePaneColumns();
}

function beginInputResize(event) {
  if (elements.workspace.classList.contains("input-collapsed")) return;
  event.preventDefault();
  const startX = event.clientX;
  const startWidth = document.querySelector(".input-pane").getBoundingClientRect().width;
  elements.input_resizer.setPointerCapture(event.pointerId);
  const move = (moveEvent) => {
    const width = Math.max(280, Math.min(620, startWidth + moveEvent.clientX - startX));
    elements.workspace.dataset.inputWidth = `${width}px`;
    updatePaneColumns();
  };
  const stop = () => {
    elements.input_resizer.removeEventListener("pointermove", move);
    elements.input_resizer.removeEventListener("pointerup", stop);
    elements.input_resizer.removeEventListener("pointercancel", stop);
  };
  elements.input_resizer.addEventListener("pointermove", move);
  elements.input_resizer.addEventListener("pointerup", stop);
  elements.input_resizer.addEventListener("pointercancel", stop);
}

function beginInspectorResize(event) {
  event.preventDefault();
  const startX = event.clientX;
  const startWidth = document.querySelector(".inspector-pane").getBoundingClientRect().width;
  elements.inspector_resizer.setPointerCapture(event.pointerId);
  const move = (moveEvent) => {
    const width = Math.max(280, Math.min(640, startWidth - (moveEvent.clientX - startX)));
    elements.workspace.dataset.inspectorWidth = `${width}px`;
    updatePaneColumns();
  };
  const stop = () => {
    elements.inspector_resizer.removeEventListener("pointermove", move);
    elements.inspector_resizer.removeEventListener("pointerup", stop);
    elements.inspector_resizer.removeEventListener("pointercancel", stop);
  };
  elements.inspector_resizer.addEventListener("pointermove", move);
  elements.inspector_resizer.addEventListener("pointerup", stop);
  elements.inspector_resizer.addEventListener("pointercancel", stop);
}

function updatePaneColumns() {
  const inputWidth = elements.workspace.classList.contains("input-collapsed") ? "0" : elements.workspace.dataset.inputWidth || "380px";
  const inspectorWidth = elements.workspace.dataset.inspectorWidth || "350px";
  const columns = window.innerWidth <= 1080
    ? `${inputWidth} minmax(440px, 1fr)`
    : `${inputWidth} minmax(500px, 1fr) ${inspectorWidth}`;
  elements.workspace.style.gridTemplateColumns = columns;
  document.querySelector(".status-bar").style.gridTemplateColumns = columns;
  scheduleGraphRelayout();
}

async function shareCurrentState() {
  const state = {
    source: elements.source_input.value,
    commands: readCommandRows(),
    environment: elements.environment_input.value,
    arguments: elements.arguments_input.value,
    stdin: elements.stdin_input.value,
    steps: elements.steps_input.value,
    memory: elements.memory_input.value,
  };
  const hash = `#state=${encodeState(state)}`;
  if (hash.length > 60000) {
    showToast("This script is too large for a shareable URL.", true);
    return;
  }
  const url = new URL(location.href);
  url.hash = hash;
  history.replaceState(null, "", hash);
  try {
    await navigator.clipboard.writeText(url.href);
    showToast("Share URL copied. The script is encoded in the URL; nothing was uploaded.");
  } catch {
    showToast("Share URL is ready in the address bar.");
  }
}

function restoreSharedState() {
  if (!location.hash.startsWith("#state=")) return false;
  try {
    const encoded = location.hash.slice(7);
    if (encoded.length > 60000) throw new Error("shared state exceeds the URL limit");
    const state = decodeState(encoded);
    elements.source_input.value = String(state.source || "");
    renderCommandRows(Array.isArray(state.commands) ? state.commands : []);
    elements.environment_input.value = String(state.environment || "");
    elements.arguments_input.value = String(state.arguments || "");
    elements.stdin_input.value = String(state.stdin || "");
    elements.steps_input.value = String(state.steps || 10000);
    elements.memory_input.value = String(state.memory || 2);
    return true;
  } catch {
    showToast("The shared playground state is invalid; the default example was loaded.", true);
    return false;
  }
}

function encodeState(value) {
  const bytes = new TextEncoder().encode(JSON.stringify(value));
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary).replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

function decodeState(value) {
  const base64 = value.replaceAll("-", "+").replaceAll("_", "/").padEnd(Math.ceil(value.length / 4) * 4, "=");
  const binary = atob(base64);
  return JSON.parse(new TextDecoder().decode(Uint8Array.from(binary, (character) => character.charCodeAt(0))));
}

function inspectorSection(title) {
  const section = createElement("section", "inspector-section");
  section.append(createElement("h3", "", title));
  return section;
}

function memoryRow(label, value, className = "") {
  const row = createElement("div", `memory-row ${className}`);
  row.append(createElement("span", "", label), createElement("b", "", String(value ?? "—")));
  return row;
}

function createElement(tag, className = "", text = "") {
  const element = document.createElement(tag);
  if (className) element.className = className;
  if (text !== "") element.textContent = text;
  return element;
}

function nodeState(node) {
  if (!node.executed) return "not-executed";
  if (node.forked || node.commandResult?.unresolved || node.status === 3 || node.pathStatus === 3) return "unresolved";
  return "executed";
}

function nodeStateLabel(node) {
  return nodeState(node).replace("-", " ");
}

function statusLabel(status) {
  return ["completed", "terminated", "incomplete", "unresolved"][status] || "—";
}

function sourceLabel(source) {
  if (!source) return "unknown source";
  return `${source.name || "command.sh"}:${source.line || 0}:${source.column || 0}`;
}

function quote(value) {
  return JSON.stringify(String(value));
}

function formatDuration(microseconds) {
  if (microseconds < 1000) return `${microseconds} µs`;
  if (microseconds < 1_000_000) return `${(microseconds / 1000).toFixed(microseconds < 10_000 ? 1 : 0)} ms`;
  return `${(microseconds / 1_000_000).toFixed(2)} s`;
}

function updateLiveControls(idleLabel = "Run simulation") {
  const view = liveControlView(Boolean(liveRuntime), livePaused);
  elements.run_button.classList.toggle("running", view.running);
  elements.run_button.querySelector(".run-icon").textContent = view.icon;
  elements.run_button.querySelector(".run-label").textContent = !liveRuntime && !workerReady ? idleLabel : view.label;
  elements.run_button.disabled = !liveRuntime && !workerReady;
  elements.stop_button.classList.toggle("hidden", !view.stopVisible);
  elements.stop_button.disabled = !view.stopVisible;
}

function setStatus(title, detail, duration) {
  elements.status_title.textContent = title;
  elements.status_detail.textContent = detail;
  elements.status_duration.textContent = duration;
}

function showToast(message, error = false) {
  elements.toast.textContent = message;
  elements.toast.classList.toggle("error", error);
  elements.toast.classList.add("visible");
  window.setTimeout(() => elements.toast.classList.remove("visible"), 3200);
}
