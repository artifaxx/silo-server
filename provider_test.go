package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

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

func TestExchangeAPIKeyProbesEmptyPayload(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotBody, _ = io.ReadAll(r.Body)
		if len(gotBody) > 0 {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("Missing payload"))
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	webhook := server.URL + "/webhook/jellyfin/test-token"
	resp, err := p.ExchangeAPIKey(context.Background(), &pluginv1.WatchSyncExchangeAPIKeyRequest{ApiKey: webhook})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.GetFault() != nil {
		t.Fatalf("fault = %#v", resp.GetFault())
	}
	if gotPath != "/webhook/jellyfin/test-token" {
		t.Fatalf("path = %q", gotPath)
	}
	if len(gotBody) != 0 {
		t.Fatalf("probe body = %q, want empty", gotBody)
	}
	if resp.GetCredentials().GetAccessToken() != webhook {
		t.Fatalf("stored token = %q", resp.GetCredentials().GetAccessToken())
	}
	if resp.GetAccount().GetExternalSubject() != "test-token" {
		t.Fatalf("account = %#v", resp.GetAccount())
	}
}

func TestExchangeAPIKeyRejectsUnauthorized(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ExchangeAPIKey(context.Background(), &pluginv1.WatchSyncExchangeAPIKeyRequest{
		ApiKey: server.URL + "/webhook/jellyfin/bad",
	})
	if err != nil {
		t.Fatalf("exchange: %v", err)
	}
	if resp.GetFault().GetCode() != pluginv1.WatchSyncFaultCode_WATCH_SYNC_FAULT_CODE_INVALID_CREDENTIAL {
		t.Fatalf("fault = %#v", resp.GetFault())
	}
}

func TestPauseIsNoOp(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("pause should not call Yamtrack")
	}))
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "pause-1",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_PAUSE,
			Media:     movieMedia("603", "", false),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_NO_CHANGE {
		t.Fatalf("result = %#v", resp.GetResults()[0])
	}
}

func TestStopCompletedMoviePostsJellyfinPayload(t *testing.T) {
	var got jellyfinWebhookPayload
	server := capturePayload(t, &got)
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "stop-1",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
			Media:     movieMedia("603", "tt0133093", true),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED {
		t.Fatalf("result = %#v", resp.GetResults()[0])
	}
	if got.Event != "Stop" || got.Item.Type != "Movie" || !got.Item.UserData.Played {
		t.Fatalf("payload = %#v", got)
	}
	if got.Item.ProviderIds["Tmdb"] != "603" || got.Item.ProviderIds["Imdb"] != "tt0133093" {
		t.Fatalf("provider ids = %#v", got.Item.ProviderIds)
	}
}

func TestStopCompletedEpisodeUsesSeriesTMDB(t *testing.T) {
	var got jellyfinWebhookPayload
	server := capturePayload(t, &got)
	defer server.Close()

	p := NewProvider(server.Client())
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "stop-ep",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
			Media: &pluginv1.WatchSyncMedia{
				MediaType:         pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE,
				SeasonNumber:      1,
				EpisodeNumber:     1,
				ExternalIds:       map[string]string{"tvdb": "303821", "imdb": "tt0583459", "tmdb": "62085"},
				SeriesExternalIds: map[string]string{"tmdb": "1396", "tvdb": "75930"},
				Metadata:          completedMetadata(t),
			},
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_APPLIED {
		t.Fatalf("result = %#v", resp.GetResults()[0])
	}
	if got.Item.Type != "Episode" || got.Item.ProviderIds["Tvdb"] != "303821" {
		t.Fatalf("payload = %#v", got)
	}
	if got.Item.ProviderIds["Tmdb"] != "1396" {
		t.Fatalf("tmdb = %q, want series id", got.Item.ProviderIds["Tmdb"])
	}
}

func TestStartMovieMarksUnplayed(t *testing.T) {
	var got jellyfinWebhookPayload
	server := capturePayload(t, &got)
	defer server.Close()

	p := NewProvider(server.Client())
	_, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "start-1",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_START,
			Media:     movieMedia("603", "", false),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Event != "Play" || got.Item.UserData.Played {
		t.Fatalf("payload = %#v, want Play with Played false", got)
	}
}

func TestStopIncompleteMovieMarksUnplayed(t *testing.T) {
	var got jellyfinWebhookPayload
	server := capturePayload(t, &got)
	defer server.Close()

	p := NewProvider(server.Client())
	_, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated(server.URL + "/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:           "stop-incomplete",
			Operation:         pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
			CompletionPercent: 12,
			Media:             movieMedia("603", "", false),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if got.Event != "Stop" || got.Item.UserData.Played {
		t.Fatalf("payload = %#v, want Stop with Played false", got)
	}
}

func TestEpisodeWithoutTVDBOrIMDbIsRejected(t *testing.T) {
	p := NewProvider(http.DefaultClient)
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated("https://yamtrack.example.com/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "ep-tmdb-only",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_SCROBBLE_STOP,
			Media: &pluginv1.WatchSyncMedia{
				MediaType:         pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_EPISODE,
				ExternalIds:       map[string]string{"tmdb": "1396"},
				SeriesExternalIds: map[string]string{"tmdb": "1396"},
				Metadata:          completedMetadata(t),
			},
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := resp.GetResults()[0]
	if got.GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED ||
		!strings.Contains(got.GetFault().GetSafeMessage(), "TVDB or IMDb") {
		t.Fatalf("result = %#v", got)
	}
}

func TestNonScrobbleIsRejected(t *testing.T) {
	p := NewProvider(http.DefaultClient)
	resp, err := p.ApplyEvents(context.Background(), &pluginv1.WatchSyncApplyEventsRequest{
		Context: authenticated("https://yamtrack.example.com/webhook/jellyfin/tok"),
		Events: []*pluginv1.WatchSyncEvent{{
			EventId:   "mark-watched",
			Operation: pluginv1.WatchSyncOperation_WATCH_SYNC_OPERATION_MARK_WATCHED,
			Media:     movieMedia("603", "", true),
		}},
	})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if resp.GetResults()[0].GetStatus() != pluginv1.WatchSyncApplyStatus_WATCH_SYNC_APPLY_STATUS_REJECTED {
		t.Fatalf("result = %#v", resp.GetResults()[0])
	}
}

func capturePayload(t *testing.T, got *jellyfinWebhookPayload) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(got); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func authenticated(webhookURL string) *pluginv1.WatchSyncAuthenticatedContext {
	return &pluginv1.WatchSyncAuthenticatedContext{
		Credentials: &pluginv1.WatchSyncCredentials{AccessToken: webhookURL},
	}
}

func movieMedia(tmdbID, imdbID string, completed bool) *pluginv1.WatchSyncMedia {
	ids := map[string]string{}
	if tmdbID != "" {
		ids["tmdb"] = tmdbID
	}
	if imdbID != "" {
		ids["imdb"] = imdbID
	}
	media := &pluginv1.WatchSyncMedia{
		MediaType:   pluginv1.WatchSyncMediaType_WATCH_SYNC_MEDIA_TYPE_MOVIE,
		ExternalIds: ids,
	}
	if completed {
		metadata, _ := structpb.NewStruct(map[string]any{"completed": true})
		media.Metadata = metadata
	}
	return media
}

func completedMetadata(t *testing.T) *structpb.Struct {
	t.Helper()
	metadata, err := structpb.NewStruct(map[string]any{"completed": true})
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}
