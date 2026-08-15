export function buildFlowModel(response = {}) {
  return buildASTFlowModel(response);
}

export function buildFlowModels(response = {}) {
  return {
    ast: buildASTFlowModel(response),
    runtime: buildRuntimeFlowModel(response),
  };
}

function buildASTFlowModel(response = {}) {
  const ast = createASTFlowModel(response.nodes || []);
  for (const event of [...(response.events || [])].sort((left, right) => left.sequence - right.sequence)) {
    ast.append(event);
  }
  return ast.finish(response);
}

export function createASTFlowModel(initialDefinitions = []) {
  const definitions = new Map();
  const nodeByDefinitionID = new Map();
  const activeByPath = new Map();
  const commandsByPath = new Map();
  const lastByPath = new Map();
  const unresolvedPaths = new Set();
  const paths = new Set();
  const model = {
    perspective: "ast",
    nodes: [],
    edges: [],
    definitions,
    maximumSequence: 0,
    pathCount: 0,
    peakLogicalBytes: 0,
    truncated: false,
    error: "",
    durationMicros: 0,
  };

  function addDefinition(definition) {
    if (!definition?.id) return false;
    definitions.set(definition.id, definition);
    const existing = nodeByDefinitionID.get(definition.id);
    if (definition.embedded) {
      if (!existing) return false;
      model.nodes = model.nodes.filter((node) => node !== existing);
      nodeByDefinitionID.delete(definition.id);
      rebuildSyntaxEdges();
      return true;
    }
    if (existing) {
      const topologyChanged = existing.definition.parentId !== definition.parentId
        || existing.definition.flowGroup !== definition.flowGroup
        || existing.definition.flowCanSkip !== definition.flowCanSkip;
      existing.definition = definition;
      if (topologyChanged) rebuildSyntaxEdges();
      return false;
    }
    const syntaxNode = {
      id: `ast:${definition.id}`,
      nodeID: definition.id,
      pathID: 0,
      pathIDs: [],
      sequence: 0,
      endSequence: 0,
      definition,
      state: "not-executed",
      executed: false,
      executionCount: 0,
      commandCount: 0,
      memory: null,
      steps: 0,
      invocation: null,
      commandResult: null,
      forked: false,
      inputSnapshot: null,
      inputSnapshotTruncated: false,
      outputSnapshot: null,
      outputSnapshotTruncated: false,
      syntaxParentID: null,
      syntaxFlowGroup: 0,
    };
    model.nodes.push(syntaxNode);
    model.nodes.sort((left, right) => left.nodeID - right.nodeID);
    nodeByDefinitionID.set(definition.id, syntaxNode);
    rebuildSyntaxEdges();
    return true;
  }

  function syntaxNode(event, fallbackToActive = false) {
    if (event.nodeId && !nodeByDefinitionID.has(event.nodeId)) {
      addDefinition(event.node || definitions.get(event.nodeId) || unknownDefinition(event.nodeId));
    }
    if (definitions.get(event.nodeId)?.embedded) return null;
    const direct = nodeByDefinitionID.get(event.nodeId);
    if (direct || !fallbackToActive) return direct || null;
    const active = activeByPath.get(event.pathId) || [];
    return nodeByDefinitionID.get(active.at(-1)) || nodeByDefinitionID.get(lastByPath.get(event.pathId)) || null;
  }

  function visibleSyntaxNode(nodeID) {
    for (let current = nodeID; current; current = definitions.get(current)?.parentId) {
      const node = nodeByDefinitionID.get(current);
      if (node) return node;
    }
    return null;
  }

  function visibleSyntaxParent(definition) {
    let flowGroup = Number(definition?.flowGroup || 0);
    for (let current = definition?.parentId; current; current = definitions.get(current)?.parentId) {
      const node = nodeByDefinitionID.get(current);
      if (node) return { node, flowGroup };
      flowGroup = Number(definitions.get(current)?.flowGroup || 0);
    }
    return null;
  }

  function markPath(event) {
    if (event.pathId) paths.add(event.pathId);
    for (const childPath of event.childPathIds || []) paths.add(childPath);
    model.pathCount = paths.size;
  }

  function updateNode(node, event) {
    if (!node) return;
    node.endSequence = Math.max(node.endSequence, event.sequence || 0);
    node.memory = event.memory || node.memory;
    node.steps = event.steps ?? node.steps;
    node.status = event.status ?? node.status;
    model.peakLogicalBytes = Math.max(model.peakLogicalBytes, event.memory?.aggregateBytes || 0);
  }

  function updateSyntaxEdgeStates() {
    for (const edge of model.edges) {
      const source = nodeByDefinitionID.get(edge.fromNodeID);
      const target = nodeByDefinitionID.get(edge.toNodeID);
      edge.pathID = target?.pathID || source?.pathID || 0;
      edge.sequence = target?.sequence || source?.sequence || 0;
      const bypassedChildExecuted = edge.fallthrough
        && edge.bypassedNodeIDs.some((nodeID) => nodeByDefinitionID.get(nodeID)?.executed);
      if (bypassedChildExecuted) {
        edge.state = source?.forked && target?.executed ? "unresolved" : "not-executed";
        continue;
      }
      edge.state = !source?.executed || !target?.executed
        ? "not-executed"
        : (source.reachedFromUnresolvedPath || target.reachedFromUnresolvedPath ? "unresolved" : "executed");
    }
  }

  function rebuildSyntaxEdges() {
    const edges = [];
    const roots = [];
    const childGroups = new Map();
    for (const node of model.nodes) {
      const parent = visibleSyntaxParent(node.definition);
      if (parent) {
        node.syntaxParentID = parent.node.id;
        node.syntaxFlowGroup = parent.flowGroup;
        const groups = childGroups.get(parent.node.id) || new Map();
        groups.set(parent.flowGroup, [...(groups.get(parent.flowGroup) || []), node]);
        childGroups.set(parent.node.id, groups);
      } else {
        node.syntaxParentID = null;
        node.syntaxFlowGroup = 0;
        roots.push(node);
      }
    }

    const connectSequence = (sequence, incoming = []) => {
      let exits = incoming;
      for (const node of sequence) {
        for (const exit of exits) {
          edges.push(syntaxEdge(exit.node, node, node.syntaxParentID === exit.node.id, node.syntaxFlowGroup, exit));
        }
        const groups = [...(childGroups.get(node.id)?.entries() || [])]
          .sort(([left], [right]) => left - right);
        exits = groups.length === 0
          ? [{ node }]
          : groups.flatMap(([, children]) => connectSequence(children, [{ node }]));
        if (groups.length !== 0 && node.definition.flowCanSkip) {
          exits.push({
            node,
            fallthrough: true,
            bypassedNodeIDs: groups.flatMap(([, children]) => children.map((child) => child.nodeID)),
          });
        }
      }
      return exits;
    };

    connectSequence(roots);
    model.edges = edges;
    updateSyntaxEdgeStates();
  }

  function append(event = {}) {
    let changed = false;
    let revealed = false;
    model.maximumSequence = Math.max(model.maximumSequence, event.sequence || 0);
    markPath(event);
    if (event.node?.id) changed = addDefinition(event.node) || changed;

    switch (event.kind) {
      case "statement_started": {
        const node = syntaxNode(event);
        if (!node) break;
        if (!node.executed) {
          node.sequence = event.sequence || 0;
          revealed = true;
        }
        node.executed = true;
        node.executionCount++;
        node.state = node.state === "unresolved" ? "unresolved" : "executed";
        node.pathID = event.pathId || node.pathID;
        if (event.pathId && !node.pathIDs.includes(event.pathId)) node.pathIDs.push(event.pathId);
        node.reachedFromUnresolvedPath ||= unresolvedPaths.has(event.pathId);
        node.inputSnapshot = displaySnapshot(event.snapshot) || node.inputSnapshot;
        node.inputSnapshotTruncated ||= Boolean(event.snapshotTruncated);
        updateNode(node, event);
        lastByPath.set(event.pathId, node.nodeID);
        activeByPath.set(event.pathId, [...(activeByPath.get(event.pathId) || []), node.nodeID]);
        changed = true;
        break;
      }

      case "statement_finished": {
        const node = syntaxNode(event, true);
        if (!node) break;
        node.outputSnapshot = displaySnapshot(event.snapshot) || node.outputSnapshot;
        node.outputSnapshotTruncated ||= Boolean(event.snapshotTruncated);
        updateNode(node, event);
        removeActiveNode(activeByPath.get(event.pathId), node.nodeID);
        changed = true;
        break;
      }

      case "path_forked": {
        const parentPath = event.parentPathId || event.pathId;
        const node = visibleSyntaxNode(event.nodeId) || nodeByDefinitionID.get(lastByPath.get(parentPath));
        if (node) {
          node.forked = true;
          node.state = "unresolved";
          node.childPathIDs = [...new Set([...(node.childPathIDs || []), ...(event.childPathIds || [])])];
          changed = true;
        }
        for (const childPath of event.childPathIds || []) {
          unresolvedPaths.add(childPath);
          if (lastByPath.has(parentPath)) lastByPath.set(childPath, lastByPath.get(parentPath));
          activeByPath.set(childPath, [...(activeByPath.get(parentPath) || [])]);
          commandsByPath.set(childPath, [...(commandsByPath.get(parentPath) || [])]);
        }
        break;
      }

      case "command_started": {
        const node = syntaxNode(event, true);
        commandsByPath.set(event.pathId, [...(commandsByPath.get(event.pathId) || []), node?.nodeID || 0]);
        if (!node) break;
        if (!node.executed) {
          node.executed = true;
          node.executionCount++;
          node.sequence = event.sequence || 0;
          node.state = "executed";
          revealed = true;
        }
        node.commandCount++;
        node.pathID = event.pathId || node.pathID;
        if (event.pathId && !node.pathIDs.includes(event.pathId)) node.pathIDs.push(event.pathId);
        node.reachedFromUnresolvedPath ||= unresolvedPaths.has(event.pathId);
        node.invocation = event.invocation || node.invocation;
        updateNode(node, event);
        lastByPath.set(event.pathId, node.nodeID);
        changed = true;
        break;
      }

      case "command_finished": {
        const commandStack = commandsByPath.get(event.pathId) || [];
        const commandNodeID = commandStack.pop();
        commandsByPath.set(event.pathId, commandStack);
        const node = nodeByDefinitionID.get(commandNodeID) || syntaxNode(event, true);
        if (!node) break;
        node.commandResult = event.commandResult || node.commandResult;
        node.error = event.error || "";
        if (event.commandResult?.unresolved) node.state = "unresolved";
        updateNode(node, event);
        changed = true;
        break;
      }

      case "path_completed": {
        const node = nodeByDefinitionID.get(lastByPath.get(event.pathId));
        if (!node) break;
        node.pathCompleted = true;
        node.pathStatus = event.status;
        updateNode(node, event);
        changed = true;
        break;
      }
    }

    updateSyntaxEdgeStates();
    return { changed, revealed };
  }

  function finish(response = {}) {
    for (const definition of response.nodes || []) addDefinition(definition);
    model.peakLogicalBytes = Math.max(model.peakLogicalBytes, response.peakLogicalBytes || 0, peakLogicalBytes(model.nodes));
    model.truncated = Boolean(response.truncated);
    model.error = response.error || "";
    model.durationMicros = response.durationMicros || 0;
    updateSyntaxEdgeStates();
    return model;
  }

  for (const definition of [...initialDefinitions].sort((left, right) => left.id - right.id)) addDefinition(definition);
  return { model, append, finish };
}

