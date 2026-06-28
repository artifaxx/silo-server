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
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

var _ watchsync.APIKeyWithBaseURLAuthProvider = (*Provider)(nil)

func TestConnectWithAPIKeyAndBaseURLStoresNormalizedBaseURL(t *testing.T) {
	t.Parallel()
	p := NewProvider(nil)
	tokens, account, err := p.ConnectWithAPIKeyAndBaseURL(context.Background(), " token ", "https://yamtrack.example.com/")
	if err != nil {
		t.Fatalf("ConnectWithAPIKeyAndBaseURL: %v", err)
	}
	if tokens.AccessToken != "token" {
		t.Fatalf("access token = %q, want token", tokens.AccessToken)
	}
	if account.ID != "https://yamtrack.example.com" {
		t.Fatalf("account id = %q", account.ID)
	}
	if account.Username != "yamtrack" {
		t.Fatalf("username = %q", account.Username)
	}
}

func TestConnectWithAPIKeyAndBaseURLRejectsMissingFields(t *testing.T) {
	t.Parallel()
	p := NewProvider(nil)
	if _, _, err := p.ConnectWithAPIKeyAndBaseURL(context.Background(), "", "https://yamtrack.example.com"); err == nil {
		t.Fatal("expected empty token error")
	}
	if _, _, err := p.ConnectWithAPIKeyAndBaseURL(context.Background(), "token", ""); err == nil {
		t.Fatal("expected empty base url error")
	}
}

func TestBuildJellyfinWebhookMoviePlay(t *testing.T) {
	t.Parallel()
	payload, err := buildJellyfinWebhook(watchsync.ScrobbleEvent{
		Kind:   historyimport.KindMovie,
		TMDBID: "603",
		IMDbID: "tt0133093",
	}, "Play", false)
	if err != nil {
		t.Fatalf("buildJellyfinWebhook: %v", err)
	}
	if payload.Event != "Play" {
		t.Fatalf("event = %q", payload.Event)
	}
	if payload.Item.Type != "Movie" {
		t.Fatalf("type = %q", payload.Item.Type)
	}
	if payload.Item.UserData.Played {
		t.Fatal("expected played=false on play")
	}
	if payload.Item.ProviderIds["Tmdb"] != "603" || payload.Item.ProviderIds["Imdb"] != "tt0133093" {
		t.Fatalf("provider ids = %#v", payload.Item.ProviderIds)
	}
}

func TestBuildJellyfinWebhookMovieStopCompleted(t *testing.T) {
	t.Parallel()
	payload, err := buildJellyfinWebhook(watchsync.ScrobbleEvent{
		Kind:   historyimport.KindMovie,
		TMDBID: "603",
	}, "Stop", true)
	if err != nil {
		t.Fatalf("buildJellyfinWebhook: %v", err)
	}
	if payload.Event != "Stop" || !payload.Item.UserData.Played {
		t.Fatalf("payload = %#v", payload)
	}
}

func TestBuildJellyfinWebhookMovieStopIncomplete(t *testing.T) {
	t.Parallel()
	payload, err := buildJellyfinWebhook(watchsync.ScrobbleEvent{
		Kind:   historyimport.KindMovie,
		TMDBID: "603",
	}, "Stop", false)
	if err != nil {
		t.Fatalf("buildJellyfinWebhook: %v", err)
	}
	if payload.Item.UserData.Played {
		t.Fatal("expected played=false")
	}
}

func TestBuildJellyfinWebhookEpisodePlay(t *testing.T) {
	t.Parallel()
	payload, err := buildJellyfinWebhook(watchsync.ScrobbleEvent{
		Kind:          historyimport.KindEpisode,
		TVDBID:        "364731",
		TMDBID:        "62085",
		IMDbID:        "tt0583435",
		SeasonNumber:  1,
		EpisodeNumber: 3,
	}, "Play", false)
	if err != nil {
		t.Fatalf("buildJellyfinWebhook: %v", err)
	}
	if payload.Item.Type != "Episode" {
		t.Fatalf("type = %q", payload.Item.Type)
	}
	if payload.Item.ParentIndexNumber != 1 || payload.Item.IndexNumber != 3 {
		t.Fatalf("season/episode = %d/%d", payload.Item.ParentIndexNumber, payload.Item.IndexNumber)
	}
	if payload.Item.ProviderIds["Tvdb"] != "364731" {
		t.Fatalf("tvdb = %q", payload.Item.ProviderIds["Tvdb"])
	}
}

