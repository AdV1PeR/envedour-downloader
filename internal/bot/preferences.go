package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-redis/redis/v8"
)

type UserPreferences struct {
	Quality   string `json:"quality"`    // "best", "1080p", "720p", "480p", "360p", "audio"
	MediaType string `json:"media_type"` // "video" or "audio"
}

type PreferencesStore struct {
	client *redis.Client
	ctx    context.Context
}

func NewPreferencesStore(client *redis.Client) *PreferencesStore {
	return &PreferencesStore{
		client: client,
		ctx:    context.Background(),
	}
}

func (p *PreferencesStore) GetPreferences(chatID int64) *UserPreferences {
	key := fmt.Sprintf("prefs:%d", chatID)
	data, err := p.client.Get(p.ctx, key).Result()
	if err != nil {
		// Return defaults
		return &UserPreferences{
			Quality:   "best",
			MediaType: "video",
		}
	}

	var prefs UserPreferences
	if err := json.Unmarshal([]byte(data), &prefs); err != nil {
		return &UserPreferences{
			Quality:   "best",
			MediaType: "video",
		}
	}

	return &prefs
}

func (p *PreferencesStore) SetQuality(chatID int64, quality string) error {
	prefs := p.GetPreferences(chatID)
	prefs.Quality = quality
	if quality == "audio" {
		prefs.MediaType = "audio"
	} else {
		prefs.MediaType = "video"
	}
	return p.SavePreferences(chatID, prefs)
}

func (p *PreferencesStore) SetMediaType(chatID int64, mediaType string) error {
	prefs := p.GetPreferences(chatID)
	prefs.MediaType = mediaType
	if mediaType == "audio" {
		prefs.Quality = "audio"
	}
	return p.SavePreferences(chatID, prefs)
}

func (p *PreferencesStore) SavePreferences(chatID int64, prefs *UserPreferences) error {
	key := fmt.Sprintf("prefs:%d", chatID)
	data, err := json.Marshal(prefs)
	if err != nil {
		return err
	}
	return p.client.Set(p.ctx, key, data, 30*24*time.Hour).Err() // 30 days expiry
}

// PendingDownload is the staged state between the two inline-keyboard steps:
// the user picks quality first, and for playlists a delivery mode second.
type PendingDownload struct {
	URL        string `json:"url"`
	IsPlaylist bool   `json:"is_playlist"`
	Quality    string `json:"quality,omitempty"`    // set after the quality step
	MediaType  string `json:"media_type,omitempty"` // set after the quality step
}

// SavePending stores the staged download state under pending_url:<jobID>.
func (p *PreferencesStore) SavePending(jobID string, pending *PendingDownload) error {
	key := fmt.Sprintf("pending_url:%s", jobID)
	data, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return p.client.Set(p.ctx, key, data, 10*time.Minute).Err() // 10 minutes expiry
}

// GetPending retrieves the staged download state. With consume=true the key
// is deleted (final step); otherwise it survives for the next keyboard step.
func (p *PreferencesStore) GetPending(jobID string, consume bool) (*PendingDownload, error) {
	key := fmt.Sprintf("pending_url:%s", jobID)
	data, err := p.client.Get(p.ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if consume {
		p.client.Del(p.ctx, key)
	}
	var pending PendingDownload
	if err := json.Unmarshal([]byte(data), &pending); err != nil {
		// Legacy value: a bare URL string saved before playlists existed.
		return &PendingDownload{URL: data}, nil
	}
	return &pending, nil
}

// DeletePending drops the staged state without reading it.
func (p *PreferencesStore) DeletePending(jobID string) {
	p.client.Del(p.ctx, fmt.Sprintf("pending_url:%s", jobID))
}
