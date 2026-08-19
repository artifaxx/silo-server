# Yamtrack watch provider

Scrobble-only Silo plugin for a self-hosted [Yamtrack](https://github.com/FuzzyGrim/Yamtrack) instance.

Install the plugin, then paste the Jellyfin webhook URL from Yamtrack **Account settings → Integrations** under Silo **Settings → Watch Providers**.

Silo reports:

- start → Yamtrack `Play` with `Played: false`
- pause → ignored (Yamtrack has no pause event)
- completed stop → `Stop` with `Played: true`
- incomplete stop → `Stop` with `Played: false`

Movies need TMDB or IMDb. Episodes need TVDB or IMDb. History import, progress sync, favorites, and watchlists are out of scope until Yamtrack has a stable write API for them.

This plugin is a `watch_sync_provider.v1` package. It is developed here because this change started in silo-server; the durable home for it is a first-party plugin repo (`silo-plugin-yamtrack`).

## Build

Commands assume this directory is the cwd.

```sh
go test ./...
go build -o silo-plugin-yamtrack .
./silo-plugin-yamtrack manifest
```

Upload the binary through Silo’s admin plugin install flow.
