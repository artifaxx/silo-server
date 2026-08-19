// Package yamtrack implements a scrobble-only Yamtrack watch provider.
// Users paste their Yamtrack Jellyfin webhook URL; Silo POSTs Play/Stop
// payloads that match Yamtrack's shipped webhook contract.
package yamtrack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

const (
	providerKey         = "yamtrack"
	jellyfinWebhookPath = "/webhook/jellyfin"
	maxErrorBodyBytes   = 4 << 10
)

type Provider struct {
	client *http.Client
}

func NewProvider(client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{
			Timeout: 20 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Provider{client: client}
}

func (p *Provider) Key() string { return providerKey }

func (p *Provider) DisplayName() string { return "Yamtrack" }

func (p *Provider) Capabilities() watchsync.Capabilities {
	return watchsync.Capabilities{ScrobblePlayback: true}
}

func (p *Provider) HistorySource() userstore.WatchHistorySource {
	return userstore.WatchHistorySourceYamtrack
}

func (p *Provider) AuthMethod() string { return watchsync.AuthMethodAPIKey }

func (p *Provider) ScrobbleOrderingKey(conn watchsync.Connection, _ watchsync.ScrobbleEvent) string {
	return "yamtrack:" + conn.ID
}

func (p *Provider) ConnectWithAPIKey(ctx context.Context, apiKey string) (watchsync.TokenSet, watchsync.ProviderAccount, error) {
	webhookURL, account, err := parseWebhookURL(apiKey)
	if err != nil {
		return watchsync.TokenSet{}, watchsync.ProviderAccount{}, err
	}
	if err := p.probe(ctx, webhookURL); err != nil {
		return watchsync.TokenSet{}, watchsync.ProviderAccount{}, err
	}
	return watchsync.TokenSet{AccessToken: webhookURL}, account, nil
}

func (p *Provider) LookupAccount(_ context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) (watchsync.ProviderAccount, error) {
	_, account, err := parseWebhookURL(conn.AccessToken)
	return account, err
}

func (p *Provider) RefreshToken(_ context.Context, _ watchsync.ServerConfig, conn watchsync.Connection) (watchsync.TokenSet, error) {
	return watchsync.TokenSet{AccessToken: conn.AccessToken}, nil
}

func (p *Provider) Start(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, conn, event, "Play", false)
}

func (p *Provider) Pause(context.Context, watchsync.ServerConfig, watchsync.Connection, watchsync.ScrobbleEvent) error {
	// Yamtrack has no pause/resume scrobble; live progress is start/stop only.
	return nil
}

func (p *Provider) Stop(ctx context.Context, _ watchsync.ServerConfig, conn watchsync.Connection, event watchsync.ScrobbleEvent) error {
	return p.scrobble(ctx, conn, event, "Stop", event.Completed)
}

func (p *Provider) scrobble(ctx context.Context, conn watchsync.Connection, event watchsync.ScrobbleEvent, jellyfinEvent string, played bool) error {
	payload, err := buildJellyfinPayload(event, jellyfinEvent, played)
	if err != nil {
		return err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode yamtrack webhook payload: %w", err)
	}
	return p.post(ctx, conn.AccessToken, body, false)
}

func (p *Provider) probe(ctx context.Context, webhookURL string) error {
	return p.post(ctx, webhookURL, nil, true)
}