function buildRuntimeFlowModel(response = {}) {
  const runtime = createRuntimeFlowModel(response.nodes || []);
  for (const event of [...(response.events || [])].sort((left, right) => left.sequence - right.sequence)) {
    runtime.append(event);
  }
  return runtime.finish(response);
}

export function createRuntimeFlowModel(nodes = []) {
  const definitions = new Map(nodes.map((node) => [node.id, node]));
  const model = {
    perspective: "runtime",
    nodes: [],
    edges: [],
    definitions,
    maximumSequence: 0,
    pathCount: 0,
    peakLogicalBytes: 0,
    truncated: false,
    error: "",
    durationMicros: 0,
  };
  const nodeByID = new Map();
  const callStacks = new Map();
  const lastByPath = new Map();
  const nextEdgeUnresolved = new Set();
  const paths = new Set();

  function append(event = {}) {
    let changed = false;
    if (event.node?.id) definitions.set(event.node.id, event.node);
    model.maximumSequence = Math.max(model.maximumSequence, event.sequence || 0);
    if (event.pathId) paths.add(event.pathId);
    for (const child of event.childPathIds || []) paths.add(child);
    model.pathCount = paths.size;
    model.peakLogicalBytes = Math.max(model.peakLogicalBytes, event.memory?.aggregateBytes || 0);
    switch (event.kind) {
      case "command_started": {
        const hidden = runtimeContainerCommand(event.invocation?.name);
        let occurrence = null;
        if (!hidden && event.invocation) {
          const syntaxDefinition = definitions.get(event.nodeId) || event.node || unknownDefinition(event.nodeId);
          occurrence = {
            id: `runtime:${event.sequence}`,
            nodeID: event.nodeId,
            pathID: event.pathId,
            sequence: event.sequence,
            endSequence: event.sequence,
            definition: {
              ...syntaxDefinition,
              kind: "command",
              snippet: invocationText(event.invocation),
            },
            state: "executed",
            executed: true,
            memory: event.memory || null,
            steps: event.steps || 0,
            status: event.status,
            invocation: event.invocation,
            commandResult: null,
            forked: false,
            inputSnapshot: null,
            inputSnapshotTruncated: false,
            outputSnapshot: null,
            outputSnapshotTruncated: false,
          };
          model.nodes.push(occurrence);
          nodeByID.set(occurrence.id, occurrence);
          appendExecutionEdge(model.edges, lastByPath, nextEdgeUnresolved, occurrence, event);
          changed = true;
        }
        const stack = [...(callStacks.get(event.pathId) || []), occurrence?.id || ""];
        callStacks.set(event.pathId, stack);
        break;
      }

      case "command_finished": {
        const stack = callStacks.get(event.pathId) || [];
        const occurrenceID = stack.pop();
        callStacks.set(event.pathId, stack);
        const occurrence = nodeByID.get(occurrenceID);
        if (occurrence) {
          occurrence.endSequence = event.sequence;
          occurrence.commandResult = event.commandResult || null;
          occurrence.memory = event.memory || occurrence.memory;
          occurrence.steps = event.steps || occurrence.steps;
          occurrence.error = event.error || "";
          if (event.commandResult?.unresolved) occurrence.state = "unresolved";
          changed = true;
        }
        break;
      }

      case "path_forked": {
        const parentPath = event.parentPathId || event.pathId;
        const previous = lastByPath.get(parentPath);
        if (previous && nodeByID.has(previous)) {
          const parent = nodeByID.get(previous);
          parent.forked = true;
          parent.state = "unresolved";
          parent.childPathIDs = [...(event.childPathIds || [])];
          changed = true;
        }
        for (const childPath of event.childPathIds || []) {
          if (previous) lastByPath.set(childPath, previous);
          callStacks.set(childPath, [...(callStacks.get(parentPath) || [])]);
          nextEdgeUnresolved.add(childPath);
        }
        break;
      }

      case "path_completed": {
        const occurrence = nodeByID.get(lastByPath.get(event.pathId));
        if (occurrence) {
          occurrence.pathCompleted = true;
          occurrence.pathStatus = event.status;
          occurrence.memory = event.memory || occurrence.memory;
          changed = true;
        }
        break;
      }
    }
    return changed;
  }

  function finish(response = {}) {
    for (const node of response.nodes || []) definitions.set(node.id, node);
    model.peakLogicalBytes = Math.max(model.peakLogicalBytes, response.peakLogicalBytes || 0, peakLogicalBytes(model.nodes));
    model.truncated = Boolean(response.truncated);
    model.error = response.error || "";
    model.durationMicros = response.durationMicros || 0;
    return model;
  }

  return {
    model,
    append,
    finish,
  };
}

