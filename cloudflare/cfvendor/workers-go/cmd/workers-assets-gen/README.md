# workers-assets-gen

* `workers-assets-gen` command generates files needed to run `workers` package.
  - e.g. wasm_exec.js, worker.mjs ...

## Usage

* See `Makefile` in [templates](https://github.com/syumai/workers-go/tree/main/_templates/cloudflare/worker-tinygo).

## Supported options

* `-mode`
  - switch generated file depends on Go / TinyGo.
* `-runtime`
  - select the target runtime (`cloudflare`, `browser`, or `neon`; default: `cloudflare`).
* `-o`
  - change output directory (default: `build`)
