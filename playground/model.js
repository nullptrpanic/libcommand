import { commandResultUnresolved, invocationText } from "./presentation.js";

// Keep existing model imports compatible while pure helpers live with their owners.
export * from "./presentation.js";
export * from "./layout.js";
export { tokenizeBash } from "./highlight.js";

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
  const nodes = response.nodes || [];
  const ast = createASTFlowModel(Number.isInteger(response.astNodeCount)
    ? nodes.slice(0, response.astNodeCount)
    : nodes);
  for (const event of [...(response.events || [])].sort((left, right) => left.sequence - right.sequence)) {
    ast.append(event);
  }
  return ast.finish(response);
}

export function createASTFlowModel(initialDefinitions = []) {
  const definitions = new Map();
  const nodeByDefinitionID = new Map();
  const visibleNodeByDefinitionID = new Map();
  const activeByPath = new Map();
  const commandsByPath = new Map();
  const lastByPath = new Map();
  const unresolvedPaths = new Set();
  const observedTransitions = new Map();
  const paths = new Set();
  let edgeByTransition = new Map();
  let topologyDirty = false;
  let topologyFrozen = false;
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
      topologyDirty = true;
      return true;
    }
    if (existing) {
      const topologyChanged = existing.definition.parentId !== definition.parentId
        || existing.definition.flowGroup !== definition.flowGroup
        || existing.definition.flowCanSkip !== definition.flowCanSkip
        || existing.definition.flowCommand !== definition.flowCommand
        || existing.definition.flowFunction !== definition.flowFunction
        || existing.definition.flowGroupExit !== definition.flowGroupExit
        || existing.definition.flowGroupDefault !== definition.flowGroupDefault;
      existing.definition = definition;
      topologyDirty ||= topologyChanged;
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
      detections: [],
      forked: false,
      inputSnapshot: null,
      inputSnapshotTruncated: false,
      outputSnapshot: null,
      outputSnapshotTruncated: false,
      syntaxParentID: null,
      syntaxFlowGroup: 0,
    };
    model.nodes.push(syntaxNode);
    nodeByDefinitionID.set(definition.id, syntaxNode);
    topologyDirty = true;
    return true;
  }

  function addDefinitions(items) {
    let changed = false;
    for (const definition of items || []) changed = addDefinition(definition) || changed;
    return changed;
  }

  function syntaxNode(event, fallbackToActive = false) {
    if (!topologyFrozen && event.nodeId && !nodeByDefinitionID.has(event.nodeId)) {
      addDefinition(event.node || definitions.get(event.nodeId) || unknownDefinition(event.nodeId));
    }
    if (definitions.get(event.nodeId)?.embedded) return null;
    const direct = nodeByDefinitionID.get(event.nodeId);
    if (direct || !fallbackToActive) return direct || null;
    const active = activeByPath.get(event.pathId) || [];
    return nodeByDefinitionID.get(active.at(-1)) || nodeByDefinitionID.get(lastByPath.get(event.pathId)) || null;
  }

  function visibleSyntaxNode(nodeID) {
    ensureTopology();
    return visibleNodeByDefinitionID.get(nodeID) || null;
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

  function rebuildSyntaxEdges() {
    model.nodes.sort((left, right) => left.nodeID - right.nodeID);
    visibleNodeByDefinitionID.clear();
    const edges = [];
    const edgeByPair = new Map();
    const roots = [];
    const childGroups = new Map();
    const visibleParentByDefinitionID = new Map();
    const orderedDefinitions = [...definitions.values()].sort((left, right) => left.id - right.id);
    for (const definition of orderedDefinitions) {
      const directParent = nodeByDefinitionID.get(definition.parentId);
      const inherited = visibleParentByDefinitionID.get(definition.parentId);
      const parent = directParent
        ? { node: directParent, flowGroup: Number(definition.flowGroup || 0) }
        : inherited || null;
      visibleParentByDefinitionID.set(definition.id, parent);
      visibleNodeByDefinitionID.set(definition.id, nodeByDefinitionID.get(definition.id) || parent?.node || null);
    }

    for (const node of model.nodes) {
      const parent = visibleParentByDefinitionID.get(node.nodeID);
      if (parent) {
        node.syntaxParentID = parent.node.id;
        node.syntaxFlowGroup = parent.flowGroup;
        const groups = childGroups.get(parent.node.id) || new Map();
        const children = groups.get(parent.flowGroup) || [];
        children.push(node);
        groups.set(parent.flowGroup, children);
        childGroups.set(parent.node.id, groups);
      } else {
        node.syntaxParentID = null;
        node.syntaxFlowGroup = 0;
        roots.push(node);
      }
    }

    const addEdge = (from, to, options = {}) => {
      if (!from || !to) return null;
      const key = transitionKey(from.nodeID, to.nodeID);
      const existing = edgeByPair.get(key);
      if (existing) {
        existing.structural ||= Boolean(options.structural);
        existing.fallthrough ||= Boolean(options.fallthrough);
        existing.backEdge ||= Boolean(options.backEdge);
        existing.caseFallthrough ||= Boolean(options.caseFallthrough);
        return existing;
      }
      const edge = syntaxEdge(from, to, Boolean(options.structural), Number(options.flowGroup || 0), options);
      edges.push(edge);
      edgeByPair.set(key, edge);
      return edge;
    };

    const flowByNodeID = new Map(model.nodes.map((node) => [node.nodeID, leafSyntaxFlow(node, definitions)]));
    const functionBodies = new Map();
    for (let index = model.nodes.length - 1; index >= 0; index--) {
      const node = model.nodes[index];
      const groups = [...(childGroups.get(node.id)?.entries() || [])]
        .sort(([left], [right]) => left - right)
        .map(([group, children]) => ({
          group,
          children,
          flow: compileSyntaxSequence(children, flowByNodeID, addEdge),
          exit: children[0]?.definition.flowGroupExit || "",
          isDefault: children.some((child) => child.definition.flowGroupDefault),
        }));
      let flow = flowByNodeID.get(node.nodeID);

      if (node.definition.kind === "function") {
        if (groups[0]?.flow.entry && node.definition.flowFunction) {
          const declarations = functionBodies.get(node.definition.flowFunction) || [];
          declarations.push({ declaration: node, flow: groups[0].flow });
          functionBodies.set(node.definition.flowFunction, declarations);
        }
      } else if (node.definition.kind === "loop" && groups[0]?.flow.entry) {
        const body = groups[0].flow;
        addEdge(node, body.entry, { structural: true, flowGroup: groups[0].group });
        flow = {
          entry: node,
          normal: [...body.normal, ...body.breaks, ...body.continues],
          breaks: [],
          continues: [],
          returns: body.returns,
        };
      } else if (groups.length !== 0 && isCaseFlow(groups)) {
        flow = compileCaseFlow(node, groups, addEdge);
      } else if (groups.length !== 0 && node.definition.kind === "condition") {
        flow = branchSyntaxFlow(node, groups, addEdge);
      } else if (groups[0]?.flow.entry) {
        addEdge(node, groups[0].flow.entry, { structural: true, flowGroup: groups[0].group });
        flow = {
          entry: node,
          normal: groups[0].flow.normal,
          breaks: groups[0].flow.breaks,
          continues: groups[0].flow.continues,
          returns: groups[0].flow.returns,
        };
      }
      flowByNodeID.set(node.nodeID, flow);
    }

    compileSyntaxSequence(roots, flowByNodeID, addEdge);
    patchFunctionCalls(model.nodes, functionBodies, edges, edgeByPair, addEdge, definitions);
    model.edges = edges;
    edgeByTransition = edgeByPair;
    updateSyntaxEdgeStates(model.edges, observedTransitions);
    topologyDirty = false;
  }

  function ensureTopology() {
    if (topologyDirty) rebuildSyntaxEdges();
  }

  function recordTransition(pathID, toNodeID, sequence) {
    const fromNodeID = lastByPath.get(pathID);
    lastByPath.set(pathID, toNodeID);
    if (!fromNodeID || !toNodeID || fromNodeID === toNodeID) return;
    const key = transitionKey(fromNodeID, toNodeID);
    const edge = edgeByTransition.get(key);
    if (!edge) return;
    const unresolved = unresolvedPaths.has(pathID);
    const existing = observedTransitions.get(key);
    if (existing) {
      existing.unresolved &&= unresolved;
      existing.sequence = Math.max(existing.sequence, sequence || 0);
      existing.pathID = pathID || existing.pathID;
    } else {
      observedTransitions.set(key, { key, fromNodeID, toNodeID, unresolved, sequence: sequence || 0, pathID });
    }
    applyObservedTransition(edge, observedTransitions.get(key));
  }

  function append(event = {}) {
    let changed = false;
    let revealed = false;
    let activated = false;
    model.maximumSequence = Math.max(model.maximumSequence, event.sequence || 0);
    markPath(event);
    if (event.kind !== "node_discovered" && event.kind !== "simulation_started") topologyFrozen = true;
    if (!topologyFrozen && event.node?.id) changed = addDefinition(event.node) || changed;
    if (event.kind !== "node_discovered") ensureTopology();

    switch (event.kind) {
      case "statement_started": {
        const node = syntaxNode(event);
        if (!node) break;
        if (!node.executed) {
          node.sequence = event.sequence || 0;
          revealed = true;
        }
        node.executed = true;
        activated = true;
        node.executionCount++;
        node.state = node.state === "unresolved" ? "unresolved" : "executed";
        node.pathID = event.pathId || node.pathID;
        if (event.pathId && !node.pathIDs.includes(event.pathId)) node.pathIDs.push(event.pathId);
        node.reachedFromUnresolvedPath ||= unresolvedPaths.has(event.pathId);
        node.inputSnapshot = displaySnapshot(event.snapshot) || node.inputSnapshot;
        node.inputSnapshotTruncated ||= Boolean(event.snapshotTruncated);
        updateNode(node, event);
        recordTransition(event.pathId, node.nodeID, event.sequence);
        activeByPath.set(event.pathId, [...(activeByPath.get(event.pathId) || []), node.nodeID]);
        changed = true;
        break;
      }

      case "statement_activated": {
        const node = syntaxNode(event, true);
        if (!node) break;
        activated = true;
        node.executed = true;
        node.executionCount++;
        node.state = node.state === "unresolved" ? "unresolved" : "executed";
        node.pathID = event.pathId || node.pathID;
        if (event.pathId && !node.pathIDs.includes(event.pathId)) node.pathIDs.push(event.pathId);
        node.reachedFromUnresolvedPath ||= unresolvedPaths.has(event.pathId);
        node.inputSnapshot = displaySnapshot(event.snapshot) || node.inputSnapshot;
        node.inputSnapshotTruncated ||= Boolean(event.snapshotTruncated);
        updateNode(node, event);
        recordTransition(event.pathId, node.nodeID, event.sequence);
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
        const parentPath = event.pathId;
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
        if (commandResultUnresolved(event.commandResult)) node.state = "unresolved";
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

    ensureTopology();
    return { changed, revealed, activated };
  }

  function finish(response = {}) {
    ensureTopology();
    for (const node of model.nodes) node.detections = [];
    for (const detection of response.detections || []) {
      const node = visibleSyntaxNode(detection.nodeId);
      if (node) node.detections.push(detection);
    }
    model.peakLogicalBytes = Math.max(model.peakLogicalBytes, response.peakLogicalBytes || 0, peakLogicalBytes(model.nodes));
    model.truncated = Boolean(response.truncated);
    model.error = response.error || "";
    model.durationMicros = response.durationMicros || 0;
    updateSyntaxEdgeStates(model.edges, observedTransitions);
    return model;
  }

  function appendDetection(detection) {
    return attachDetection(visibleSyntaxNode(detection?.nodeId), detection);
  }

  addDefinitions([...initialDefinitions].sort((left, right) => left.id - right.id));
  ensureTopology();
  return { model, append, appendDetection, finish };
}

function transitionKey(fromNodeID, toNodeID) {
  return `${fromNodeID}:${toNodeID}`;
}

function leafSyntaxFlow(node, definitions) {
  switch (node.definition.flowCommand) {
    case "break":
      if (!syntaxHasAncestor(node, definitions, (ancestor) => ancestor.kind === "loop")) break;
      return { entry: node, normal: [], breaks: [{ node }], continues: [], returns: [] };
    case "continue":
      if (!syntaxHasAncestor(node, definitions, (ancestor) => ancestor.kind === "loop")) break;
      return { entry: node, normal: [], breaks: [], continues: [{ node }], returns: [] };
    case "return":
      if (!syntaxHasAncestor(node, definitions, (ancestor) => ancestor.kind === "function"
        || ancestor.flowCommand === "source" || ancestor.flowCommand === ".")) break;
      return { entry: node, normal: [], breaks: [], continues: [], returns: [{ node }] };
    case "exit":
      return { entry: node, normal: [], breaks: [], continues: [], returns: [] };
  }
  return { entry: node, normal: [{ node }], breaks: [], continues: [], returns: [] };
}

function syntaxHasAncestor(node, definitions, matches) {
  if (!definitions) return true;
  for (let parentID = node.definition.parentId; parentID; parentID = definitions.get(parentID)?.parentId) {
    const ancestor = definitions.get(parentID);
    if (ancestor && matches(ancestor)) return true;
  }
  return false;
}

function compileSyntaxSequence(sequence, flowByNodeID, addEdge) {
  let entry = null;
  let normal = [];
  const breaks = [];
  const continues = [];
  const returns = [];
  let reachable = true;
  for (const node of sequence) {
    const flow = flowByNodeID.get(node.nodeID) || leafSyntaxFlow(node);
    if (!entry) entry = flow.entry;
    if (!reachable) continue;
    for (const exit of normal) addEdge(exit.node, flow.entry, exit);
    breaks.push(...flow.breaks);
    continues.push(...flow.continues);
    returns.push(...flow.returns);
    normal = flow.normal;
    reachable = normal.length !== 0;
  }
  return { entry, normal, breaks, continues, returns };
}

function branchSyntaxFlow(node, groups, addEdge) {
  const flow = { entry: node, normal: [], breaks: [], continues: [], returns: [] };
  for (const group of groups) {
    if (!group.flow.entry) continue;
    addEdge(node, group.flow.entry, { structural: true, flowGroup: group.group });
    flow.normal.push(...group.flow.normal);
    flow.breaks.push(...group.flow.breaks);
    flow.continues.push(...group.flow.continues);
    flow.returns.push(...group.flow.returns);
  }
  if (node.definition.flowCanSkip) {
    flow.normal.push({
      node,
      fallthrough: true,
      bypassedNodeIDs: groups.flatMap((group) => group.children.map((child) => child.nodeID)),
    });
  }
  return flow;
}

function isCaseFlow(groups) {
  return groups.some((group) => group.exit || group.isDefault);
}

function compileCaseFlow(node, groups, addEdge) {
  const flow = { entry: node, normal: [], breaks: [], continues: [], returns: [] };
  for (const group of groups) {
    if (group.flow.entry) addEdge(node, group.flow.entry, { structural: true, flowGroup: group.group });
    flow.breaks.push(...group.flow.breaks);
    flow.continues.push(...group.flow.continues);
    flow.returns.push(...group.flow.returns);
  }
  for (let index = 0; index < groups.length; index++) {
    const group = groups[index];
    const next = groups[index + 1];
    if (group.exit === ";&" && next?.flow.entry) {
      for (const exit of group.flow.normal) addEdge(exit.node, next.flow.entry, { caseFallthrough: true });
      continue;
    }
    if (group.exit === ";;&") {
      for (let candidate = index + 1; candidate < groups.length; candidate++) {
        if (!groups[candidate].flow.entry) continue;
        for (const exit of group.flow.normal) addEdge(exit.node, groups[candidate].flow.entry, { caseFallthrough: true });
      }
      if (!groups.slice(index + 1).some((candidate) => candidate.isDefault)) {
        flow.normal.push(...group.flow.normal);
      }
      continue;
    }
    flow.normal.push(...group.flow.normal);
  }
  if (node.definition.flowCanSkip) {
    flow.normal.push({
      node,
      fallthrough: true,
      bypassedNodeIDs: groups.flatMap((group) => group.children.map((child) => child.nodeID)),
    });
  }
  return flow;
}

function patchFunctionCalls(nodes, functionBodies, edges, edgeByPair, addEdge, definitions) {
  for (const declarations of functionBodies.values()) {
    declarations.sort((left, right) => left.declaration.nodeID - right.declaration.nodeID);
  }
  const nodeByID = new Map(nodes.map((node) => [node.nodeID, node]));
  const outgoingByNodeID = new Map();
  for (const edge of edges) {
    const outgoing = outgoingByNodeID.get(edge.fromNodeID) || [];
    outgoing.push(edge);
    outgoingByNodeID.set(edge.fromNodeID, outgoing);
  }
  const patches = [];
  const removed = new Set();
  for (const call of nodes) {
    const name = call.definition.flowCommand;
    if (!name || !functionBodies.has(name)) continue;
    const declarations = functionBodies.get(name);
    const target = precedingFunctionDeclaration(declarations, call.nodeID);
    if (!target?.flow.entry) continue;
    const outgoing = outgoingByNodeID.get(call.nodeID) || [];
    for (const edge of outgoing) {
      removed.add(edge);
      edgeByPair.delete(transitionKey(edge.fromNodeID, edge.toNodeID));
    }
    patches.push({ call, target, outgoing });
  }
  if (removed.size) {
    let retained = 0;
    for (const edge of edges) {
      if (!removed.has(edge)) edges[retained++] = edge;
    }
    edges.length = retained;
  }
  for (const { call, target, outgoing } of patches) {
    addEdge(call, target.flow.entry, {
      structural: true,
      backEdge: syntaxDescendsFrom(call.nodeID, target.declaration.nodeID, definitions),
    });
    for (const continuation of outgoing) {
      const next = nodeByID.get(continuation.toNodeID);
      for (const exit of [...target.flow.normal, ...target.flow.returns]) {
        addEdge(exit.node, next, continuation);
      }
    }
  }
}

function precedingFunctionDeclaration(declarations, nodeID) {
  let low = 0;
  let high = declarations.length;
  while (low < high) {
    const middle = (low + high) >> 1;
    if (declarations[middle].declaration.nodeID < nodeID) low = middle + 1;
    else high = middle;
  }
  return low ? declarations[low - 1] : null;
}

function syntaxDescendsFrom(nodeID, ancestorID, definitions) {
  for (let current = definitions.get(nodeID)?.parentId; current; current = definitions.get(current)?.parentId) {
    if (current === ancestorID) return true;
  }
  return false;
}

function updateSyntaxEdgeStates(edges, observedTransitions) {
  for (const edge of edges) {
    const transition = observedTransitions.get(transitionKey(edge.fromNodeID, edge.toNodeID));
    if (transition) applyObservedTransition(edge, transition);
    else edge.state = "not-executed";
  }
}

function applyObservedTransition(edge, transition) {
  edge.pathID = transition.pathID || 0;
  edge.sequence = transition.sequence || 0;
  edge.state = transition.unresolved ? "unresolved" : "executed";
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
  const pendingDetections = new Map();
  const paths = new Set();

  function appendOccurrence(event, definition, overrides = {}) {
    const occurrence = {
      id: `runtime:${event.sequence}`,
      nodeID: event.nodeId,
      pathID: event.pathId,
      sequence: event.sequence,
      endSequence: event.sequence,
      definition: { ...definition, ...overrides },
      state: "executed",
      executed: true,
      executionCount: 1,
      memory: event.memory || null,
      steps: event.steps || 0,
      status: event.status,
      invocation: event.invocation || null,
      commandResult: null,
      detections: [],
      forked: false,
      inputSnapshot: displaySnapshot(event.snapshot),
      inputSnapshotTruncated: Boolean(event.snapshotTruncated),
      outputSnapshot: null,
      outputSnapshotTruncated: false,
    };
    model.nodes.push(occurrence);
    nodeByID.set(occurrence.id, occurrence);
    for (const detection of pendingDetections.get(event.sequence) || []) {
      attachDetection(occurrence, detection);
    }
    pendingDetections.delete(event.sequence);
    appendExecutionEdge(model.edges, lastByPath, nextEdgeUnresolved, occurrence, event);
    return occurrence;
  }

  function appendDetection(detection) {
    const occurrence = nodeByID.get(`runtime:${detection?.sequence}`);
    if (occurrence) return attachDetection(occurrence, detection);
    if (!detection?.sequence) return false;
    const pending = pendingDetections.get(detection.sequence) || [];
    if (!pending.some((item) => sameDetection(item, detection))) pending.push(detection);
    pendingDetections.set(detection.sequence, pending);
    return false;
  }

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
          occurrence = appendOccurrence(event, syntaxDefinition, {
            kind: "command",
            snippet: invocationText(event.invocation),
          });
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
          if (commandResultUnresolved(event.commandResult)) occurrence.state = "unresolved";
          changed = true;
        }
        break;
      }

      case "path_forked": {
        const parentPath = event.pathId;
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
    for (const node of model.nodes) node.detections = [];
    for (const detection of response.detections || []) {
      const node = nodeByID.get(`runtime:${detection.sequence}`);
      if (node) node.detections.push(detection);
    }
    model.peakLogicalBytes = Math.max(model.peakLogicalBytes, response.peakLogicalBytes || 0, peakLogicalBytes(model.nodes));
    model.truncated = Boolean(response.truncated);
    model.error = response.error || "";
    model.durationMicros = response.durationMicros || 0;
    return model;
  }

  return {
    model,
    append,
    appendDetection,
    finish,
  };
}

function attachDetection(node, detection) {
  if (!node || !detection || node.detections.some((item) => sameDetection(item, detection))) return false;
  node.detections.push(detection);
  return true;
}

function sameDetection(left, right) {
  return left.sequence === right.sequence
    && left.nodeId === right.nodeId
    && left.pathId === right.pathId
    && left.command === right.command
    && left.type === right.type;
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
    backEdge: Boolean(exit.backEdge),
    caseFallthrough: Boolean(exit.caseFallthrough),
    observed: Boolean(exit.observed),
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
