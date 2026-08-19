# Silo Yamtrack Plugin

Scrobble-only Silo watch provider for a self-hosted [Yamtrack](https://github.com/FuzzyGrim/Yamtrack) instance.

Install the plugin, then paste the Jellyfin webhook URL from Yamtrack **Account settings → Integrations** under Silo **Settings → Watch Providers**.

Silo reports:

- start → Yamtrack `Play` with `Played: false`
- pause → ignored (Yamtrack has no pause event)
- completed stop → `Stop` with `Played: true`
- incomplete stop → `Stop` with `Played: false`

Movies need TMDB or IMDb. Episodes need TVDB or IMDb. History import, progress sync, favorites, and watchlists are out of scope until Yamtrack has a stable write API for them.

## Dependency Model

This repository consumes `github.com/Silo-Server/silo-plugin-sdk` as a normal Go module dependency. CI and release builds run with `GOWORK=off` and expect the SDK version in `go.mod` to resolve from a published semver tag.

For local multi-repo development, use a temporary `replace` or a local `go.work` that points at a checkout of `silo-plugin-sdk`. Do not commit machine-local filesystem replaces as the supported release path.

## Development

Commands assume the repository root is the cwd.

```sh
go test ./...
go build -trimpath -ldflags="-s -w -X main.version=0.1.0" -o plugin .
./plugin manifest
```

Upload the binary through Silo’s admin plugin install flow.

## License

`silo-plugin-yamtrack` is licensed under `AGPL-3.0-or-later`. See [LICENSE](LICENSE).
