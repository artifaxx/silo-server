package yamtrack

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/userstore"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

func TestProviderIdentityAndCapabilities(t *testing.T) {
	p := NewProvider(nil)
	if p.Key() != "yamtrack" {
		t.Fatalf("got key %q, want yamtrack", p.Key())
	}
	if p.DisplayName() != "Yamtrack" {
		t.Fatalf("got display name %q, want Yamtrack", p.DisplayName())
	}
	if p.AuthMethod() != watchsync.AuthMethodAPIKey {
		t.Fatalf("got auth method %q, want api_key", p.AuthMethod())
	}
	if p.HistorySource() != userstore.WatchHistorySourceYamtrack {
		t.Fatalf("got history source %q, want yamtrack", p.HistorySource())
	}
	caps := p.Capabilities()
	if !caps.ScrobblePlayback {
		t.Fatalf("yamtrack should scrobble playback: %#v", caps)
	}
	if caps.ImportWatched || caps.ExportWatched || caps.ImportProgress || caps.ExportUnwatched ||
		caps.ImportFavorites || caps.ExportFavorites || caps.ImportWatchlist || caps.ExportWatchlist {
		t.Fatalf("yamtrack should be scrobble-only: %#v", caps)
	}
	var _ watchsync.APIKeyAuthProvider = p
	var _ watchsync.Scrobbler = p
}

func TestParseWebhookURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		wantURL string
		wantID  string
		wantErr string
	}{
		{
			name:    "https jellyfin webhook",
			raw:     "  https://yamtrack.example.com/webhook/jellyfin/tok_abc  ",
			wantURL: "https://yamtrack.example.com/webhook/jellyfin/tok_abc",
			wantID:  "tok_abc",
		},
		{
			name:    "http with port and trailing slash",
			raw:     "http://192.168.1.12:8000/webhook/jellyfin/tok123/",
			wantURL: "http://192.168.1.12:8000/webhook/jellyfin/tok123",
			wantID:  "tok123",
		},
		{
			name:    "subpath install",
			raw:     "https://home.example.com/yamtrack/webhook/jellyfin/token",
			wantURL: "https://home.example.com/yamtrack/webhook/jellyfin/token",
			wantID:  "token",
		},
		{
			name:    "empty",
			raw:     "   ",
			wantErr: "yamtrack jellyfin webhook URL is required",
		},
		{
			name:    "wrong scheme",
			raw:     "ftp://yamtrack.example.com/webhook/jellyfin/tok",
			wantErr: "yamtrack webhook URL must use http or https",
		},
		{
			name:    "embedded credentials",
			raw:     "https://user:pass@yamtrack.example.com/webhook/jellyfin/tok",
			wantErr: "yamtrack webhook URL must not embed credentials",
		},
		{
			name:    "missing webhook path",
			raw:     "https://yamtrack.example.com/api/v1/media",
			wantErr: "paste the full Yamtrack Jellyfin webhook URL",
		},
		{
			name:    "missing token",
			raw:     "https://yamtrack.example.com/webhook/jellyfin/",
			wantErr: "yamtrack webhook URL is missing its token",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotURL, account, err := parseWebhookURL(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if gotURL != tt.wantURL {
				t.Fatalf("url = %q, want %q", gotURL, tt.wantURL)
			}
			if account.ID != tt.wantID {
				t.Fatalf("account id = %q, want %q", account.ID, tt.wantID)
			}
		})
	}
}

func TestConnectWithAPIKeyProbesEmptyPayload(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		if r.ContentLength > 0 && len(gotBody) > 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Missing payload"))
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	webhook := server.URL + "/webhook/jellyfin/test-token"
	tokens, account, err := p.ConnectWithAPIKey(context.Background(), webhook)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if gotPath != "/webhook/jellyfin/test-token" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(gotBody) != 0 {
		t.Fatalf("probe body = %q, want empty", gotBody)
	}
	if tokens.AccessToken != webhook {
		t.Fatalf("stored token = %q, want webhook URL", tokens.AccessToken)
	}
	if account.ID != "test-token" {
		t.Fatalf("account id = %q", account.ID)
	}
}

func TestConnectWithAPIKeyRejectsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	_, _, err := p.ConnectWithAPIKey(context.Background(), server.URL+"/webhook/jellyfin/bad")
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v, want rejected token", err)
	}
}

func TestPauseIsNoOp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("pause should not call Yamtrack")
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	if err := p.Pause(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{
		AccessToken: server.URL + "/webhook/jellyfin/tok",
	}, watchsync.ScrobbleEvent{Kind: historyimport.KindMovie, TMDBID: "603"}); err != nil {
		t.Fatalf("pause: %v", err)
	}
}

