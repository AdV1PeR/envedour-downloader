package executor

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"envedour-bot/internal/config"
	"envedour-bot/internal/queue"
)

const fakeYTDLPHelp = `#!/bin/sh
echo "Usage: yt-dlp [OPTIONS] URL [URL...]"
echo ""
echo "  --js-runtimes RUNTIME[:PATH]    Additional JavaScript runtime to enable"
`

func writeFakeBinary(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func setupFakePath(t *testing.T, scripts map[string]string) string {
	t.Helper()
	binDir := t.TempDir()
	for name, script := range scripts {
		writeFakeBinary(t, binDir, name, script)
	}
	t.Setenv("PATH", binDir)
	t.Setenv("YTDLP_JS_RUNTIME", "")
	return binDir
}

func TestIsYouTubeURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://youtu.be/aYe-2Glruu4", true},
		{"https://www.youtube.com/watch?v=aYe-2Glruu4", true},
		{"https://music.youtube.com/watch?v=aYe-2Glruu4", true},
		{"https://youtube-nocookie.com/embed/aYe-2Glruu4", true},
		{"https://notyoutube.com/watch?v=aYe-2Glruu4", false},
		{"https://example.com/?next=youtube.com", false},
	}

	for _, tt := range tests {
		if got := isYouTubeURL(tt.url); got != tt.want {
			t.Errorf("isYouTubeURL(%q) = %t, want %t", tt.url, got, tt.want)
		}
	}
}

func TestIsTikTokURL(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://www.tiktok.com/@user/video/123", true},
		{"https://vt.tiktok.com/ZSa4WqjU7/", true},
		{"https://tiktok.com/@user/video/123", true},
		{"https://notiktok.com/@user/video/123", false},
		{"https://example.com/tiktok.com", false},
	}

	for _, tt := range tests {
		if got := isTikTokURL(tt.url); got != tt.want {
			t.Errorf("isTikTokURL(%q) = %t, want %t", tt.url, got, tt.want)
		}
	}
}

func TestAddYTDLPJavaScriptRuntimeUsesDenoWhenAvailable(t *testing.T) {
	resetJSRuntimeCacheForTest()
	binDir := setupFakePath(t, map[string]string{
		"yt-dlp": fakeYTDLPHelp,
		"deno":   "#!/bin/sh\necho 'deno 2.9.8 (stable, release)'\n",
	})
	denoPath := filepath.Join(binDir, "deno")

	args := addYTDLPJavaScriptRuntime([]string{"--no-cache-dir"})
	want := []string{"--no-cache-dir", "--js-runtimes", "deno:" + denoPath}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected yt-dlp args: %v, want %v", args, want)
	}
}

func TestAddYTDLPJavaScriptRuntimeIgnoresOldNode(t *testing.T) {
	resetJSRuntimeCacheForTest()
	setupFakePath(t, map[string]string{
		"yt-dlp": fakeYTDLPHelp,
		"node":   "#!/bin/sh\necho v20.20.2\n",
	})

	args := addYTDLPJavaScriptRuntime([]string{"--no-cache-dir"})
	if len(args) != 1 || args[0] != "--no-cache-dir" {
		t.Fatalf("old Node should not be passed to yt-dlp, got: %v", args)
	}
}

func TestAddYTDLPJavaScriptRuntimeUsesSupportedNode(t *testing.T) {
	resetJSRuntimeCacheForTest()
	binDir := setupFakePath(t, map[string]string{
		"yt-dlp": fakeYTDLPHelp,
		"node":   "#!/bin/sh\necho v22.1.0\n",
	})
	nodePath := filepath.Join(binDir, "node")

	args := addYTDLPJavaScriptRuntime([]string{"--no-cache-dir"})
	want := []string{"--no-cache-dir", "--js-runtimes", "node:" + nodePath}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected yt-dlp args: %v, want %v", args, want)
	}
}

