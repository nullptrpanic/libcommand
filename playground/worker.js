/* global Go, importScripts */

let runtimePromise;
let handlerRuntime;
let activeCommandHandlers = new Map();

self.libcommandExecuteCommand = (registrationName, encodedInvocation) => (
  handlerRuntime.executeCommandHandler(activeCommandHandlers, registrationName, encodedInvocation)
);

function initializeRuntime() {
  if (runtimePromise) return runtimePromise;
  runtimePromise = (async () => {
    handlerRuntime = await import("./handler.js");
    importScripts("wasm_exec.js");
    const go = new Go();
    const response = await fetch("libcommand.wasm");
    if (!response.ok) throw new Error(`load WebAssembly: HTTP ${response.status}`);
    const bytes = await response.arrayBuffer();
    const instance = await WebAssembly.instantiate(bytes, go.importObject);
    void go.run(instance.instance);
    for (let attempt = 0; attempt < 100 && (typeof self.libcommandAnalyze !== "function" || typeof self.libcommandParse !== "function"); attempt++) {
      await new Promise((resolve) => setTimeout(resolve, 0));
    }
    if (typeof self.libcommandAnalyze !== "function" || typeof self.libcommandParse !== "function") {
      throw new Error("WebAssembly runtime did not initialize");
    }
  })();
  return runtimePromise;
}

self.onmessage = async ({ data }) => {
  const { id, request, type = "simulate" } = data || {};
  try {
    await initializeRuntime();
    if (type === "parse") {
      const result = JSON.parse(self.libcommandParse(JSON.stringify({ source: request?.source || "" })));
      self.postMessage({ id, type: "ast", result });
      return;
    }
    activeCommandHandlers = handlerRuntime.compileCommandHandlers(request?.commands || []);
    try {
      const streamTrace = (encodedEvent) => {
        self.postMessage({ id, type: "trace", event: JSON.parse(encodedEvent) });
      };
      const result = JSON.parse(self.libcommandAnalyze(JSON.stringify(request), streamTrace));
      self.postMessage({ id, type: "result", result });
    } finally {
      activeCommandHandlers = new Map();
    }
  } catch (error) {
    self.postMessage({ id, type: type === "parse" ? "ast" : "result", error: error instanceof Error ? error.message : String(error) });
  }
};

void initializeRuntime()
  .then(() => self.postMessage({ type: "ready" }))
  .catch((error) => self.postMessage({ type: "error", error: error instanceof Error ? error.message : String(error) }));
