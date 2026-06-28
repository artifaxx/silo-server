# Silo Yamtrack Scrobble Provider

**Status:** Design approved, ready for implementation plan
**Date:** 2026-06-27
**Area:** `internal/watchsync/providers/yamtrack`, `cmd/silo/main.go`, watch provider settings UI
**Repository:** [artifaxx/silo-server](https://github.com/artifaxx/silo-server) (fork; upstream PR candidate)

## Summary

Add a built-in Silo watch provider that scrobbles playback to [Yamtrack](https://github.com/FuzzyGrim/Yamtrack) by POSTing **Jellyfin-compatible webhook JSON** to Yamtrack's existing `/integrations/webhook/jellyfin/{token}` endpoint.

Yamtrack has no Trakt-style scrobble API today. It already accepts Jellyfin `Play` and `Stop` events and uses them to mark media **in-progress** and **completed**. Silo's `watchsync` subsystem fires `ScrobbleStart` / `ScrobbleStop` events during playback; this provider translates those into Yamtrack's expected payload shape.

No Yamtrack changes are required. Silo's plugin SDK does not expose a watch-provider capability, so this is implemented as a compiled-in provider (same pattern as Trakt, Simkl, MDBList), not a plugin.

## Goals

- Jellyfin-parity scrobbling from Silo → Yamtrack for movies and TV episodes.
- **Play** (`ScrobbleStart`) marks media in-progress in Yamtrack (movies get `IN_PROGRESS`; TV show/season promoted to in-progress).
- **Stop** (`ScrobbleStop`) marks completed playback as watched when `Completed=true`.
- Per-profile configuration: Yamtrack base URL + webhook token (from Yamtrack Integrations settings).
- Deploy from the `artifaxx/silo-server` fork until upstream merge.

## Non-Goals

- Bidirectional history/watchlist sync (Trakt/Simkl-style import/export).
- Manual mark played/unplayed sync (`MarkPlayed` / `MarkUnplayed` Jellyfin events).
- In-progress percentage tracking beyond Yamtrack's existing Jellyfin behavior (no partial-progress API).
- Plugin-based extensibility for watch providers.
- A native Yamtrack webhook endpoint (Jellyfin emulation is sufficient).
- Trakt/Simkl bridge workarounds.

## Background

### Silo watchsync

Watch providers are registered at startup in `cmd/silo/main.go` and implement interfaces from `internal/watchsync/types.go`. Scrobbling is dispatched from `internal/watchsync/service.go` via `ScrobbleStart` / `ScrobblePause` / `ScrobbleStop` to every connected provider with `ScrobbleEnabled` for the user's profile.

The plugin system (`internal/pluginhost/`, `silo-plugin-sdk`) covers metadata, markers, auth, and scheduled tasks — **not** watch/scrobble providers. Adding plugin support would be a larger architectural change than a single provider PR.

### Yamtrack Jellyfin webhook

Yamtrack's `JellyfinWebhookProcessor` (`src/integrations/webhooks/jellyfin.py`) accepts:

| Event | Yamtrack behavior |
|-------|-------------------|
| `Play` | Movies → `IN_PROGRESS` with `start_date`. TV → show + season `IN_PROGRESS`. |
| `Stop` + `UserData.Played: true` | Movies → `COMPLETED`. TV → episode marked watched. |
| `Stop` + `UserData.Played: false` | Movies stay in-progress. TV episode not marked. |

`Pause` is not supported and should be a no-op.

Yamtrack's integrations UI documents the official Jellyfin webhook template mapping `PlaybackStart` → `Play` and `PlaybackStop` → `Stop`, with `UserData.Played` derived from `PlayedToCompletion`.

### Provider ID resolution in Yamtrack

Yamtrack extracts all three Jellyfin `ProviderIds` keys (`Tmdb`, `Imdb`, `Tvdb`) but uses a **priority chain** per media type. At least one non-empty ID is required or the webhook is ignored.

| Media | Primary ID | Fallback | Notes |
|-------|------------|----------|-------|
| **Movie** | `Tmdb` | `Imdb` (TMDB find) | `Tvdb` is extracted but **ignored** for movies. |
| **Episode** | `Tvdb` (episode ID) | `Imdb` (TMDB find) | `Tmdb` in `ProviderIds` is **not used directly** on the TV path. |

Silo should send **all available IDs** (matches Jellyfin's official template). Redundant IDs are harmless.

**Critical:** for TV episodes, `Tvdb` must be the **episode-level** TVDB ID, not the series ID. Silo's `ScrobbleEvent.TVDBID` must be episode-level.

## Approaches Considered

| Approach | Verdict |
|----------|---------|
| **Built-in Yamtrack provider via Jellyfin webhook emulation** | **Recommended.** Clean, real-time, no Yamtrack changes, fits existing watchsync model. |
| Trakt/Simkl bridge + Yamtrack periodic import | Rejected. Not real-time; extra dependency; poor match for Play/Stop semantics. |
| Sidecar bridge service | Rejected. Silo exposes no clean outbound event API; another service to maintain. |
| Plugin | Rejected. No watch-provider capability in plugin SDK today. |
| Yamtrack native Silo webhook | Rejected. Still requires Silo to emit events; Jellyfin emulation is well-defined and tested in Yamtrack. |

## Design

### Architecture

```
Silo player starts playback
  → watchsync.Service.ScrobbleStart(event)
  → YamtrackProvider.Start()
  → POST {base}/integrations/webhook/jellyfin/{token}
     Event: "Play", UserData.Played: false
  → Yamtrack marks IN_PROGRESS

Silo player stops (completed)
  → watchsync.Service.ScrobbleStop(event, Completed=true)
  → YamtrackProvider.Stop()
  → POST same URL
     Event: "Stop", UserData.Played: true
  → Yamtrack marks COMPLETED / episode watched

Silo player stops (not completed)
  → ScrobbleStop(Completed=false)
  → POST Event: "Stop", UserData.Played: false
  → Yamtrack leaves in-progress state unchanged
```

`ScrobblePause` is a no-op.

### Package layout

```
internal/watchsync/providers/yamtrack/
  provider.go       # Provider, Scrobbler, APIKeyAuthProvider
  payload.go        # buildJellyfinWebhook(event, eventName, played bool)
  client.go         # HTTP POST helper
  provider_test.go  # payload + client tests
```

Register in `cmd/silo/main.go` alongside `trakt`, `simkl`, `mdblist`:

```go
watchProviderRegistry.Register(yamtrack.NewProvider(nil))
```

At scrobble time, resolve the webhook URL as:

```
{conn.ProviderAccountID}/integrations/webhook/jellyfin/{conn.AccessToken}
```

### Provider capabilities

```go
func (p *Provider) Capabilities() watchsync.Capabilities {
    return watchsync.Capabilities{
        ScrobblePlayback: true,
    }
}
```

Scrobble-only — no import/export, watchlist, or favorites sync.

### Authentication & connection

Implement `APIKeyAuthProvider` (same pattern as MDBList), with one additive API change for the base URL.

Yamtrack needs **two** values; the existing `ConnectAPIKey` handler only accepts a single `api_key` string today. Extend the connect request body (backward compatible):

```json
{ "api_key": "<webhook-token>", "base_url": "https://yamtrack.example.com" }
```

Storage on `watchsync.Connection`:

| Field | Value |
|-------|-------|
| `AccessToken` | Yamtrack webhook token (from Integrations → Jellyfin URL) |
| `ProviderAccountID` | Normalized base URL (no trailing slash) |

`ConnectWithAPIKey` validates both fields are non-empty. Yamtrack has no account lookup API — `ProviderAccount.Username` can be `"yamtrack"` and `ProviderAccount.ID` holds the base URL.

`RefreshToken` is a no-op (token does not expire unless regenerated in Yamtrack).

Connection setup in the Watch Providers UI:

1. Yamtrack base URL (new field, shown only for this provider)
2. Webhook token

Frontend: extend `APIKeyBlock` in `WatchProvidersSettings.tsx` (or a Yamtrack-specific variant) to collect both fields and POST them to the extended connect endpoint.

### Payload builder

Map `watchsync.ScrobbleEvent` → Jellyfin webhook JSON:

```json
{
  "Event": "Play",
  "Item": {
    "Type": "Episode",
    "Name": "<title>",
    "ProductionYear": 1999,
    "ProviderIds": {
      "Tmdb": "<tmdb or series tmdb>",
      "Imdb": "<imdb>",
      "Tvdb": "<episode tvdb>"
    },
    "UserData": {
      "Played": false
    },
    "SeriesName": "<series title>",
    "ParentIndexNumber": 1,
    "IndexNumber": 3
  }
}
```

Mapping rules:

| Silo field | Jellyfin field | Notes |
|------------|----------------|-------|
| `Kind == movie` | `Item.Type: "Movie"` | |
| `Kind == episode` | `Item.Type: "Episode"` | |
| `TMDBID` | `ProviderIds.Tmdb` | Movie: direct. Episode: episode TMDB if present, else omit. |
| `IMDbID` | `ProviderIds.Imdb` | |
| `TVDBID` | `ProviderIds.Tvdb` | Episode-level only for TV. |
| `SeriesTitle` | `Item.SeriesName` | Episodes only. |
| `SeasonNumber` | `Item.ParentIndexNumber` | Episodes only. |
| `EpisodeNumber` | `Item.IndexNumber` | Episodes only. |
| `Title` | `Item.Name` | Optional but useful for Yamtrack logging. |
| `Year` (if available) | `Item.ProductionYear` | Movies only when known. |
| `Completed` on Stop | `UserData.Played` | `true` when `event.Completed`, else `false`. |

Omit empty provider ID fields rather than sending empty strings (Yamtrack treats `""` as absent in fallback tests).

Skip scrobble (log debug, return nil) when **no** provider ID would be sent — Yamtrack would ignore the webhook anyway.

### HTTP client

```
POST {baseURL}/integrations/webhook/jellyfin/{token}
Content-Type: application/json
```

- Timeout: 10s (configurable via injected `http.Client`).
- Retry: up to 2 retries with short backoff on 5xx / network errors.
- 401 → surface as connection error ("invalid webhook token").
- 2xx → success.
- Do not block playback on Yamtrack failures; log and record `LastScrobbleErrorAt` via existing watchsync error plumbing.

### UI

Add **Yamtrack** to Watch Providers settings:

- Display name: Yamtrack
- Auth method: API key (base URL + token fields)
- Capability toggles: scrobble only (`ScrobbleEnabled`)
- Connection status / last error (reuse existing watch provider status components)

Follow MDBList's API-key connect flow in the frontend watch provider settings.

### Error handling

| Condition | Behavior |
|-----------|----------|
| Missing base URL or token | Reject at connect time |
| No provider IDs on event | Skip POST, debug log |
| Yamtrack unreachable | Log warning, retry, record last error |
| 401 Unauthorized | Mark connection unhealthy, prompt token refresh |
| Playback continues | Never fail the Silo player session |

### Testing

**Unit tests (`provider_test.go`, `payload_test.go`):**

- Movie Play → `Event: Play`, `Played: false`, `Type: Movie`, `ProviderIds.Tmdb` set.
- Movie Stop completed → `Event: Stop`, `Played: true`.
- Movie Stop incomplete → `Event: Stop`, `Played: false`.
- Episode Play → `Type: Episode`, episode `Tvdb`, season/episode numbers, `SeriesName`.
- Episode Stop completed → same IDs, `Played: true`.
- Empty IDs → builder returns skip / error.
- Pause → no HTTP call.

**Integration test:**

- `httptest.Server` receives expected POST body on `Start` and `Stop`.
- Assert path includes token segment.

**Manual:**

1. Configure Yamtrack Jellyfin webhook token in Silo Watch Providers.
2. Play a movie in Silo → verify Yamtrack shows in-progress.
3. Finish movie → verify completed.
4. Play TV episode → verify show in-progress; finish → episode watched.

### Deployment

Until upstream accepts a PR to [Silo-Server/silo-server](https://github.com/Silo-Server/silo-server):

1. Implement on `artifaxx/silo-server`.
2. Build and deploy a container image from the fork (GHCR or local registry).
3. Optionally open upstream PR for merge into official image (`ghcr.io/silo-server/silo-server`).

Yamtrack requires no changes — reuse existing Jellyfin integration URL and token.

## Risks & Mitigations

| Risk | Mitigation |
|------|------------|
| Wrong TVDB ID granularity (series vs episode) | Document requirement; map from Silo episode metadata only; add test fixtures. |
| Yamtrack rejects payload with no IDs | Skip POST when ID set would be empty; log clearly. |
| Jellyfin emulation breaks on Yamtrack upgrade | Yamtrack has extensive Jellyfin webhook tests; monitor upstream changes. |
| Fork drift from upstream Silo | Keep provider isolated in one package; open upstream PR early. |

## References

- Yamtrack Jellyfin webhook: `src/integrations/webhooks/jellyfin.py`
- Yamtrack movie handler: `src/integrations/webhooks/movie.py`
- Yamtrack TV handler: `src/integrations/webhooks/tv.py`
- Yamtrack Jellyfin tests: `src/integrations/tests/test_webhooks_jellyfin.py`
- Yamtrack integrations template: `src/templates/users/integrations.html`
- Silo MDBList provider (API key + scrobble pattern): `internal/watchsync/providers/mdblist/`
- Silo watchsync types: `internal/watchsync/types.go`
- Silo provider registration: `cmd/silo/main.go`
