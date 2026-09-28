package main

type Runtime string

const (
	RuntimeCloudflare Runtime = "cloudflare"
	RuntimeBrowser    Runtime = "browser"
	RuntimeNeon       Runtime = "neon"
)

func (r Runtime) IsValid() bool {
	switch r {
	case RuntimeCloudflare, RuntimeBrowser, RuntimeNeon:
		return true
	}
	return false
}

func (r Runtime) AssetFileName() string {
	return string(r) + ".mjs"
}
