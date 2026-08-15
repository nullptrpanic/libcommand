import test from "node:test";
import assert from "node:assert/strict";

const handlerRuntime = await import("./handler.js").catch(() => ({}));

test("JavaScript command handler receives the expanded lark-cli invocation", () => {
  assert.equal(typeof handlerRuntime.compileCommandHandlers, "function");
  assert.equal(typeof handlerRuntime.executeCommandHandler, "function");

  const handlers = handlerRuntime.compileCommandHandlers([{
    name: "lark-cli",
    javascript: `const args = invocation.args.map((argument) => argument.value);
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
};`,
  }]);
  const result = JSON.parse(handlerRuntime.executeCommandHandler(handlers, "lark-cli", invocationJSON([
    "im", "+messages-send", "--chat-id", "oc_demo", "--text", "评测任务已启动", "--as", "bot",
  ])));

  assert.deepEqual(JSON.parse(result.stdout), {
    command: "lark-cli",
    chatId: "oc_demo",
    text: "评测任务已启动",
    as: "bot",
  });
  assert.equal(result.stderr, "");
  assert.equal(result.exitCode, 0);
});

test("JavaScript command handler exposes decoded stdin and defaults its result", () => {
  const handlers = handlerRuntime.compileCommandHandlers([{
    name: "read-input",
    javascript: "return { stdout: invocation.stdin };",
  }]);

  assert.deepEqual(JSON.parse(handlerRuntime.executeCommandHandler(
    handlers,
    "read-input",
    invocationJSON([], "line one\n"),
  )), {
    stdout: "line one\n",
    stderr: "",
    exitCode: 0,
  });
});

test("JavaScript wildcard handler observes the actual command name", () => {
  const handlers = handlerRuntime.compileCommandHandlers([{
    name: "*",
    javascript: "return { stdout: invocation.name };",
  }]);

  const result = JSON.parse(handlerRuntime.executeCommandHandler(
    handlers,
    "*",
    invocationJSON([], "", "external-command"),
  ));
  assert.equal(result.stdout, "external-command");
});

test("JavaScript command handler reports compilation and execution errors", () => {
  assert.throws(() => handlerRuntime.compileCommandHandlers([{
    name: "bad-syntax",
    javascript: "return {",
  }]), /bad-syntax/);

  for (const [name, source, message] of [
    ["throws", "throw new Error('handler failed');", /handler failed/],
    ["async", "return Promise.resolve({ exitCode: 0 });", /synchronous/],
    ["bad-result", "return null;", /result object/],
    ["bad-exit", "return { exitCode: 300 };", /0 through 255/],
  ]) {
    const handlers = handlerRuntime.compileCommandHandlers([{ name, javascript: source }]);
    const result = JSON.parse(handlerRuntime.executeCommandHandler(handlers, name, invocationJSON([])));
    assert.match(result.error, message);
  }
});

function invocationJSON(args, stdin = "", name = "lark-cli") {
  return JSON.stringify({
    name,
    args: args.map((value) => ({ kind: 0, value })),
    env: { HOME: "/" },
    dir: "/",
    stdin: Buffer.from(stdin).toString("base64"),
  });
}