func (p *Provider) post(ctx context.Context, webhookURL string, body []byte, probe bool) error {
	if strings.TrimSpace(webhookURL) == "" {
		return errors.New("yamtrack webhook URL is missing")
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, reader)
	if err != nil {
		return fmt.Errorf("create yamtrack webhook request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("send yamtrack webhook request: %w", err)
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxErrorBodyBytes)
	_, _ = io.Copy(io.Discard, limited)

	if probe {
		switch resp.StatusCode {
		case http.StatusBadRequest:
			// Empty body is Yamtrack's "Missing payload" probe success.
			return nil
		case http.StatusUnauthorized:
			return errors.New("yamtrack webhook token was rejected")
		default:
			return fmt.Errorf("yamtrack webhook URL did not look like a Jellyfin webhook: status %d", resp.StatusCode)
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("yamtrack webhook token was rejected")
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("yamtrack webhook request failed: status %d", resp.StatusCode)
	}
	return nil
}

func parseWebhookURL(raw string) (string, watchsync.ProviderAccount, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", watchsync.ProviderAccount{}, errors.New("yamtrack jellyfin webhook URL is required")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", watchsync.ProviderAccount{}, errors.New("yamtrack webhook URL is invalid")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", watchsync.ProviderAccount{}, errors.New("yamtrack webhook URL must use http or https")
	}
	if parsed.User != nil {
		return "", watchsync.ProviderAccount{}, errors.New("yamtrack webhook URL must not embed credentials")
	}
	if parsed.Host == "" {
		return "", watchsync.ProviderAccount{}, errors.New("yamtrack webhook URL has no host")
	}
	path := strings.TrimSuffix(parsed.Path, "/")
	idx := strings.LastIndex(strings.ToLower(path), jellyfinWebhookPath)
	if idx < 0 {
		return "", watchsync.ProviderAccount{}, errors.New("paste the full Yamtrack Jellyfin webhook URL")
	}
	token := strings.TrimPrefix(path[idx+len(jellyfinWebhookPath):], "/")
	if token == "" || strings.Contains(token, "/") {
		return "", watchsync.ProviderAccount{}, errors.New("yamtrack webhook URL is missing its token")
	}
	parsed.Fragment = ""
	parsed.RawQuery = ""
	parsed.Path = path
	return parsed.String(), watchsync.ProviderAccount{
		ID:       token,
		Username: parsed.Hostname(),
	}, nil
}

type jellyfinWebhookPayload struct {
	Event string              `json:"Event"`
	Item  jellyfinWebhookItem `json:"Item"`
}

type jellyfinWebhookItem struct {
	Type              string            `json:"Type"`
	Name              string            `json:"Name,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber,omitempty"`
	IndexNumber       int               `json:"IndexNumber,omitempty"`
	ProviderIds       map[string]string `json:"ProviderIds"`
	UserData          jellyfinUserData  `json:"UserData"`
}

type jellyfinUserData struct {
	Played bool `json:"Played"`
}

func buildJellyfinPayload(event watchsync.ScrobbleEvent, jellyfinEvent string, played bool) (jellyfinWebhookPayload, error) {
	payload := jellyfinWebhookPayload{
		Event: jellyfinEvent,
		Item: jellyfinWebhookItem{
			ProviderIds: map[string]string{},
			UserData:    jellyfinUserData{Played: played},
		},
	}
	switch event.Kind {
	case historyimport.KindEpisode:
		if event.TVDBID == "" && event.IMDbID == "" {
			return jellyfinWebhookPayload{}, errors.New("yamtrack episode scrobble needs a TVDB or IMDb id")
		}
		payload.Item.Type = "Episode"
		payload.Item.ParentIndexNumber = event.SeasonNumber
		payload.Item.IndexNumber = event.EpisodeNumber
		setProviderID(payload.Item.ProviderIds, "Tvdb", event.TVDBID)
		setProviderID(payload.Item.ProviderIds, "Imdb", event.IMDbID)
		setProviderID(payload.Item.ProviderIds, "Tmdb", event.TMDBID)
	default:
		if event.TMDBID == "" && event.IMDbID == "" {
			return jellyfinWebhookPayload{}, errors.New("yamtrack movie scrobble needs a TMDB or IMDb id")
		}
		payload.Item.Type = "Movie"
		setProviderID(payload.Item.ProviderIds, "Tmdb", event.TMDBID)
		setProviderID(payload.Item.ProviderIds, "Imdb", event.IMDbID)
		setProviderID(payload.Item.ProviderIds, "Tvdb", event.TVDBID)
	}
	return payload, nil
}

func setProviderID(ids map[string]string, key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	ids[key] = value
}
