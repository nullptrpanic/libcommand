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

export function flowEdgePath(edge, from, to, layout) {
  const fromX = from.x + layout.nodeWidth / 2;
  const fromY = from.y + layout.nodeHeight;
  const toX = to.x + layout.nodeWidth / 2;
  const toY = to.y;
  const route = layout.edgeRoutes?.get(edge.id);
  if (route) {
    const startY = fromY + (route.backEdge ? 18 : 24);
    const endY = toY - (route.backEdge ? 18 : 24);
    return `M ${fromX} ${fromY} C ${fromX} ${startY}, ${route.railX} ${startY}, ${route.railX} ${startY} L ${route.railX} ${endY} C ${route.railX} ${endY}, ${toX} ${endY}, ${toX} ${toY}`;
  }
  if (edge.backEdge) {
    const railX = 16;
    return `M ${fromX} ${fromY} C ${fromX} ${fromY + 24}, ${railX} ${fromY + 24}, ${railX} ${fromY + 24} L ${railX} ${toY - 24} C ${railX} ${toY - 24}, ${toX} ${toY - 24}, ${toX} ${toY}`;
  }
  const bend = Math.max(30, (toY - fromY) * .5);
  return `M ${fromX} ${fromY} C ${fromX} ${fromY + bend}, ${toX} ${toY - bend}, ${toX} ${toY}`;
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
    const children = groups.get(group) || [];
    children.push(node);
    groups.set(group, children);
    childGroups.set(parentID, groups);
  }
  const roots = nodes.filter((node) => !childIDs.has(node.id));
  const subtreeWidth = new Map();
  const subtreeRows = new Map();
  const groupLayout = new Map();
  for (let index = nodes.length - 1; index >= 0; index--) {
    const node = nodes[index];
    const groups = [...(childGroups.get(node.id)?.entries() || [])]
      .sort(([left], [right]) => left - right)
      .map(([group, children]) => {
        const width = Math.max(nodeWidth, ...children.map((child) => subtreeWidth.get(child.id) || nodeWidth));
        const rows = Math.max(1, children.reduce((total, child) => total + (subtreeRows.get(child.id) || 1), 0));
        return { group, children, width, rows };
      });
    groupLayout.set(node.id, groups);
    const childrenWidth = groups.length
      ? groups.reduce((total, group) => total + group.width, 0) + horizontalGap * (groups.length - 1)
      : 0;
    const sideBranchWidth = optionalSideBranch(node, groups)
      ? nodeWidth + 2 * (horizontalGap + groups[0].width)
      : 0;
    subtreeWidth.set(node.id, Math.max(nodeWidth, childrenWidth, sideBranchWidth));
    subtreeRows.set(node.id, 1 + Math.max(0, ...groups.map((group) => group.rows)));
  }

  const graphWidth = Math.max(nodeWidth, ...roots.map((node) => subtreeWidth.get(node.id) || nodeWidth));
  const graphRows = Math.max(1, roots.reduce((total, node) => total + (subtreeRows.get(node.id) || 1), 0));
  const syntaxPositions = new Map();
  const tasks = [{ type: "sequence", nodes: roots, left: 0, width: graphWidth, row: 0 }];
  while (tasks.length) {
    const task = tasks.pop();
    if (task.type === "sequence") {
      let row = task.row;
      const placements = [];
      for (const node of task.nodes) {
        const width = subtreeWidth.get(node.id) || nodeWidth;
        placements.push({ type: "node", node, left: task.left + (task.width - width) / 2, width, row });
        row += subtreeRows.get(node.id) || 1;
      }
      for (let index = placements.length - 1; index >= 0; index--) tasks.push(placements[index]);
      continue;
    }

    syntaxPositions.set(task.node.id, {
      x: task.left + (task.width - nodeWidth) / 2,
      row: task.row,
    });
    const groups = groupLayout.get(task.node.id) || [];
    if (!groups.length) continue;
    if (optionalSideBranch(task.node, groups)) {
      const nodeLeft = task.left + (task.width - nodeWidth) / 2;
      tasks.push({
        type: "sequence",
        nodes: groups[0].children,
        left: nodeLeft + nodeWidth + horizontalGap,
        width: groups[0].width,
        row: task.row + 1,
      });
      continue;
    }
    const childrenWidth = groups.reduce((total, group) => total + group.width, 0) + horizontalGap * (groups.length - 1);
    const left = task.left + (task.width - childrenWidth) / 2;
    const groupOffsets = [];
    let offset = 0;
    for (const group of groups) {
      groupOffsets.push(offset);
      offset += group.width + horizontalGap;
    }
    for (let index = groups.length - 1; index >= 0; index--) {
      const group = groups[index];
      tasks.push({ type: "sequence", nodes: group.children, left: left + groupOffsets[index], width: group.width, row: task.row + 1 });
    }
  }

  const levels = new Map(nodes.map((node) => [node.id, syntaxPositions.get(node.id)?.row || 0]));
  const outgoing = new Map();
  const indegree = new Map(nodes.map((node) => [node.id, 0]));
  for (const edge of edges) {
    if (edge.backEdge || edge.from === edge.to || !nodeByID.has(edge.from) || !nodeByID.has(edge.to)) continue;
    const successors = outgoing.get(edge.from) || [];
    successors.push(edge.to);
    outgoing.set(edge.from, successors);
    indegree.set(edge.to, (indegree.get(edge.to) || 0) + 1);
  }
  const queue = nodes.filter((node) => indegree.get(node.id) === 0).map((node) => node.id);
  for (let next = 0; next < queue.length; next++) {
    const from = queue[next];
    for (const to of outgoing.get(from) || []) {
      levels.set(to, Math.max(levels.get(to) || 0, (levels.get(from) || 0) + 1));
      indegree.set(to, indegree.get(to) - 1);
      if (indegree.get(to) === 0) queue.push(to);
    }
  }

  const routeIntervals = { left: [], right: [] };
  const routeAssignments = new Map();
  for (const edge of edges) {
    const fromLevel = levels.get(edge.from);
    const toLevel = levels.get(edge.to);
    if (fromLevel === undefined || toLevel === undefined) continue;
    const fromPosition = syntaxPositions.get(edge.from);
    const straightX = (fromPosition?.x || 0) + nodeWidth / 2;
    const bypassesNode = (edge.bypassedNodeIDs || []).some((nodeID) => {
      const bypassed = syntaxPositions.get(`ast:${nodeID}`);
      return bypassed && straightX >= bypassed.x && straightX <= bypassed.x + nodeWidth;
    });
    const routed = edge.backEdge || toLevel <= fromLevel || edge.fallthrough && bypassesNode;
    if (!routed) continue;
    const side = edge.backEdge || toLevel <= fromLevel ? "left" : "right";
    const start = Math.min(fromLevel, toLevel);
    const end = Math.max(fromLevel, toLevel);
    let lane = routeIntervals[side].findIndex((occupiedUntil) => occupiedUntil < start);
    if (lane < 0) lane = routeIntervals[side].length;
    routeIntervals[side][lane] = end;
    routeAssignments.set(edge.id, { side, lane, backEdge: Boolean(edge.backEdge || toLevel <= fromLevel) });
  }

  const routingLanes = Math.max(routeIntervals.left.length, routeIntervals.right.length);
  const routingMargin = routingLanes * 18;
  const width = Math.max(minimumWidth, graphWidth + 80 + routingMargin * 2);
  const graphLeft = (width - graphWidth) / 2;
  const positions = new Map();
  let maximumLevel = graphRows - 1;
  for (const node of nodes) {
    const position = syntaxPositions.get(node.id) || { x: 0, row: 0 };
    const level = levels.get(node.id) ?? position.row;
    maximumLevel = Math.max(maximumLevel, level);
    positions.set(node.id, {
      x: graphLeft + position.x,
      y: verticalPadding + level * verticalPitch,
      lane: position.x,
      level,
    });
  }
  const edgeRoutes = new Map();
  for (const [edgeID, route] of routeAssignments) {
    edgeRoutes.set(edgeID, {
      railX: route.side === "left"
        ? graphLeft - 18 - route.lane * 18
        : graphLeft + graphWidth + 18 + route.lane * 18,
      backEdge: route.backEdge,
    });
  }

  return {
    positions,
    edgeRoutes,
    nodeWidth,
    nodeHeight,
    width,
    height: Math.max(minimumHeight, verticalPadding * 2 + nodeHeight + Math.max(0, maximumLevel) * verticalPitch),
  };
}

function optionalSideBranch(node, groups) {
  return node.definition.kind === "condition" && node.definition.flowCanSkip && groups.length === 1;
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