func TestAddYTDLPJavaScriptRuntimeAddsAllSupportedRuntimes(t *testing.T) {
	resetJSRuntimeCacheForTest()
	binDir := setupFakePath(t, map[string]string{
		"yt-dlp": fakeYTDLPHelp,
		"deno":   "#!/bin/sh\necho 'deno 2.9.8'\n",
		"node":   "#!/bin/sh\necho v22.11.0\n",
		"qjs":    "#!/bin/sh\necho 'QuickJS-ng version 0.10.1'\n",
		"bun":    "#!/bin/sh\necho 1.2.11\n",
	})

	args := addYTDLPJavaScriptRuntime([]string{"--no-cache-dir"})
	want := []string{
		"--no-cache-dir",
		"--js-runtimes", "deno:" + filepath.Join(binDir, "deno"),
		"--js-runtimes", "node:" + filepath.Join(binDir, "node"),
		"--js-runtimes", "quickjs:" + filepath.Join(binDir, "qjs"),
		"--js-runtimes", "bun:" + filepath.Join(binDir, "bun"),
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected yt-dlp args: %v, want %v", args, want)
	}
}

func TestAddYTDLPJavaScriptRuntimeDoesNothingWithoutRuntimes(t *testing.T) {
	resetJSRuntimeCacheForTest()
	setupFakePath(t, map[string]string{
		"yt-dlp": fakeYTDLPHelp,
	})

	args := addYTDLPJavaScriptRuntime([]string{"--no-cache-dir"})
	if len(args) != 1 || args[0] != "--no-cache-dir" {
		t.Fatalf("expected args to be unchanged, got: %v", args)
	}
}

func TestAddYTDLPJavaScriptRuntimeSkipsOldYTDLP(t *testing.T) {
	resetJSRuntimeCacheForTest()
	setupFakePath(t, map[string]string{
		"yt-dlp": "#!/bin/sh\necho 'Usage: yt-dlp [OPTIONS]'\n",
		"deno":   "#!/bin/sh\necho 'deno 2.9.8'\n",
	})

	args := addYTDLPJavaScriptRuntime([]string{"--no-cache-dir"})
	if len(args) != 1 || args[0] != "--no-cache-dir" {
		t.Fatalf("old yt-dlp must not receive --js-runtimes, got: %v", args)
	}
}

func TestAddYTDLPJavaScriptRuntimeHonorsOverride(t *testing.T) {
	resetJSRuntimeCacheForTest()
	setupFakePath(t, map[string]string{
		"yt-dlp": fakeYTDLPHelp,
	})
	t.Setenv("YTDLP_JS_RUNTIME", "deno:/opt/custom/deno")

	args := addYTDLPJavaScriptRuntime([]string{"--no-cache-dir"})
	want := []string{"--no-cache-dir", "--js-runtimes", "deno:/opt/custom/deno"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected yt-dlp args: %v, want %v", args, want)
	}
}

func TestVersionAtLeast(t *testing.T) {
	tests := []struct {
		have []int
		want []int
		ok   bool
	}{
		{[]int{2, 9, 8}, []int{2, 3, 0}, true},
		{[]int{2, 2, 0}, []int{2, 3, 0}, false},
		{[]int{22, 0, 0}, []int{22, 0, 0}, true},
		{[]int{22}, []int{22, 0, 1}, false},
		{[]int{2024, 1, 1}, []int{2023, 12, 9}, true},
	}

	for _, tt := range tests {
		if got := versionAtLeast(tt.have, tt.want); got != tt.ok {
			t.Errorf("versionAtLeast(%v, %v) = %t, want %t", tt.have, tt.want, got, tt.ok)
		}
	}
}

