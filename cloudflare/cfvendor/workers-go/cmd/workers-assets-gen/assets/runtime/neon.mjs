import { readFileSync } from "node:fs";

const modPromise = WebAssembly.compile(
  readFileSync(new URL("./app.wasm", import.meta.url)),
);

export async function loadModule() {
  return await modPromise;
}

export function createRuntimeContext({ env, ctx, binding }) {
  return {
    env,
    ctx,
    binding,
  };
}
