export function argumentPresentation(argument = {}) {
  const unresolved = Number(argument.kind) !== 0;
  return {
    value: unresolved ? "UNRESOLVED" : String(argument.value || ""),
    unresolved,
  };
}

export function visibleResultFields(result = {}) {
  const exitCodeUnresolved = Boolean(result.exitCodeUnresolved);
  const fields = [{
    label: "Exit code",
    value: exitCodeUnresolved ? "" : String(result.exitCode ?? 0),
    wide: false,
    unresolved: exitCodeUnresolved,
  }];
  if (result.error) fields.push({ label: "Error", value: result.error, wide: false, unresolved: false });
  if (result.stdout || result.stdoutUnresolved) {
    fields.push({ label: "Stdout", value: result.stdout || "", wide: true, unresolved: Boolean(result.stdoutUnresolved) });
  }
  if (result.stderr || result.stderrUnresolved) {
    fields.push({ label: "Stderr", value: result.stderr || "", wide: true, unresolved: Boolean(result.stderrUnresolved) });
  }
  return fields;
}

export function commandResultUnresolved(result) {
  return Boolean(result?.stdoutUnresolved && result?.stderrUnresolved && result?.exitCodeUnresolved);
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

export function executionOccurrenceLabel(node = {}) {
  const count = Number(node.executionCount || 0);
  if (count <= 0) return "Not executed";
  if (count === 1) return "Execution 1 of 1";
  return `Latest execution (${count} of ${count})`;
}

export function invocationText(invocation) {
  return [invocation.name, ...(invocation.args || []).map(formatInvocationArgument)].filter(Boolean).join(" ");
}

function formatInvocationArgument(argument) {
  if (!argument || Number(argument.kind) !== 0) return "";
  const value = String(argument.value || "");
  if (/^[A-Za-z0-9_./:@%+=,-]+$/.test(value)) return value;
  return JSON.stringify(value);
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