func TestStopCompletedMoviePostsJellyfinPayload(t *testing.T) {
	var got jellyfinWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	err := p.Stop(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{
		AccessToken: server.URL + "/webhook/jellyfin/tok",
	}, watchsync.ScrobbleEvent{
		Kind:      historyimport.KindMovie,
		TMDBID:    "603",
		IMDbID:    "tt0133093",
		Completed: true,
	})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got.Event != "Stop" {
		t.Fatalf("event = %q, want Stop", got.Event)
	}
	if got.Item.Type != "Movie" {
		t.Fatalf("type = %q, want Movie", got.Item.Type)
	}
	if !got.Item.UserData.Played {
		t.Fatal("expected Played true for completed stop")
	}
	if got.Item.ProviderIds["Tmdb"] != "603" || got.Item.ProviderIds["Imdb"] != "tt0133093" {
		t.Fatalf("provider ids = %#v", got.Item.ProviderIds)
	}
}

func TestStopCompletedEpisodeUsesEpisodeTVDB(t *testing.T) {
	var got jellyfinWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	err := p.Stop(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{
		AccessToken: server.URL + "/webhook/jellyfin/tok",
	}, watchsync.ScrobbleEvent{
		Kind:          historyimport.KindEpisode,
		TVDBID:        "303821",
		TMDBID:        "62085",
		IMDbID:        "tt0583459",
		SeriesTMDBID:  "1396",
		SeriesTVDBID:  "75930",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		Completed:     true,
	})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got.Item.Type != "Episode" {
		t.Fatalf("type = %q", got.Item.Type)
	}
	if got.Item.ProviderIds["Tvdb"] != "303821" {
		t.Fatalf("tvdb = %q, want episode id not series id", got.Item.ProviderIds["Tvdb"])
	}
	if got.Item.ProviderIds["Tmdb"] != "1396" {
		t.Fatalf("tmdb = %q, want series id", got.Item.ProviderIds["Tmdb"])
	}
	if got.Item.ParentIndexNumber != 1 || got.Item.IndexNumber != 1 {
		t.Fatalf("season/episode = %d/%d", got.Item.ParentIndexNumber, got.Item.IndexNumber)
	}
}

func TestStopIncompleteMovieMarksUnplayed(t *testing.T) {
	var got jellyfinWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	err := p.Stop(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{
		AccessToken: server.URL + "/webhook/jellyfin/tok",
	}, watchsync.ScrobbleEvent{Kind: historyimport.KindMovie, TMDBID: "603", Completed: false})
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	if got.Event != "Stop" || got.Item.UserData.Played {
		t.Fatalf("payload = %#v, want Stop with Played false", got)
	}
}

func TestStartMovieMarksUnplayed(t *testing.T) {
	var got jellyfinWebhookPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	err := p.Start(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{
		AccessToken: server.URL + "/webhook/jellyfin/tok",
	}, watchsync.ScrobbleEvent{Kind: historyimport.KindMovie, TMDBID: "603"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if got.Event != "Play" || got.Item.UserData.Played {
		t.Fatalf("payload = %#v, want Play with Played false", got)
	}
}

func TestEpisodeWithoutTVDBOrIMDbIsRejected(t *testing.T) {
	p := NewProvider(http.DefaultClient)
	err := p.Stop(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{
		AccessToken: "https://yamtrack.example.com/webhook/jellyfin/tok",
	}, watchsync.ScrobbleEvent{
		Kind:          historyimport.KindEpisode,
		TMDBID:        "1396",
		SeriesTMDBID:  "1396",
		SeasonNumber:  1,
		EpisodeNumber: 1,
		Completed:     true,
	})
	if err == nil || !strings.Contains(err.Error(), "TVDB or IMDb") {
		t.Fatalf("err = %v, want TVDB/IMDb requirement", err)
	}
}

func TestMovieWithoutTMDBOrIMDbIsRejected(t *testing.T) {
	p := NewProvider(http.DefaultClient)
	err := p.Stop(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{
		AccessToken: "https://yamtrack.example.com/webhook/jellyfin/tok",
	}, watchsync.ScrobbleEvent{
		Kind:      historyimport.KindMovie,
		Completed: true,
	})
	if err == nil || !strings.Contains(err.Error(), "TMDB or IMDb") {
		t.Fatalf("err = %v, want TMDB/IMDb requirement", err)
	}
}