func TestBuildJellyfinWebhookEpisodeStopCompleted(t *testing.T) {
	t.Parallel()
	payload, err := buildJellyfinWebhook(watchsync.ScrobbleEvent{
		Kind:          historyimport.KindEpisode,
		TVDBID:        "364731",
		SeasonNumber:  1,
		EpisodeNumber: 3,
	}, "Stop", true)
	if err != nil {
		t.Fatalf("buildJellyfinWebhook: %v", err)
	}
	if !payload.Item.UserData.Played {
		t.Fatal("expected played=true")
	}
}

func TestBuildJellyfinWebhookSkipsEmptyIDs(t *testing.T) {
	t.Parallel()
	_, err := buildJellyfinWebhook(watchsync.ScrobbleEvent{Kind: historyimport.KindMovie}, "Play", false)
	if err == nil {
		t.Fatal("expected skip error")
	}
	if !strings.Contains(err.Error(), "no provider ids") {
		t.Fatalf("error = %v", err)
	}
}

func TestStartAndStopPostExpectedWebhook(t *testing.T) {
	t.Parallel()
	var bodies []map[string]any
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("decode body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		bodies = append(bodies, payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	conn := watchsync.Connection{
		ProviderAccountID: server.URL,
		AccessToken:       "secret-token",
	}
	event := watchsync.ScrobbleEvent{
		Kind:   historyimport.KindMovie,
		TMDBID: "603",
	}

	if err := p.Start(context.Background(), watchsync.ServerConfig{}, conn, event); err != nil {
		t.Fatalf("Start: %v", err)
	}
	event.Completed = true
	if err := p.Stop(context.Background(), watchsync.ServerConfig{}, conn, event); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if len(paths) != 2 {
		t.Fatalf("paths = %#v", paths)
	}
	if !strings.HasSuffix(paths[0], "/integrations/webhook/jellyfin/secret-token") {
		t.Fatalf("path = %q", paths[0])
	}
	if bodies[0]["Event"] != "Play" {
		t.Fatalf("start event = %#v", bodies[0])
	}
	if bodies[1]["Event"] != "Stop" {
		t.Fatalf("stop event = %#v", bodies[1])
	}
	item := bodies[1]["Item"].(map[string]any)
	userData := item["UserData"].(map[string]any)
	if userData["Played"] != true {
		t.Fatalf("stop played = %#v", userData)
	}
}

func TestPauseDoesNotPost(t *testing.T) {
	t.Parallel()
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	err := p.Pause(context.Background(), watchsync.ServerConfig{}, watchsync.Connection{
		ProviderAccountID: server.URL,
		AccessToken:       "token",
	}, watchsync.ScrobbleEvent{Kind: historyimport.KindMovie, TMDBID: "1"})
	if err != nil {
		t.Fatalf("Pause: %v", err)
	}
	if called {
		t.Fatal("expected no webhook on pause")
	}
}

func TestWebhookURLIncludesTokenSegment(t *testing.T) {
	t.Parallel()
	target, err := webhookURL("https://yamtrack.example.com", "abc123")
	if err != nil {
		t.Fatalf("webhookURL: %v", err)
	}
	want := "https://yamtrack.example.com/integrations/webhook/jellyfin/abc123"
	if target != want {
		t.Fatalf("url = %q, want %q", target, want)
	}
}

func TestPostWebhookReturnsInvalidTokenError(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	client := newWebhookClient(server.Client())
	err := client.postWebhook(context.Background(), server.URL, "token", jellyfinWebhook{
		Event: "Play",
		Item: jellyfinItem{
			Type:        "Movie",
			ProviderIds: map[string]string{"Tmdb": "1"},
			UserData:    jellyfinUserData{Played: false},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "invalid yamtrack webhook token") {
		t.Fatalf("error = %v", err)
	}
}