export function advanceRuntimeFlow(runtime, events, start) {
  let next = start;
  let changed = false;
  let revealed = false;
  while (next < events.length) {
    const nodeCount = runtime.model.nodes.length;
    changed = runtime.append(events[next]) || changed;
    next++;
    if (runtime.model.nodes.length > nodeCount) {
      revealed = true;
      break;
    }
  }
  return { next, changed, revealed };
}

export function liveControlView(active, paused) {
  if (!active) {
    return { icon: "▶", label: "Run simulation", running: false, stopVisible: false };
  }
  if (paused) {
    return { icon: "▶", label: "Resume", running: true, stopVisible: true };
  }
  return { icon: "Ⅱ", label: "Pause", running: true, stopVisible: true };
}

function syntaxEdge(from, to, structural, flowGroup, exit = {}) {
  return {
    id: `ast-edge:${from.id}:${to.id}`,
    from: from.id,
    to: to.id,
    fromNodeID: from.nodeID,
    toNodeID: to.nodeID,
    pathID: 0,
    sequence: 0,
    state: "not-executed",
    structural,
    flowGroup,
    fallthrough: Boolean(exit.fallthrough),
    bypassedNodeIDs: exit.bypassedNodeIDs || [],
  };
}

function appendExecutionEdge(edges, lastByPath, nextEdgeUnresolved, occurrence, event) {
  const previous = lastByPath.get(event.pathId);
  if (previous && previous !== occurrence.id) {
    edges.push({
      id: `edge:${previous}:${occurrence.id}`,
      from: previous,
      to: occurrence.id,
      pathID: event.pathId,
      sequence: event.sequence,
      state: nextEdgeUnresolved.has(event.pathId) ? "unresolved" : "executed",
    });
  }
  nextEdgeUnresolved.delete(event.pathId);
  lastByPath.set(event.pathId, occurrence.id);
}

