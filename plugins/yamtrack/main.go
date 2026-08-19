package main

import (
	_ "embed"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	sdkruntime "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginsdk/runtime"
)

const pluginVersion = "0.1.0"

//go:embed manifest.json
var manifestJSON []byte

func main() {
	sdkruntime.ServeManifest(manifestJSON, pluginVersion, sdkruntime.CapabilityServers{
		WatchSyncProvider: NewProvider(nil),
	})
}

// Ensure the generated unimplemented server stays embedded if methods are added.
var _ pluginv1.WatchSyncProviderServer = (*Provider)(nil)
