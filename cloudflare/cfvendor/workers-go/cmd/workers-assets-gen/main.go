package main

import (
	"bytes"
	"embed"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
)

//go:embed assets
var assets embed.FS

const (
	assetDirPath        = "assets"
	commonDirPath       = "assets/common"
	runtimeDirPath      = "assets/runtime"
	defaultBuildDirPath = "build"
)

func main() {
	var (
		mode         string
		runtime      string
		buildDirPath string
	)
	flag.StringVar(&mode, "mode", string(ModeTinygo), `build mode: tinygo or go`)
	flag.StringVar(&runtime, "runtime", string(RuntimeCloudflare), `runtime: cloudflare, browser, or neon`)
	flag.StringVar(&buildDirPath, "o", defaultBuildDirPath, `output dir path: defaults to "build"`)
	flag.Parse()
	if !Mode(mode).IsValid() {
		flag.PrintDefaults()
		os.Exit(1)
		return
	}
	if !Runtime(runtime).IsValid() {
		flag.PrintDefaults()
		os.Exit(1)
		return
	}
	if err := runMain(Mode(mode), Runtime(runtime), buildDirPath); err != nil {
		fmt.Fprintf(os.Stderr, "err: %v", err)
		os.Exit(1)
	}
}

func runMain(mode Mode, runtime Runtime, buildDirPath string) error {
	if err := os.RemoveAll(buildDirPath); err != nil {
		return err
	}
	if err := os.MkdirAll(buildDirPath, os.ModePerm); err != nil {
		return err
	}
	if err := copyWasmExecJS(mode, buildDirPath); err != nil {
		return err
	}
	if err := copyRuntimeAssets(runtime, buildDirPath); err != nil {
		return err
	}
	if err := copyCommonAssets(runtime, buildDirPath); err != nil {
		return err
	}
	return nil
}

func copyWasmExecJS(mode Mode, buildDirPath string) error {
	var fileName string
	switch mode {
	case ModeTinygo:
		fileName = "wasm_exec_tinygo.js"
	case ModeGo:
		fileName = "wasm_exec_go.js"
	default:
		return fmt.Errorf("unexpected mode: %s", mode)
	}
	destPath := path.Join(buildDirPath, "wasm_exec.js")
	originPath := path.Join(assetDirPath, fileName)
	if err := copyFile(destPath, originPath); err != nil {
		return err
	}
	return nil
}

func copyRuntimeAssets(runtime Runtime, buildDirPath string) error {
	destPath := path.Join(buildDirPath, "runtime.mjs")
	originPath := path.Join(runtimeDirPath, runtime.AssetFileName())
	if err := copyFile(destPath, originPath); err != nil {
		return err
	}
	return nil
}

func copyCommonAssets(runtime Runtime, buildDirPath string) error {
	entries, err := assets.ReadDir(commonDirPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		fileName := entry.Name()
		// Neon Functions only loads an entry file named index.mjs or index.js,
		// so the worker entry point is renamed for that runtime.
		// https://neon.com/docs/compute/functions/deploy
		if runtime == RuntimeNeon && fileName == "worker.mjs" {
			fileName = "index.mjs"
		}
		destPath := path.Join(buildDirPath, fileName)
		originPath := path.Join(commonDirPath, entry.Name())
		if err := copyFile(destPath, originPath); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(destPath, originPath string) error {
	f, err := assets.ReadFile(originPath)
	if err != nil {
		return err
	}
	dest, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer dest.Close()
	_, err = io.Copy(dest, bytes.NewReader(f))
	if err != nil {
		return err
	}
	return nil
}