function runtimeContainerCommand(name) {
  return name === "eval" || name === "source" || name === ".";
}

function invocationText(invocation) {
  return [invocation.name, ...(invocation.args || []).map(formatInvocationArgument)].filter(Boolean).join(" ");
}

function formatInvocationArgument(argument) {
  if (!argument || Number(argument.kind) !== 0) return "<unresolved>";
  const value = String(argument.value || "");
  if (/^[A-Za-z0-9_./:@%+=,-]+$/.test(value)) return value;
  return JSON.stringify(value);
}

export function flowScrollTarget(node, viewport, resetTop = false) {
  const left = Math.max(0, node.left - (viewport.width - node.width) / 2);
  if (resetTop) return { left, top: 0 };

  const margin = Math.min(48, viewport.height / 4);
  let top = viewport.scrollTop;
  if (node.top < top + margin) {
    top = Math.max(0, node.top - margin);
  } else if (node.top + node.height > top + viewport.height - margin) {
    top = Math.max(0, node.top + node.height - viewport.height + margin);
  }
  return { left, top };
}

export function layoutASTFlowGraph(nodes, edges, minimumWidth = 720, minimumHeight = 460) {
  const nodeWidth = 196;
  const nodeHeight = 94;
  const horizontalGap = 48;
  const verticalPitch = 138;
  const verticalPadding = 40;
  const nodeByID = new Map(nodes.map((node) => [node.id, node]));
  const childIDs = new Set();
  const childGroups = new Map();
  for (const node of nodes) {
    const parentID = node.syntaxParentID !== undefined
      ? node.syntaxParentID
      : (node.definition.parentId ? `ast:${node.definition.parentId}` : null);
    if (!parentID || !nodeByID.has(parentID)) continue;
    childIDs.add(node.id);
    const groups = childGroups.get(parentID) || new Map();
    const group = Number(node.syntaxFlowGroup ?? node.definition.flowGroup ?? 0);
    groups.set(group, [...(groups.get(group) || []), node]);
    childGroups.set(parentID, groups);
  }

  function layoutSequence(sequence) {
    const layouts = sequence.map(layoutNode);
    const width = Math.max(nodeWidth, ...layouts.map((layout) => layout.width));
    const positions = new Map();
    let row = 0;
    for (const layout of layouts) {
      const left = (width - layout.width) / 2;
      for (const [id, position] of layout.positions) {
        positions.set(id, { x: position.x + left, row: position.row + row });
      }
      row += layout.rows;
    }
    return { width, rows: Math.max(1, row), positions };
  }

  function layoutNode(node) {
    const groups = [...(childGroups.get(node.id)?.entries() || [])]
      .sort(([left], [right]) => left - right)
      .map(([, children]) => layoutSequence(children));
    if (groups.length === 0) {
      return { width: nodeWidth, rows: 1, positions: new Map([[node.id, { x: 0, row: 0 }]]) };
    }

    const childrenWidth = groups.reduce((total, group) => total + group.width, 0) + horizontalGap * (groups.length - 1);
    const width = Math.max(nodeWidth, childrenWidth);
    const positions = new Map([[node.id, { x: (width - nodeWidth) / 2, row: 0 }]]);
    let left = (width - childrenWidth) / 2;
    let childRows = 0;
    for (const group of groups) {
      for (const [id, position] of group.positions) {
        positions.set(id, { x: position.x + left, row: position.row + 1 });
      }
      left += group.width + horizontalGap;
      childRows = Math.max(childRows, group.rows);
    }
    return { width, rows: childRows + 1, positions };
  }

  const roots = nodes.filter((node) => !childIDs.has(node.id));
  const graph = layoutSequence(roots);
  const horizontalPadding = Math.max(40, (minimumWidth - nodeWidth) / 2);
  const width = Math.max(minimumWidth, horizontalPadding * 2 + graph.width);
  const positions = new Map();
  for (const [id, position] of graph.positions) {
    positions.set(id, {
      x: horizontalPadding + position.x,
      y: verticalPadding + position.row * verticalPitch,
      lane: position.x,
      level: position.row,
    });
  }

  return {
    positions,
    nodeWidth,
    nodeHeight,
    width,
    height: Math.max(minimumHeight, verticalPadding * 2 + nodeHeight + Math.max(0, graph.rows - 1) * verticalPitch),
  };
}

