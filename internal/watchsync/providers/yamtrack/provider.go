// Package yamtrack implements a Silo watch provider that scrobbles playback to
// Yamtrack via Jellyfin-compatible webhook JSON.
package yamtrack

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/Silo-Server/silo-server/internal/watchsync"
)

type Provider struct {
	client *webhookClient
}

func NewProvider(client *http.Client) *Provider {
	return &Provider{
		client: newWebhookClient(client),
	}
}

func (p *Provider) Key() string {
	return "yamtrack"
}

func (p *Provider) DisplayName() string {
	return "Yamtrack"
}

func (p *Provider) Capabilities() watchsync.Capabilities {
	return watchsync.Capabilities{
		ScrobblePlayback: true,
	}
}

func (p *Provider) ConnectWithAPIKeyAndBaseURL(
	ctx context.Context,
	apiKey string,
	baseURL string,
) (watchsync.TokenSet, watchsync.ProviderAccount, error) {
	_ = ctx
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" {
		return watchsync.TokenSet{}, watchsync.ProviderAccount{}, errors.New("yamtrack webhook token is required")
	}
	normalizedBaseURL, err := normalizeBaseURL(baseURL)
	if err != nil {
		return watchsync.TokenSet{}, watchsync.ProviderAccount{}, err
	}
	return watchsync.TokenSet{AccessToken: apiKey}, watchsync.ProviderAccount{
		ID:       normalizedBaseURL,
		Username: "yamtrack",
	}, nil
}

func (p *Provider) LookupAccount(_ context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) (watchsync.ProviderAccount, error) {
	return watchsync.ProviderAccount{
		ID:       conn.ProviderAccountID,
		Username: "yamtrack",
	}, nil
}

// RefreshToken is a no-op: Yamtrack webhook tokens do not expire unless regenerated.
func (p *Provider) RefreshToken(_ context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) (watchsync.TokenSet, error) {
	return watchsync.TokenSet{AccessToken: conn.AccessToken}, nil
}

func (p *Provider) Start(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, conn, event, "Play", false)
}

func (p *Provider) Pause(_ context.Context, _ watchsync.ServerConfig, _ watchsync.Connection, _ watchsync.ScrobbleEvent) error {
	return nil
}

func (p *Provider) Stop(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, conn, event, "Stop", event.Completed)
}

func (p *Provider) ScrobbleOrderingKey(conn watchsync.Connection, _ watchsync.ScrobbleEvent) string {
	return "yamtrack:" + conn.ID
}

func (p *Provider) scrobble(
	ctx context.Context,
	conn watchsync.Connection,
	event watchsync.ScrobbleEvent,
	eventName string,
	played bool,
) error {
	payload, err := buildJellyfinWebhook(event, eventName, played)
	if errors.Is(err, errSkipScrobble) {
		slog.Debug("yamtrack scrobble skipped: no provider ids",
			"provider", p.Key(),
			"media_item_id", event.MediaItemID,
			"kind", event.Kind,
		)
		return nil
	}
	if err != nil {
		return err
	}
	return p.client.postWebhook(ctx, conn.ProviderAccountID, conn.AccessToken, payload)
}

func normalizeBaseURL(baseURL string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return "", errors.New("yamtrack base url is required")
	}
	baseURL = strings.TrimRight(baseURL, "/")
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("yamtrack base url is invalid: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("yamtrack base url must use http or https")
	}
	if parsed.Host == "" {
		return "", errors.New("yamtrack base url must include a host")
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	return parsed.Scheme + "://" + parsed.Host + path, nil
}
