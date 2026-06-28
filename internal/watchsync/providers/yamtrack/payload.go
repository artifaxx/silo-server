package yamtrack

import (
	"errors"
	"strings"

	"github.com/Silo-Server/silo-server/internal/historyimport"
	"github.com/Silo-Server/silo-server/internal/watchsync"
)

var errSkipScrobble = errors.New("yamtrack scrobble skipped: no provider ids")

type jellyfinWebhook struct {
	Event string       `json:"Event"`
	Item  jellyfinItem `json:"Item"`
}

type jellyfinItem struct {
	Type              string            `json:"Type"`
	Name              string            `json:"Name,omitempty"`
	ProductionYear    int               `json:"ProductionYear,omitempty"`
	SeriesName        string            `json:"SeriesName,omitempty"`
	ParentIndexNumber int               `json:"ParentIndexNumber,omitempty"`
	IndexNumber       int               `json:"IndexNumber,omitempty"`
	ProviderIds       map[string]string `json:"ProviderIds"`
	UserData          jellyfinUserData  `json:"UserData"`
}

type jellyfinUserData struct {
	Played bool `json:"Played"`
}

func buildJellyfinWebhook(event watchsync.ScrobbleEvent, eventName string, played bool) (jellyfinWebhook, error) {
	providerIDs := providerIDsFromEvent(event)
	if len(providerIDs) == 0 {
		return jellyfinWebhook{}, errSkipScrobble
	}

	item := jellyfinItem{
		ProviderIds: providerIDs,
		UserData:    jellyfinUserData{Played: played},
	}

	switch event.Kind {
	case historyimport.KindEpisode:
		item.Type = "Episode"
		item.ParentIndexNumber = event.SeasonNumber
		item.IndexNumber = event.EpisodeNumber
	default:
		item.Type = "Movie"
	}

	return jellyfinWebhook{
		Event: eventName,
		Item:  item,
	}, nil
}

func providerIDsFromEvent(event watchsync.ScrobbleEvent) map[string]string {
	ids := make(map[string]string)
	switch event.Kind {
	case historyimport.KindEpisode:
		setProviderID(ids, "Tvdb", event.TVDBID)
		setProviderID(ids, "Tmdb", event.TMDBID)
		setProviderID(ids, "Imdb", event.IMDbID)
	default:
		setProviderID(ids, "Tmdb", event.TMDBID)
		setProviderID(ids, "Imdb", event.IMDbID)
		setProviderID(ids, "Tvdb", event.TVDBID)
	}
	if len(ids) == 0 {
		return nil
	}
	return ids
}

func setProviderID(ids map[string]string, key, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	ids[key] = value
}