export function layoutFlowGraph(nodes, edges, minimumWidth = 720, minimumHeight = 0) {
  const nodeWidth = 196;
  const nodeHeight = 94;
  const horizontalPitch = 244;
  const verticalPitch = 138;
  const horizontalPadding = Math.max(40, (minimumWidth - nodeWidth) / 2);
  const verticalPadding = 40;
  const visible = new Set(nodes.map((node) => node.id));
  const incoming = new Map();
  for (const edge of edges) {
    if (!visible.has(edge.from) || !visible.has(edge.to)) continue;
    const parents = incoming.get(edge.to) || [];
    parents.push(edge.from);
    incoming.set(edge.to, parents);
  }

  const levels = new Map();
  const pathLanes = new Map();
  const occupied = new Set();
  const positions = new Map();
  let nextLane = 0;
  let maximumLevel = 0;
  let maximumLane = 0;
  for (const node of nodes) {
    const parentIDs = incoming.get(node.id) || [];
    const parentLevel = Math.max(-1, ...parentIDs.map((id) => levels.get(id) ?? -1));
    const level = parentLevel + 1;
    levels.set(node.id, level);
    maximumLevel = Math.max(maximumLevel, level);

    let lane;
    if (node.pathID && pathLanes.has(node.pathID)) {
      lane = pathLanes.get(node.pathID);
    } else if (parentIDs.length && positions.has(parentIDs[0])) {
      lane = positions.get(parentIDs[0]).lane;
    } else {
      lane = nextLane;
    }
    while (occupied.has(`${level}:${lane}`)) lane++;
    if (node.pathID) pathLanes.set(node.pathID, lane);
    nextLane = Math.max(nextLane, lane + 1);
    occupied.add(`${level}:${lane}`);
    maximumLane = Math.max(maximumLane, lane);
    positions.set(node.id, {
      x: lane * horizontalPitch,
      y: verticalPadding + level * verticalPitch,
      lane,
      level,
    });
  }

  const contentWidth = nodeWidth + maximumLane * horizontalPitch;
  const width = Math.max(minimumWidth, horizontalPadding * 2 + contentWidth);
  const left = Math.max(horizontalPadding, (width - contentWidth) / 2);
  for (const position of positions.values()) {
    position.x += left;
  }

  return {
    positions,
    nodeWidth,
    nodeHeight,
    width,
    height: Math.max(460, minimumHeight, verticalPadding * 2 + (maximumLevel + 1) * nodeHeight + maximumLevel * (verticalPitch - nodeHeight)),
  };
}