func TestTruncateFilenameKeepsValidUTF8(t *testing.T) {
	name := strings.Repeat("Ж", 200)
	got := truncateFilename(name, 160)
	if len(got) > 160 {
		t.Fatalf("truncateFilename returned %d bytes, want <= 160", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("truncateFilename split a UTF-8 rune: %q", got)
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"a/b\\c:d*e?f\"g<h>i|j", "a b c d e f g h i j"},
		{"Сёстры Победы | Полная версия", "Сёстры Победы Полная версия"},
		{"  hello   world  ", "hello world"},
		{"line\nbreak", "line break"},
		{"...", ""},
	}

	for _, tt := range tests {
		if got := sanitizeFilename(tt.in); got != tt.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNeedsRemuxToMp4(t *testing.T) {
	tests := []struct {
		ext  string
		want bool
	}{
		{".unknown_video", true},
		{".unknown_audio", true},
		{".UNKNOWN_VIDEO", true},
		{".mp4", false},
		{".webm", false},
		{".mkv", false},
		{".mp3", false},
		{"", false},
	}

	for _, tt := range tests {
		if got := needsRemuxToMp4(tt.ext); got != tt.want {
			t.Errorf("needsRemuxToMp4(%q) = %t, want %t", tt.ext, got, tt.want)
		}
	}
}

func TestMediaTypeArgsVideoForcesMp4Container(t *testing.T) {
	e := &Executor{}
	for _, quality := range []string{"", "best", "1080p", "720p", "480p", "360p"} {
		args := e.mediaTypeArgs("video", quality)
		joined := strings.Join(args, "\x00")
		for _, want := range []string{"--format", "--merge-output-format\x00mp4", "--remux-video\x00mp4"} {
			if !strings.Contains(joined, want) {
				t.Errorf("mediaTypeArgs(video, %q) = %v, missing %q", quality, args, want)
			}
		}
	}
}

func TestMediaTypeArgsAudioLeavesContainerToPostprocessing(t *testing.T) {
	e := &Executor{}
	args := e.mediaTypeArgs("audio", "best")
	joined := strings.Join(args, "\x00")
	for _, want := range []string{"-x", "--audio-format\x00mp3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("mediaTypeArgs(audio, \"best\") = %v, missing %q", args, want)
		}
	}
	if strings.Contains(joined, "--remux-video") || strings.Contains(joined, "--merge-output-format") {
		t.Errorf("audio args must not force a video container: %v", args)
	}
}

func TestProbeVideoParsesMetadata(t *testing.T) {
	setupFakePath(t, map[string]string{
		"ffprobe": "#!/bin/sh\necho '{\"streams\":[{\"width\":720,\"height\":1280}],\"format\":{\"duration\":\"12.345\"}}'\n",
		"ffmpeg":  "#!/bin/sh\nfor last; do :; done\necho jpeg > \"$last\"\n",
	})
	e := &Executor{config: &config.Config{TmpfsPath: t.TempDir()}}

	meta := e.probeVideo("/fake/video.mp4")
	if meta.width != 720 || meta.height != 1280 || meta.duration != 12 {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if meta.thumb == "" {
		t.Fatal("expected a thumbnail to be generated")
	}
	os.Remove(meta.thumb)
}

func TestProbeVideoToleratesFailures(t *testing.T) {
	setupFakePath(t, map[string]string{
		"ffprobe": "#!/bin/sh\necho boom >&2\nexit 1\n",
		"ffmpeg":  "#!/bin/sh\nexit 1\n",
	})
	e := &Executor{config: &config.Config{TmpfsPath: t.TempDir()}}

	meta := e.probeVideo("/fake/video.mp4")
	if meta.duration != 0 || meta.width != 0 || meta.height != 0 || meta.thumb != "" {
		t.Fatalf("expected zero metadata, got %+v", meta)
	}
}

func TestCookieErrorPlatform(t *testing.T) {
	loginErr := "ERROR: [Instagram] abc: Requested content is not available, rate-limit reached or login required. Use --cookies"
	netErr := "ERROR: unable to download video data: HTTP Error 502"

	tests := []struct {
		url     string
		errText string
		want    string
	}{
		{"https://www.instagram.com/reel/DTxn2UhjDbE/", loginErr, "Instagram"},
		{"https://instagram.com/p/xyz/", loginErr, "Instagram"},
		{"https://www.tiktok.com/@user/video/123", loginErr, "TikTok"},
		{"https://youtu.be/aYe-2Glruu4", loginErr, "YouTube"},
		{"https://www.youtube.com/watch?v=abc", "https error, Use --cookies for authentication", "YouTube"},
		// Errors unrelated to cookies must never trigger a reminder.
		{"https://www.instagram.com/reel/DTxn2UhjDbE/", netErr, ""},
		{"https://example.com/video", loginErr, ""},
		{"https://example.com/video", netErr, ""},
		{"not-a-url", loginErr, ""},
	}

	for _, tt := range tests {
		if got := cookieErrorPlatform(tt.url, tt.errText); got != tt.want {
			t.Errorf("cookieErrorPlatform(%q, ...) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestFetchAudioThumbnail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("fake-image-bytes"))
	}))
	defer srv.Close()

	setupFakePath(t, map[string]string{
		"ffmpeg": "#!/bin/sh\nfor last; do :; done\necho jpeg > \"$last\"\n",
	})
	e := &Executor{config: &config.Config{TmpfsPath: t.TempDir()}}

	thumb := e.fetchAudioThumbnail(context.Background(), srv.URL+"/cover.jpg")
	if thumb == "" {
		t.Fatal("expected a thumbnail to be generated")
	}
	if _, err := os.Stat(thumb); err != nil {
		t.Fatalf("thumbnail file missing: %v", err)
	}
	// The raw cover download must be cleaned up after conversion.
	entries, _ := os.ReadDir(e.config.TmpfsPath)
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".img") {
			t.Errorf("raw cover file %s was not cleaned up", entry.Name())
		}
	}
	os.Remove(thumb)
}

func TestFetchAudioThumbnailHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	e := &Executor{config: &config.Config{TmpfsPath: t.TempDir()}}
	if got := e.fetchAudioThumbnail(context.Background(), srv.URL+"/gone.jpg"); got != "" {
		t.Fatalf("expected no thumbnail on HTTP 404, got %q", got)
	}
}

func TestCleanAudioTitle(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"Пикули_Пародия_на_песню_солдатских_жен", "Пикули Пародия на песню солдатских жен"},
		{"Never Gonna Give You Up", "Never Gonna Give You Up"},
		{"  spaced   out  ", "spaced out"},
		{"line\nbreak_song", "line break song"},
	}

	for _, tt := range tests {
		if got := cleanAudioTitle(tt.in); got != tt.want {
			t.Errorf("cleanAudioTitle(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestProbeDuration(t *testing.T) {
	setupFakePath(t, map[string]string{
		"ffprobe": "#!/bin/sh\necho '{\"format\":{\"duration\":\"183.48\"}}'\n",
	})
	if got := probeDuration("/fake/audio.mp3"); got != 183 {
		t.Fatalf("probeDuration = %d, want 183", got)
	}

	setupFakePath(t, map[string]string{
		"ffprobe": "#!/bin/sh\necho boom >&2\nexit 1\n",
	})
	if got := probeDuration("/fake/audio.mp3"); got != 0 {
		t.Fatalf("probeDuration on ffprobe failure = %d, want 0", got)
	}
}

func TestParsePlaylistListing(t *testing.T) {
	// yt-dlp prints each field JSON-encoded (…j conversion), three lines per
	// entry: url, entry title, playlist title.
	out := strings.Join([]string{
		`"https://www.youtube.com/watch?v=aaa"`, `"First video"`, `"My Mix"`,
		`"https://www.youtube.com/watch?v=bbb"`, `"Second \"quoted\" video"`, `"My Mix"`,
		`"https://www.youtube.com/watch?v=ccc"`, `"NA"`, `"My Mix"`,
		`""`, `"NA"`, `"NA"`, // entries without a URL are skipped
	}, "\n")

	title, entries := parsePlaylistListing(out)
	if title != "My Mix" {
		t.Fatalf("playlist title = %q, want %q", title, "My Mix")
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].URL != "https://www.youtube.com/watch?v=aaa" || entries[0].Title != "First video" {
		t.Fatalf("entry 0 = %+v", entries[0])
	}
	if entries[1].Title != `Second "quoted" video` {
		t.Fatalf("entry 1 title = %q", entries[1].Title)
	}
	if entries[2].Title != "" {
		t.Fatalf("NA title must become empty, got %q", entries[2].Title)
	}

	if _, entries := parsePlaylistListing(""); len(entries) != 0 {
		t.Fatalf("empty listing must produce no entries, got %d", len(entries))
	}
}

func TestAppendMediaToZip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "item.mp4")
	if err := os.WriteFile(src, []byte("video-bytes"), 0644); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(dir, "out.zip")
	zf, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(zf)
	e := &Executor{}
	if err := e.appendMediaToZip(w, src, "My | Video", 7); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zf.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if len(r.File) != 1 {
		t.Fatalf("zip has %d files, want 1", len(r.File))
	}
	if r.File[0].Name != "007 My Video.mp4" {
		t.Fatalf("zip entry name = %q", r.File[0].Name)
	}
	if r.File[0].Method != zip.Store {
		t.Fatalf("zip entry method = %d, want Store", r.File[0].Method)
	}
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "video-bytes" {
		t.Fatalf("zip entry content = %q", data)
	}
}

