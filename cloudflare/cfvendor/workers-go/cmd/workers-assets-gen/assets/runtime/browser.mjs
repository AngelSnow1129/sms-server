const modPromise = WebAssembly.compileStreaming(fetch("./build/app.wasm"));

export async function loadModule() {
  return await modPromise;
}

export function createRuntimeContext({ binding }) {
  return {
    binding,
  };
}