export function nodeOutput(node = {}) {
  const result = node.commandResult;
  if (result?.outputCaptured) {
    return {
      stdout: String(result.stdout || ""),
      stdoutUnresolved: Boolean(result.stdoutUnresolved),
      stderr: String(result.stderr || ""),
      stderrUnresolved: Boolean(result.stderrUnresolved),
      exitCode: Number(result.exitCode || 0),
      exitCodeUnresolved: Boolean(result.exitCodeUnresolved),
      error: String(node.error || ""),
      truncated: Boolean(result.outputTruncated),
    };
  }

  const before = node.inputSnapshot || {};
  const after = node.outputSnapshot;
  if (!after) return null;
  const stdout = streamDelta(before.stdout, before.stdoutUnresolved, after.stdout, after.stdoutUnresolved);
  const stderr = streamDelta(before.stderr, before.stderrUnresolved, after.stderr, after.stderrUnresolved);
  const beforeError = String(before.error || "");
  const afterError = String(after.error || "");
  return {
    stdout: stdout.value,
    stdoutUnresolved: stdout.unresolved,
    stderr: stderr.value,
    stderrUnresolved: stderr.unresolved,
    exitCode: Number(after.exitCode || 0),
    exitCodeUnresolved: Boolean(after.exitCodeUnresolved),
    error: afterError === beforeError ? "" : afterError,
    truncated: Boolean(result?.outputTruncated || node.outputSnapshotTruncated),
  };
}

function streamDelta(beforeValue, beforeUnresolved, afterValue, afterUnresolved) {
  const before = String(beforeValue || "");
  const after = String(afterValue || "");
  const changed = before !== after;
  const value = after.startsWith(before) ? after.slice(before.length) : (changed ? after : "");
  return {
    value,
    unresolved: Boolean(afterUnresolved && (!beforeUnresolved || changed)),
  };
}

export function normalizeCommands(rows) {
  if (!Array.isArray(rows)) throw new Error("commands must be configured with command rows");
  const result = [];
  const seen = new Set();
  for (const row of rows) {
    const name = String(row?.name || "").trim();
    const outcome = String(row?.outcome || "resolved").toLowerCase();
    if (!name) throw new Error("command name is required");
    if (/\s/.test(name)) throw new Error(`command name ${JSON.stringify(name)} cannot contain whitespace`);
    if (seen.has(name)) throw new Error(`command ${JSON.stringify(name)} is registered more than once`);
    seen.add(name);
    switch (outcome) {
      case "resolved": {
        const exitCode = Number(row.exitCode ?? 0);
        if (!Number.isInteger(exitCode) || exitCode < 0 || exitCode > 255) {
          throw new Error(`exit code for ${name} must be an integer from 0 to 255`);
        }
        result.push({
          name,
          stdout: String(row.stdout || ""),
          stderr: String(row.stderr || ""),
          exitCode,
        });
        break;
      }
      case "error": {
        const message = String(row.error || "").trim();
        if (!message) throw new Error(`error message for ${name} is required`);
        result.push({ name, error: message });
        break;
      }
      case "javascript": {
        const source = String(row.javascript || "");
        if (!source.trim()) throw new Error(`JavaScript handler source for ${name} is required`);
        result.push({ name, javascript: source });
        break;
      }
      default:
        throw new Error(`unsupported outcome ${JSON.stringify(outcome)} for ${name}`);
    }
  }
  return result;
}

