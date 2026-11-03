package bot

import (
	"testing"
	"time"

	"envedour-bot/internal/config"
)

func TestIsGroupAllowed(t *testing.T) {
	b := &Bot{config: &config.Config{AllowedGroupIDs: []int64{-100123, -100456}}}

	tests := []struct {
		chatID int64
		want   bool
	}{
		{-100123, true},
		{-100456, true},
		{-100999, false},
		{12345, false}, // private chat IDs are never in the group list
		{0, false},
	}

	for _, tt := range tests {
		if got := b.isGroupAllowed(tt.chatID); got != tt.want {
			t.Errorf("isGroupAllowed(%d) = %t, want %t", tt.chatID, got, tt.want)
		}
	}
}

func TestIsYouTubePlaylistURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.youtube.com/playlist?list=PLabc123", true},
		{"https://youtube.com/playlist?list=PLabc123", true},
		{"https://music.youtube.com/playlist?list=PLabc123", true},
		{"https://www.youtube.com/playlist/?list=PLabc123", true},
		// A watch link carrying a list stays a single video by design.
		{"https://www.youtube.com/watch?v=aYe-2Glruu4&list=PLabc123", false},
		{"https://youtu.be/aYe-2Glruu4?list=PLabc123", false},
		// A playlist path without the list parameter is not a playlist.
		{"https://www.youtube.com/playlist", false},
		// Non-YouTube hosts are never playlists.
		{"https://example.com/playlist?list=PLabc123", false},
		{"not a url", false},
	}

	for _, tt := range tests {
		if got := isYouTubePlaylistURL(tt.url); got != tt.want {
			t.Errorf("isYouTubePlaylistURL(%q) = %t, want %t", tt.url, got, tt.want)
		}
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter()

	// Limit 0 disables the limiter entirely.
	for i := 0; i < 100; i++ {
		if ok, _ := rl.allow(1, 0, time.Hour); !ok {
			t.Fatalf("limit 0 must always allow")
		}
	}

	// limit=2: the first two pass, the third is rejected with a wait time.
	if ok, wait := rl.allow(2, 2, time.Hour); !ok || wait != 0 {
		t.Fatalf("first request: ok=%t wait=%v, want ok=true", ok, wait)
	}
	if ok, wait := rl.allow(2, 2, time.Hour); !ok || wait != 0 {
		t.Fatalf("second request: ok=%t wait=%v, want ok=true", ok, wait)
	}
	ok, wait := rl.allow(2, 2, time.Hour)
	if ok {
		t.Fatal("third request must be rejected")
	}
	if wait <= 0 || wait > time.Hour {
		t.Fatalf("wait = %v, want in (0, 1h]", wait)
	}

	// Another user is unaffected by the first user's hits.
	if ok, _ := rl.allow(3, 2, time.Hour); !ok {
		t.Fatal("second user must not be limited by the first user's hits")
	}
}

func TestURLDedup(t *testing.T) {
	d := newURLDedup(5 * time.Minute)

	if d.seen(1, "https://example.com/v1") {
		t.Fatal("first occurrence must not be reported as seen")
	}
	if !d.seen(1, "https://example.com/v1") {
		t.Fatal("second occurrence must be reported as seen")
	}
	// Same URL from another user is a different key.
	if d.seen(2, "https://example.com/v1") {
		t.Fatal("another user posting the same link must not be deduped")
	}
	// A different URL from the first user is fine.
	if d.seen(1, "https://example.com/v2") {
		t.Fatal("a new link must not be deduped")
	}
}
