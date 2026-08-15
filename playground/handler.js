export function compileCommandHandlers(commands) {
  const handlers = new Map();
  for (const command of commands || []) {
    if (typeof command?.javascript !== "string") continue;
    try {
      handlers.set(command.name, new Function("invocation", `"use strict";\n${command.javascript}`));
    } catch (error) {
      throw new Error(`compile JavaScript handler ${JSON.stringify(command.name)}: ${errorMessage(error)}`);
    }
  }
  return handlers;
}

export function executeCommandHandler(handlers, registrationName, encodedInvocation) {
  try {
    const invocation = JSON.parse(encodedInvocation);
    invocation.stdin = decodeBase64(invocation.stdin);
    const result = handlers.get(registrationName)(invocation);
    if (result && typeof result.then === "function") {
      throw new Error("JavaScript handlers must be synchronous");
    }
    if (result === null || typeof result !== "object" || Array.isArray(result)) {
      throw new Error("JavaScript handler must return a result object");
    }
    const exitCode = Number(result.exitCode ?? 0);
    if (!Number.isInteger(exitCode) || exitCode < 0 || exitCode > 255) {
      throw new Error("JavaScript handler exitCode must be an integer from 0 through 255");
    }
    return JSON.stringify({
      stdout: String(result.stdout ?? ""),
      stderr: String(result.stderr ?? ""),
      exitCode,
    });
  } catch (error) {
    return JSON.stringify({
      error: `JavaScript handler ${JSON.stringify(registrationName)}: ${errorMessage(error)}`,
    });
  }
}

function decodeBase64(value) {
  if (!value) return "";
  const bytes = Uint8Array.from(atob(value), (character) => character.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

function errorMessage(error) {
  return error instanceof Error ? error.message : String(error);
}