export function parseEnvironment(source) {
  const environment = {};
  for (const [index, rawLine] of String(source || "").split(/\r?\n/).entries()) {
    const line = rawLine.trim();
    if (!line || line.startsWith("#")) continue;
    const separator = line.indexOf("=");
    if (separator <= 0) throw new Error(`environment line ${index + 1} must use KEY=value`);
    const name = line.slice(0, separator).trim();
    if (!/^[A-Za-z_][A-Za-z0-9_]*$/.test(name)) {
      throw new Error(`environment line ${index + 1} has an invalid variable name`);
    }
    environment[name] = line.slice(separator + 1);
  }
  return environment;
}

export function parseArguments(source) {
  if (!String(source || "").trim()) return [];
  const value = JSON.parse(source);
  if (!Array.isArray(value) || value.some((argument) => typeof argument !== "string")) {
    throw new Error("arguments must be a JSON array of strings");
  }
  return value;
}

export function concreteDisplayValue(value) {
  return value == null ? "" : String(value);
}

export function containsJavaScriptCommands(commands) {
  return Array.isArray(commands) && commands.some((command) => command?.outcome === "javascript");
}

export function startupMode(sharedStateRestored, commands) {
  return sharedStateRestored && containsJavaScriptCommands(commands) ? "review" : "ready";
}

export function formatBytes(value) {
  const bytes = Math.max(0, Number(value) || 0);
  if (bytes < 1024) return `${Math.round(bytes)} B`;
  const units = ["KiB", "MiB", "GiB"];
  let scaled = bytes;
  let unit = -1;
  do {
    scaled /= 1024;
    unit++;
  } while (scaled >= 1024 && unit < units.length - 1);
  const digits = scaled >= 10 || Number.isInteger(scaled) ? 0 : 1;
  return `${scaled.toFixed(digits)} ${units[unit]}`;
}

const bashKeywords = new Set([
  "!", "[[", "]]", "case", "coproc", "do", "done", "elif", "else", "esac",
  "fi", "for", "function", "if", "in", "select", "then", "time", "until", "while",
]);
const commandStartingKeywords = new Set(["do", "elif", "else", "if", "then", "until", "while"]);
const bashOperators = [";;&", "<<<", "&&", "||", ";;", ";&", "<<", ">>", ">&", "<&", "==", "!=", "=~", "<=", ">=", "((", "))"];

export function tokenizeBash(source) {
  const value = String(source || "");
  const tokens = [];
  let index = 0;
  let commandExpected = true;
  while (index < value.length) {
    const character = value[index];
    if (/\s/.test(character)) {
      const end = consumeWhile(value, index, (current) => /\s/.test(current));
      const whitespace = value.slice(index, end);
      appendToken(tokens, "plain", whitespace);
      if (whitespace.includes("\n")) commandExpected = true;
      index = end;
      continue;
    }
    if (character === "#" && commentStartsAt(value, index)) {
      const newline = value.indexOf("\n", index);
      const end = newline < 0 ? value.length : newline;
      appendToken(tokens, "comment", value.slice(index, end));
      index = end;
      continue;
    }
    if (character === "'") {
      const end = quotedEnd(value, index, "'", false);
      appendToken(tokens, "string", value.slice(index, end));
      index = end;
      continue;
    }
    if (character === '"') {
      index = tokenizeDoubleQuoted(value, index, tokens);
      continue;
    }
    if (character === "`") {
      const end = quotedEnd(value, index, "`", true);
      appendToken(tokens, "variable", value.slice(index, end));
      index = end;
      continue;
    }
    if (character === "$") {
      const end = expansionEnd(value, index);
      appendToken(tokens, "variable", value.slice(index, end));
      index = end;
      continue;
    }
    const operator = operatorAt(value, index);
    if (operator) {
      const kind = bashKeywords.has(operator) ? "keyword" : "operator";
      appendToken(tokens, kind, operator);
      if ([";", ";;", ";&", ";;&", "&&", "||", "|", "&"].includes(operator)) commandExpected = true;
      if (operator === "[[" || operator === "]]" || operator === "((" || operator === "))") commandExpected = false;
      index += operator.length;
      continue;
    }

    const end = consumeWhile(value, index, (current, position) =>
      !/\s/.test(current) && current !== "'" && current !== '"' && current !== "`" &&
      current !== "$" && !operatorAt(value, position));
    const word = value.slice(index, Math.max(index + 1, end));
    let kind = "plain";
    if (bashKeywords.has(word)) {
      kind = "keyword";
      commandExpected = commandStartingKeywords.has(word);
    } else if (/^[A-Za-z_][A-Za-z0-9_]*(?:\[[^\]]+\])?=/.test(word)) {
      kind = "assignment";
    } else if (/^(?:0[xX][0-9A-Fa-f]+|[0-9]+)$/.test(word)) {
      kind = "number";
      commandExpected = false;
    } else if (commandExpected && !word.startsWith("-")) {
      kind = "command";
      commandExpected = false;
    } else {
      commandExpected = false;
    }
    appendToken(tokens, kind, word);
    index += word.length;
  }
  return tokens;
}