func TestCheckTmpfsFree(t *testing.T) {
	dir := t.TempDir()
	e := &Executor{config: &config.Config{TmpfsPath: dir, MinFreeTmpfsMB: 0}}
	if err := e.checkTmpfsFree(); err != nil {
		t.Fatalf("MinFreeTmpfsMB=0 must always pass, got %v", err)
	}

	// An absurdly high requirement must fail on any real filesystem.
	e.config.MinFreeTmpfsMB = 1 << 40
	if err := e.checkTmpfsFree(); err == nil {
		t.Fatal("expected an error with an impossible free-space requirement")
	}
}

func TestInflightRecordRoundTrip(t *testing.T) {
	rec := inflightRecord{
		Job: queue.Job{
			ID:         "job_123",
			URL:        "https://www.youtube.com/playlist?list=PLabc",
			ChatID:     -100123,
			Quality:    "720p",
			MediaType:  "video",
			IsPlaylist: true,
			Delivery:   "sequential",
		},
		Done: 17,
	}

	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	var back inflightRecord
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if back.Job.ID != rec.Job.ID || back.Job.ChatID != rec.Job.ChatID ||
		back.Job.IsPlaylist != rec.Job.IsPlaylist || back.Job.Delivery != rec.Job.Delivery || back.Done != 17 {
		t.Fatalf("round trip mismatch: %+v vs %+v", back, rec)
	}
}

func TestProcessJobSafe(t *testing.T) {
	e := &Executor{config: &config.Config{}} // nil botAPI: sendMessage is a no-op

	// A panicking job must be contained: no panic escapes, the user is told
	// about the internal error.
	e.processJobSafe(context.Background(), &queue.Job{ID: "job_1", ChatID: 1}, func() {
		panic("boom")
	})

	// A normal job runs to completion.
	ran := false
	e.processJobSafe(context.Background(), &queue.Job{ID: "job_2", ChatID: 1}, func() {
		ran = true
	})
	if !ran {
		t.Fatal("normal job must run")
	}
}

func TestPlaylistBudget(t *testing.T) {
	if got := playlistBudget(0); got != playlistJobTimeout {
		t.Fatalf("playlistBudget(0) = %v, want the %v floor", got, playlistJobTimeout)
	}
	if got := playlistBudget(5); got != playlistJobTimeout {
		t.Fatalf("playlistBudget(5) = %v, want the %v floor", got, playlistJobTimeout)
	}
	if got := playlistBudget(100); got != 150*time.Minute {
		t.Fatalf("playlistBudget(100) = %v, want 150m (90s per item)", got)
	}
	if got := playlistBudget(100000); got != 12*time.Hour {
		t.Fatalf("playlistBudget(100000) = %v, want the 12h cap", got)
	}
}

func TestArchiveBuilderRotation(t *testing.T) {
	dir := t.TempDir()
	e := &Executor{config: &config.Config{ArchiveDir: dir}}                       // nil botAPI: sends fail
	b := newArchiveBuilder(e, &queue.Job{ID: "job_5", ChatID: 1}, "My Mix", 1000) // 1000-byte parts

	zipCount := func() int {
		entries, _ := os.ReadDir(dir)
		n := 0
		for _, en := range entries {
			if strings.HasSuffix(en.Name(), ".zip") {
				n++
			}
		}
		return n
	}

	writeItem := func(name string, size int) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, bytes.Repeat([]byte("x"), size), 0644); err != nil {
			t.Fatal(err)
		}
		return path
	}

	// Item 1 (600 bytes) fits into the empty 1000-byte part.
	if b.wouldOverflow(600) {
		t.Fatal("empty part must not overflow")
	}
	if err := b.append(writeItem("a.mp4", 600), "a", 1, 600); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(b.path); err != nil {
		t.Fatalf("part file missing: %v", err)
	}

	// Item 2 (500 bytes) does not fit next to 600 bytes → rotation needed.
	if !b.wouldOverflow(500) {
		t.Fatal("600+500 must overflow a 1000-byte part")
	}
	if err := b.sealAndSend(); err == nil {
		t.Fatal("sealAndSend must fail with a nil botAPI")
	}
	// After the failed send the builder is reset and the part file removed.
	if b.partNo != 2 || b.file != nil || b.bytes != 0 {
		t.Fatalf("builder not reset: partNo=%d file=%v bytes=%d", b.partNo, b.file, b.bytes)
	}
	if n := zipCount(); n != 0 {
		t.Fatalf("part files left behind: %d", n)
	}

	// The new part accepts the item again.
	if b.wouldOverflow(500) {
		t.Fatal("fresh part must not overflow")
	}
	if err := b.append(writeItem("b.mp4", 500), "b", 2, 500); err != nil {
		t.Fatal(err)
	}
	b.cleanup()
	if n := zipCount(); n != 0 {
		t.Fatalf("cleanup must remove the unsent part, left %d", n)
	}
}