function tokenizeDoubleQuoted(source, start, tokens) {
  let index = start;
  let literalStart = start;
  index++;
  while (index < source.length) {
    if (source[index] === "\\") {
      index = Math.min(source.length, index + 2);
      continue;
    }
    if (source[index] === "$" || source[index] === "`") {
      appendToken(tokens, "string", source.slice(literalStart, index));
      const end = source[index] === "$" ? expansionEnd(source, index) : quotedEnd(source, index, "`", true);
      appendToken(tokens, "variable", source.slice(index, end));
      index = end;
      literalStart = index;
      continue;
    }
    if (source[index] === '"') {
      index++;
      appendToken(tokens, "string", source.slice(literalStart, index));
      return index;
    }
    index++;
  }
  appendToken(tokens, "string", source.slice(literalStart));
  return source.length;
}

function expansionEnd(source, start) {
  if (source.startsWith("$(", start)) return balancedEnd(source, start + 1, "(", ")");
  if (source.startsWith("${", start)) return balancedEnd(source, start + 1, "{", "}");
  const match = source.slice(start + 1).match(/^(?:[A-Za-z_][A-Za-z0-9_]*|[0-9]+|[-#?$!@*])/);
  return match ? start + 1 + match[0].length : start + 1;
}

function balancedEnd(source, openingIndex, opening, closing) {
  let depth = 0;
  for (let index = openingIndex; index < source.length; index++) {
    if (source[index] === "\\") {
      index++;
      continue;
    }
    if (source[index] === "'" || source[index] === '"') {
      index = quotedEnd(source, index, source[index], source[index] === '"') - 1;
      continue;
    }
    if (source[index] === opening) depth++;
    if (source[index] === closing && --depth === 0) return index + 1;
  }
  return source.length;
}

function quotedEnd(source, start, quote, escaped) {
  for (let index = start + 1; index < source.length; index++) {
    if (escaped && source[index] === "\\") {
      index++;
      continue;
    }
    if (source[index] === quote) return index + 1;
  }
  return source.length;
}

function operatorAt(source, index) {
  for (const operator of bashOperators) {
    if (source.startsWith(operator, index)) return operator;
  }
  return ";|&()<>".includes(source[index]) ? source[index] : "";
}

function commentStartsAt(source, index) {
  return index === 0 || /[\s;|&()]/.test(source[index - 1]);
}

function consumeWhile(source, start, predicate) {
  let index = start;
  while (index < source.length && predicate(source[index], index)) index++;
  return index;
}

function appendToken(tokens, kind, value) {
  if (!value) return;
  const previous = tokens[tokens.length - 1];
  if (previous?.kind === kind) previous.value += value;
  else tokens.push({ kind, value });
}

function displaySnapshot(snapshot) {
  if (!snapshot || !Array.isArray(snapshot.variables)) return snapshot || null;
  const variables = snapshot.variables.filter((variable) => variable?.name !== "OPTIND");
  return variables.length === snapshot.variables.length ? snapshot : { ...snapshot, variables };
}

function unknownDefinition(nodeID) {
  return {
    id: nodeID,
    parentId: 0,
    kind: "statement",
    snippet: `node ${nodeID}`,
    source: { name: "command.sh", line: 0, column: 0, endLine: 0, endColumn: 0 },
  };
}

function removeActiveNode(active, nodeID) {
  if (!active) return;
  const index = active.lastIndexOf(nodeID);
  if (index >= 0) active.splice(index, 1);
}

function peakLogicalBytes(nodes) {
  let peak = 0;
  for (const node of nodes) peak = Math.max(peak, node.memory?.aggregateBytes || 0);
  return peak;
}