func TestCancelByUser(t *testing.T) {
	e := &Executor{
		config:    &config.Config{},
		active:    make(map[string]*activeJob),
		cancelled: make(map[string]bool),
	}

	_, cancelA := context.WithCancel(context.Background())
	_, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	e.trackJob(&queue.Job{ID: "job_1", FromID: 100, URL: "https://example.com/a"}, cancelA)
	e.trackJob(&queue.Job{ID: "job_2", FromID: 200, URL: "https://example.com/b"}, cancelB)

	// Cancelling user 100 must not touch user 200's job.
	stopped := e.CancelByUser(100)
	if len(stopped) != 1 || stopped[0] != "https://example.com/a" {
		t.Fatalf("CancelByUser(100) = %v", stopped)
	}
	if !e.isCancelled("job_1") {
		t.Fatal("job_1 must be marked cancelled")
	}
	if e.isCancelled("job_2") {
		t.Fatal("job_2 belongs to another user and must not be cancelled")
	}
	cancelA() // satisfied: the context cancel fired and must not leak

	// Another user has nothing to stop.
	if got := e.CancelByUser(300); len(got) != 0 {
		t.Fatalf("CancelByUser(300) = %v, want empty", got)
	}

	// Untracking clears the cancelled flag together with the active entry.
	e.untrackJob("job_1")
	if e.isCancelled("job_1") {
		t.Fatal("cancelled flag must be cleared on untrack")
	}
}

func TestShortReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			"rate limit",
			fmt.Errorf("yt-dlp failed: exit status 1\nOutput: WARNING: [youtube] abc: nsig\nERROR: [youtube] abc: Requested content is not available, rate-limit reached or login required. Use --cookies"),
			"требуется вход или лимит запросов — нужны свежие куки",
		},
		{
			"unavailable",
			fmt.Errorf("yt-dlp failed: exit status 1\nOutput: ERROR: [youtube] abc: Video unavailable"),
			"видео недоступно",
		},
		{
			"unknown error falls back to the last ERROR line",
			fmt.Errorf("yt-dlp failed: exit status 1\nOutput: WARNING: something\nERROR: [generic] abc: Unsupported URL"),
			"[generic] abc: Unsupported URL",
		},
		{
			"no ERROR line falls back to the first line",
			fmt.Errorf("context canceled"),
			"context canceled",
		},
	}

	for _, tt := range tests {
		if got := shortReason(tt.err); got != tt.want {
			t.Errorf("%s: shortReason = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestGetFormatForQualityHighRes(t *testing.T) {
	for _, q := range []string{"1440p", "2160p", "4320p"} {
		f := (&Executor{}).getFormatForQuality(q)
		if !strings.Contains(f, "height<=") {
			t.Errorf("%s selector missing height filter: %s", q, f)
		}
		// High-res sources are often webm/vp9: the VIDEO alternatives must
		// have no container filter (the remux converts the container), while
		// preferring m4a audio keeps the mp4 merge clean.
		if strings.Contains(f, "[ext=mp4]") {
			t.Errorf("%s selector must not filter video by mp4 container: %s", q, f)
		}
	}
}

func TestParseMemInfoValue(t *testing.T) {
	data := "MemTotal:        4026408 kB\nMemFree:          242228 kB\nMemAvailable:    2076200 kB\nShmem:            116620 kB\nSwapTotal:       2012156 kB\n"
	if kb, ok := parseMemInfoValue(data, "MemAvailable"); !ok || kb != 2076200 {
		t.Fatalf("MemAvailable = %d (found %v), want 2076200", kb, ok)
	}
	if kb, ok := parseMemInfoValue(data, "MemFree"); !ok || kb != 242228 {
		t.Fatalf("MemFree = %d (found %v), want 242228", kb, ok)
	}
	if _, ok := parseMemInfoValue(data, "SwapFree"); ok {
		t.Fatal("a missing field must not be found")
	}
}
