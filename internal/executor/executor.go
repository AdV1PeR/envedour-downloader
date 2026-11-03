package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"envedour-bot/internal/config"
	"envedour-bot/internal/queue"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

// ThermalMonitor interface for thermal monitoring (only on ARM64)
type ThermalMonitor interface {
	IsThrottled() bool
	Start()
	Stop()
}

var thermalMonitorImpl ThermalMonitor

type Executor struct {
	config       *config.Config
	armOptimized bool
	botAPI       *tgbotapi.BotAPI
	thermalMon   ThermalMonitor

	notifyMu         sync.Mutex
	lastCookieNotify map[string]time.Time
}

func NewExecutor(cfg *config.Config, armOptimized bool) *Executor {
	// Initialize bot API for sending files. The endpoint must be supplied to
	// the constructor: NewBotAPI performs getMe immediately, so setting the
	// endpoint afterwards would still contact api.telegram.org first.
	var botAPI *tgbotapi.BotAPI
	var err error
	if cfg.LocalAPIURL != "" {
		endpoint := normalizeAPIEndpoint(cfg.LocalAPIURL)
		botAPI, err = tgbotapi.NewBotAPIWithAPIEndpoint(cfg.BotToken, endpoint)
		if err != nil {
			log.Printf("Warning: failed to connect to local Bot API at %s: %v; falling back to the default Telegram API", endpoint, err)
			botAPI, err = tgbotapi.NewBotAPI(cfg.BotToken)
		}
	} else {
		botAPI, err = tgbotapi.NewBotAPI(cfg.BotToken)
	}
	if err != nil {
		log.Printf("Warning: failed to initialize bot API: %v", err)
	}

	exec := &Executor{
		config:           cfg,
		armOptimized:     armOptimized,
		botAPI:           botAPI,
		lastCookieNotify: make(map[string]time.Time),
	}

	// Initialize thermal monitor on ARM64 if requested
	if armOptimized && runtime.GOARCH == "arm64" && thermalMonitorImpl != nil {
		exec.thermalMon = thermalMonitorImpl
		go func() {
			if m, ok := exec.thermalMon.(interface{ Start() }); ok {
				m.Start()
			}
		}()
	}

	// Validate environment (errors ignored)
	ValidateEnvironment(cfg.TmpfsPath)

	return exec
}

// jobTimeout bounds a single download+upload job. Without it a wedged
// yt-dlp (network stall, interactive prompt) blocks a worker forever and,
// with all workers wedged, silently idles the whole bot.
const jobTimeout = 20 * time.Minute

func (e *Executor) Worker(ctx context.Context, q queue.Queue) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			job, err := q.Dequeue(ctx)
			if err != nil {
				time.Sleep(1 * time.Second)
				continue
			}

			if job == nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			jobCtx, cancel := context.WithTimeout(ctx, jobTimeout)
			e.processJob(jobCtx, job)
			cancel()
		}
	}
}

func (e *Executor) processJob(ctx context.Context, job *queue.Job) {
	// Check thermal throttling if ARM optimized
	if e.armOptimized && e.thermalMon != nil {
		if e.thermalMon.IsThrottled() {
			e.sendMessage(job.ChatID, "Система перегружена (высокая температура). Попробуйте позже.")
			time.Sleep(5 * time.Second)
			return
		}
	}

	// Check available memory
	if err := e.checkMemory(); err != nil {
		e.sendMessage(job.ChatID, "Недостаточно памяти на устройстве. Попробуйте позже.")
		return
	}

	// Set default values if not set
	quality := job.Quality
	if quality == "" {
		quality = "best"
	}
	mediaType := job.MediaType
	if mediaType == "" {
		mediaType = "video"
	}

	// Download media (video or audio)
	media, err := e.downloadMedia(ctx, job.URL, job.ID, quality, mediaType)
	if err != nil {
		log.Printf("Download error: %v", err)
		e.notifyAdminCookiesStale(job.URL, err)
		userMsg := "❌ Ошибка при скачивании.\n\nВозможные причины:\n• Неверная ссылка\n• Видео недоступно\n• Проблемы с сетью\n• Недостаточно памяти\n\nПопробуйте другую ссылку или повторите позже."
		e.sendMessage(job.ChatID, userMsg)
		return
	}
	defer func() {
		os.Remove(media.path)
		if media.thumb != "" {
			os.Remove(media.thumb)
		}
	}()

	// Send media based on type
	if mediaType == "audio" {
		if err := e.sendAudio(job.ChatID, media.path, media.thumb, media.title, probeDuration(media.path)); err != nil {
			log.Printf("Audio send error: %v", err)
			userMsg := "❌ Ошибка при отправке аудио.\n\nВозможно, файл слишком большой или поврежден.\nПопробуйте другую ссылку."
			e.sendMessage(job.ChatID, userMsg)
			return
		}
	} else {
		if err := e.sendVideo(job.ChatID, media.path); err != nil {
			log.Printf("Video send error: %v", err)
			userMsg := "❌ Ошибка при отправке видео.\n\n"
			if err.Error() == "bot API not initialized" {
				userMsg += "Проблема с конфигурацией бота. Обратитесь к администратору."
			} else if filepath.Ext(media.path) != "" {
				userMsg += "Возможно, файл слишком большой или поврежден.\nПопробуйте другую ссылку."
			} else {
				userMsg += "Попробуйте повторить запрос позже."
			}
			e.sendMessage(job.ChatID, userMsg)
			return
		}
	}
}

// cookieNotifyInterval throttles admin reminders: a burst of failing links
// must not spam the admin chat, one reminder per platform is enough.
const cookieNotifyInterval = 6 * time.Hour

// cookieErrorPlatform reports which site's cookies the yt-dlp error blames,
// or "" when the failure is unrelated to authentication. yt-dlp phrases this
// as "... rate-limit reached or login required. Use --cookies ...".
func cookieErrorPlatform(rawURL, errText string) string {
	lowered := strings.ToLower(errText)
	if !strings.Contains(lowered, "login required") &&
		!strings.Contains(lowered, "rate-limit reached") &&
		!strings.Contains(lowered, "use --cookies") {
		return ""
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	switch {
	case host == "instagram.com" || strings.HasSuffix(host, ".instagram.com") || host == "instagr.am":
		return "Instagram"
	case host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com"):
		return "TikTok"
	case host == "youtu.be" || host == "youtube.com" || strings.HasSuffix(host, ".youtube.com"):
		return "YouTube"
	default:
		return ""
	}
}

// notifyAdminCookiesStale reminds the admin that a site's cookies need
// refreshing. Reminders repeat no more than once per cookieNotifyInterval
// per platform; disabled entirely when ADMIN_CHAT_ID is not set.
func (e *Executor) notifyAdminCookiesStale(rawURL string, err error) {
	if e.config.AdminChatID == 0 || err == nil {
		return
	}
	platform := cookieErrorPlatform(rawURL, err.Error())
	if platform == "" {
		return
	}

	e.notifyMu.Lock()
	if time.Since(e.lastCookieNotify[platform]) < cookieNotifyInterval {
		e.notifyMu.Unlock()
		return
	}
	e.lastCookieNotify[platform] = time.Now()
	e.notifyMu.Unlock()

	text := fmt.Sprintf(
		"⚠️ Куки для %s устарели или не настроены — видео не скачалось.\n\n"+
			"Обнови файл с куками на opiadv (например /opt/envedour-bot/cookies/instagram.txt)\n"+
			"и переменную %s_COOKIES в /opt/envedour-bot/.env, затем перезапусти бота:\n"+
			"systemctl restart envedour-down-bot",
		platform, strings.ToUpper(platform))
	e.sendMessage(e.config.AdminChatID, text)
}

// normalizeAPIEndpoint converts a base URL such as
// "http://127.0.0.1:8081" into the endpoint template expected by
// go-telegram-bot-api.
func normalizeAPIEndpoint(raw string) string {
	baseURL := strings.TrimSuffix(strings.TrimSpace(raw), "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	return baseURL + "/bot%s/%s"
}

func isYouTubeURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	return host == "youtu.be" || host == "youtube.com" ||
		strings.HasSuffix(host, ".youtube.com") || host == "youtube-nocookie.com" ||
		strings.HasSuffix(host, ".youtube-nocookie.com")
}

func isTikTokURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	return host == "tiktok.com" || strings.HasSuffix(host, ".tiktok.com") ||
		host == "vt.tiktok.com"
}

// addTikTokArgs adds the headers and connection tweaks that TikTok needs to
// serve media to yt-dlp. Client hints are only sent for the actual download;
// keeping them out of metadata requests makes those requests less suspicious.
func addTikTokArgs(args []string, includeClientHints bool) []string {
	args = append(args,
		"--no-check-certificate",
		"-4", // Force IPv4
		"--legacy-server-connect",
		"--user-agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
		"--referer", "https://www.tiktok.com/",
		"--add-header", "Accept:text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8",
		"--add-header", "Accept-Language:en-US,en;q=0.9",
		"--add-header", "Accept-Encoding:gzip, deflate, br",
		"--add-header", "DNT:1",
		"--add-header", "Connection:keep-alive",
		"--add-header", "Upgrade-Insecure-Requests:1",
		"--add-header", "Sec-Fetch-Dest:document",
		"--add-header", "Sec-Fetch-Mode:navigate",
		"--add-header", "Sec-Fetch-Site:none",
		"--add-header", "Sec-Fetch-User:?1",
	)
	if includeClientHints {
		args = append(args,
			"--add-header", "sec-ch-ua:\"Google Chrome\";v=\"131\", \"Chromium\";v=\"131\", \"Not_A Brand\";v=\"24\"",
			"--add-header", "sec-ch-ua-mobile:?0",
			"--add-header", "sec-ch-ua-platform:\"Windows\"",
		)
	}
	return args
}

// truncateFilename limits a filename to maxBytes without splitting a UTF-8
// rune. This matters on filesystems with a 255-byte name limit: 180 Cyrillic
// characters are already 360 bytes even though they fit in 180 Go runes.
func truncateFilename(name string, maxBytes int) string {
	if len(name) <= maxBytes {
		return name
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return strings.TrimSpace(name[:cut])
}

// ytdlpJSRuntime describes a JavaScript runtime supported by yt-dlp.
// The order of this list mirrors yt-dlp's own priority order.
type ytdlpJSRuntime struct {
	name       string
	binary     string
	minVersion []int
}

var ytdlpJSRuntimeCandidates = []ytdlpJSRuntime{
	{name: "deno", binary: "deno", minVersion: []int{2, 3, 0}},
	{name: "node", binary: "node", minVersion: []int{22, 0, 0}},
	{name: "quickjs", binary: "qjs", minVersion: []int{2023, 12, 9}},
	{name: "bun", binary: "bun", minVersion: []int{1, 2, 11}},
}

var versionNumberRe = regexp.MustCompile(`\d+`)

// versionAtLeast compares two numeric version tuples, padding the shorter one
// with zeroes (2.9 == 2.9.0).
func versionAtLeast(have, want []int) bool {
	length := len(have)
	if len(want) > length {
		length = len(want)
	}
	for i := 0; i < length; i++ {
		var h, w int
		if i < len(have) {
			h = have[i]
		}
		if i < len(want) {
			w = want[i]
		}
		if h != w {
			return h > w
		}
	}
	return true
}

func parseVersionTuple(output string) []int {
	matches := versionNumberRe.FindAllString(output, -1)
	version := make([]int, 0, 3)
	for _, match := range matches {
		number, err := strconv.Atoi(match)
		if err != nil {
			continue
		}
		version = append(version, number)
		if len(version) == 3 {
			break
		}
	}
	return version
}

// runtimeVersion runs the runtime's version command with a short timeout so a
// broken binary cannot stall a download worker.
func runtimeVersion(path string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput()
	if err != nil {
		return ""
	}
	return string(output)
}

// detectYTDLPJSRuntimes returns runtimes in the format expected by
// yt-dlp's --js-runtimes option ("name:path"). The YTDLP_JS_RUNTIME
// environment variable can be used to override detection, for example:
//
//	YTDLP_JS_RUNTIME=deno:/usr/local/bin/deno
//	YTDLP_JS_RUNTIME=node:/usr/bin/node,deno:/usr/local/bin/deno
func detectYTDLPJSRuntimes() []string {
	if override := strings.TrimSpace(os.Getenv("YTDLP_JS_RUNTIME")); override != "" {
		var runtimes []string
		for _, item := range strings.Split(override, ",") {
			item = strings.TrimSpace(item)
			if item != "" {
				runtimes = append(runtimes, item)
			}
		}
		return runtimes
	}

	var runtimes []string
	for _, candidate := range ytdlpJSRuntimeCandidates {
		path, err := exec.LookPath(candidate.binary)
		if err != nil {
			continue
		}

		var output string
		if candidate.name == "quickjs" {
			output = runtimeVersion(path, "--help")
		} else {
			output = runtimeVersion(path, "--version")
		}
		if output == "" {
			continue
		}

		// QuickJS-ng versions are not comparable with the classic QuickJS
		// calendar versions; yt-dlp accepts any QuickJS-ng build.
		if candidate.name == "quickjs" && strings.Contains(output, "QuickJS-ng") {
			runtimes = append(runtimes, candidate.name+":"+path)
			continue
		}

		version := parseVersionTuple(output)
		if len(version) == 0 || !versionAtLeast(version, candidate.minVersion) {
			continue
		}
		runtimes = append(runtimes, candidate.name+":"+path)
	}
	return runtimes
}

// ytdlpSupportsJSRuntimes reports whether the installed yt-dlp understands the
// --js-runtimes flag. Older releases do not have it and would fail if given an
// unknown option.
func ytdlpSupportsJSRuntimes() bool {
	output, err := exec.Command("yt-dlp", "--help").CombinedOutput()
	if err != nil {
		return false
	}
	return strings.Contains(string(output), "--js-runtimes")
}

// jsRuntimeCache holds the probe result for the process lifetime: probing
// spawns a Python interpreter plus up to four version subprocesses, which on
// a Cortex-A53 costs up to a second of pure CPU per download.
var (
	jsRuntimeOnce     sync.Once
	cachedJSArgs      []string
)

func addYTDLPJavaScriptRuntime(args []string) []string {
	jsRuntimeOnce.Do(func() {
		cachedJSArgs = probeYTDLPJSRuntimeArgs()
	})
	return append(args, cachedJSArgs...)
}

func probeYTDLPJSRuntimeArgs() []string {
	if !ytdlpSupportsJSRuntimes() {
		return nil
	}
	var out []string
	for _, runtime := range detectYTDLPJSRuntimes() {
		out = append(out, "--js-runtimes", runtime)
	}
	return out
}

// resetJSRuntimeCacheForTest clears the cached probe result so tests can
// exercise detection with different fake PATH environments.
func resetJSRuntimeCacheForTest() {
	jsRuntimeOnce = sync.Once{}
	cachedJSArgs = nil
}

// mediaResult carries everything processJob needs to deliver a download.
type mediaResult struct {
	path  string // file to send (inside tmpfs)
	thumb string // cover art JPEG for audio, "" if none
	title string // cleaned-up source title for audio, "" for video
}

// downloadMedia downloads audio or video and returns the file, the cover art
// (audio only) and the cleaned-up title (audio only).
func (e *Executor) downloadMedia(ctx context.Context, url, jobID, quality, mediaType string) (mediaResult, error) {
	if err := os.MkdirAll(e.config.TmpfsPath, 0755); err != nil {
		return mediaResult{}, fmt.Errorf("create temp directory: %w", err)
	}

	var title string
	var tempCookiesFiles []string

	// Helper function to create a temporary cookies file.
	createTempCookies := func(originalFile string) string {
		tempCookiesFile := filepath.Join(e.config.TmpfsPath, fmt.Sprintf("cookies_%s_%d.txt", jobID, time.Now().UnixNano()))
		if data, err := os.ReadFile(originalFile); err == nil {
			// Cookies are live session credentials: keep them private even
			// inside the shared tmpfs directory.
			if err := os.WriteFile(tempCookiesFile, data, 0600); err == nil {
				tempCookiesFiles = append(tempCookiesFiles, tempCookiesFile)
				return tempCookiesFile
			}
		}
		return originalFile // Fallback to original
	}

	defer func() {
		for _, tempFile := range tempCookiesFiles {
			os.Remove(tempFile)
		}
	}()

	// Extract the title and the cover art URL before downloading audio so the
	// resulting file has a readable name and the Telegram player a thumbnail.
	// The title is sanitized and byte-limited below.
	var thumbURL string
	if mediaType == "audio" {
		titleArgs := []string{"--no-cache-dir", "--no-playlist", "--skip-download",
			"--print", "%(title)s", "--print", "%(thumbnail)s"}
		if cookiesFile := e.getCookiesFile(url); cookiesFile != "" {
			titleArgs = append(titleArgs, "--cookies", createTempCookies(cookiesFile))
		}
		if isTikTokURL(url) {
			titleArgs = addTikTokArgs(titleArgs, false)
		}
		titleArgs = addYTDLPJavaScriptRuntime(titleArgs)
		titleArgs = append(titleArgs, url)

		titleCmd := exec.CommandContext(ctx, "yt-dlp", titleArgs...)
		if titleBytes, err := titleCmd.Output(); err == nil && len(titleBytes) > 0 {
			// yt-dlp prints one line per --print field; missing fields come
			// out as "NA".
			lines := strings.Split(strings.TrimSpace(string(titleBytes)), "\n")
			title = cleanAudioTitle(lines[0])
			if len(lines) > 1 {
				thumbURL = strings.TrimSpace(lines[1])
			}
		}
		if thumbURL == "NA" {
			thumbURL = ""
		}
		if title == "" {
			title = fmt.Sprintf("audio_%d", time.Now().Unix())
		}
	}

	// Always download to a job-specific name. Using the title directly here
	// allowed two simultaneous downloads with the same title to overwrite
	// each other and made "most recent file" searches pick another job's file.
	outputPath := filepath.Join(e.config.TmpfsPath, fmt.Sprintf("%s.%%(ext)s", jobID))

	args := []string{
		"--no-cache-dir",
		"--no-playlist",
		// Keep yt-dlp's \r progress stream out of the captured output:
		// CombinedOutput would otherwise buffer megabytes of it in RAM.
		"--no-progress",
		"--quiet",
		"-o", outputPath,
		// Disable automatic cookie extraction from browsers (server doesn't have browsers)
		"--no-cookies-from-browser",
	}

	if !isYouTubeURL(url) {
		// aria2c is optional. Passing it when it is not installed makes yt-dlp
		// fail before it even tries to download.
		if _, err := exec.LookPath("aria2c"); err == nil {
			args = append(args,
				"--external-downloader", "aria2c",
				"--external-downloader-args", "aria2c:-j16 -x16 -s16 --file-allocation=falloc --stream-piece-selector=geom --max-download-limit=0",
			)
		}
	}

	if isTikTokURL(url) {
		args = addTikTokArgs(args, true)
	}

	if isYouTubeURL(url) {
		// YouTube frequently blocks requests from default web client without
		// cookies. Using the Android player client is often more reliable and
		// works without cookies for many videos. yt-dlp also needs a JavaScript
		// runtime (Deno / Node 22+) to decrypt the player, which is added below
		// via addYTDLPJavaScriptRuntime.
		args = append(args, "--extractor-args", "youtube:player_client=android")
	}

	// Set format based on media type and quality.
	args = append(args, e.mediaTypeArgs(mediaType, quality)...)

	// Use a temporary copy of the cookies file so yt-dlp can update it without
	// touching the configured (possibly read-only) file.
	if cookiesFile := e.getCookiesFile(url); cookiesFile != "" {
		args = append(args, "--cookies", createTempCookies(cookiesFile))
	} else if isTikTokURL(url) {
		log.Printf("Warning: TikTok URL detected but no cookies file configured. TikTok may require cookies to bypass 403 errors.")
	}

	args = addYTDLPJavaScriptRuntime(args)
	args = append(args, url)

	ytdlpCmd := exec.CommandContext(ctx, "yt-dlp", args...)
	ytdlpCmd.Env = os.Environ()
	if e.armOptimized && runtime.GOARCH == "arm64" {
		// Set ARM-specific environment
		ytdlpCmd.Env = append(ytdlpCmd.Env, "FFMPEG_BINARY=ffmpeg")
	}

	// Capture stderr only, bounded: yt-dlp errors are short, but a stray
	// verbose flag should not turn into an unbounded in-memory buffer.
	var stderrBuf bytes.Buffer
	ytdlpCmd.Stdout = io.Discard
	ytdlpCmd.Stderr = &stderrBuf
	err := ytdlpCmd.Run()
	for _, tempFile := range tempCookiesFiles {
		os.Remove(tempFile)
	}
	if err != nil {
		output := stderrBuf.String()
		if len(output) > 4096 {
			output = output[len(output)-4096:]
		}
		return mediaResult{}, fmt.Errorf("yt-dlp failed: %w\nOutput: %s", err, output)
	}

	// Find the file belonging to this job. Ignore partial and metadata files
	// that yt-dlp may leave behind.
	matches, _ := filepath.Glob(filepath.Join(e.config.TmpfsPath, jobID+"*"))
	var selected string
	var selectedSize int64
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil || info.IsDir() || info.Size() == 0 {
			continue
		}
		switch strings.ToLower(filepath.Ext(match)) {
		case ".part", ".ytdl", ".tmp", ".temp":
			continue
		}
		if info.Size() > selectedSize {
			selected = match
			selectedSize = info.Size()
		}
	}
	if selected == "" {
		return mediaResult{}, fmt.Errorf("downloaded file not found")
	}

	// --remux-video handles most containers at the yt-dlp level; catch
	// whatever still lands with a pseudo-extension so Telegram does not
	// show "*.unknown_video" documents.
	if needsRemuxToMp4(filepath.Ext(selected)) {
		if remuxed, err := e.remuxToMp4(ctx, selected, jobID); err == nil {
			selected = remuxed
		} else {
			log.Printf("Warning: could not remux %s to mp4: %v", filepath.Base(selected), err)
		}
	}

	// Give audio files a human-readable name. The short job id keeps two
	// concurrent downloads with the same title from overwriting each other.
	if mediaType == "audio" && title != "" {
		ext := filepath.Ext(selected)
		suffix := jobID
		if len(suffix) > 8 {
			suffix = suffix[:8]
		}
		renamed := filepath.Join(e.config.TmpfsPath, fmt.Sprintf("%s [%s]%s", title, suffix, ext))
		if err := os.Rename(selected, renamed); err == nil {
			selected = renamed
		}
	}

	// Fetch the cover art so the Telegram player shows it instead of a
	// generic icon. A missing cover must not fail the job.
	var cover string
	if mediaType == "audio" && thumbURL != "" {
		cover = e.fetchAudioThumbnail(ctx, thumbURL)
	}

	return mediaResult{path: selected, thumb: cover, title: title}, nil
}

// cleanAudioTitle makes a source title suitable for display: uploaders often
// stuff titles with underscores, which become spaces, and filesystem-hostile
// characters are sanitized away.
func cleanAudioTitle(raw string) string {
	return truncateFilename(sanitizeFilename(strings.ReplaceAll(strings.TrimSpace(raw), "_", " ")), 160)
}

// fetchAudioThumbnail downloads a cover art URL and turns it into a JPEG
// fitting Telegram's 320x320 preview limit, reusing generateThumbnail.
func (e *Executor) fetchAudioThumbnail(ctx context.Context, thumbURL string) string {
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, thumbURL, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Warning: cover download failed (%s): %v", thumbURL, err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf("Warning: cover download failed (%s): HTTP %d", thumbURL, resp.StatusCode)
		return ""
	}

	raw := filepath.Join(e.config.TmpfsPath, fmt.Sprintf("cover_%d.img", time.Now().UnixNano()))
	file, err := os.Create(raw)
	if err != nil {
		return ""
	}
	_, copyErr := io.Copy(file, resp.Body)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(raw)
		return ""
	}
	defer os.Remove(raw)

	return e.generateThumbnail(raw, 0)
}

// needsRemuxToMp4 reports whether yt-dlp saved the file without a
// determinable container extension (e.g. *.unknown_video).
func needsRemuxToMp4(ext string) bool {
	switch strings.ToLower(ext) {
	case ".unknown_video", ".unknown_audio":
		return true
	default:
		return false
	}
}

// remuxToMp4 stream-copies the file into an MP4 container (no re-encode).
// On failure the caller keeps the original file: delivering it with the odd
// extension is still better than failing the job.
func (e *Executor) remuxToMp4(ctx context.Context, src, jobID string) (string, error) {
	dst := filepath.Join(e.config.TmpfsPath, jobID+".mp4")

	// Capture ffmpeg's stderr bounded: it is chatty even on success.
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-i", src, "-c", "copy", "-movflags", "+faststart", dst)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		os.Remove(dst)
		detail := stderr.String()
		if len(detail) > 2048 {
			detail = detail[len(detail)-2048:]
		}
		return "", fmt.Errorf("ffmpeg remux failed: %w\nOutput: %s", err, detail)
	}
	if err := os.Remove(src); err != nil {
		log.Printf("Warning: failed to remove original %s after remux: %v", src, err)
	}
	return dst, nil
}

// getCookiesFile returns the appropriate cookies file path for the given URL
// Returns empty string if no cookies file is configured for the platform
func (e *Executor) getCookiesFile(url string) string {
	// Check for platform-specific cookies files first
	if strings.Contains(url, "tiktok.com") || strings.Contains(url, "vt.tiktok.com") {
		if e.config.TikTokCookies != "" {
			if _, err := os.Stat(e.config.TikTokCookies); err == nil {
				return e.config.TikTokCookies
			}
		}
	} else if strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am") {
		if e.config.InstagramCookies != "" {
			if _, err := os.Stat(e.config.InstagramCookies); err == nil {
				return e.config.InstagramCookies
			}
		}
	} else if strings.Contains(url, "youtube.com") || strings.Contains(url, "youtu.be") {
		if e.config.YouTubeCookies != "" {
			if _, err := os.Stat(e.config.YouTubeCookies); err == nil {
				return e.config.YouTubeCookies
			}
		}
	}

	// Fallback to deprecated COOKIES_FILE for backward compatibility
	if e.config.CookiesFile != "" {
		if _, err := os.Stat(e.config.CookiesFile); err == nil {
			return e.config.CookiesFile
		}
	}

	return ""
}

// sanitizeFilename removes characters that are invalid in filenames by
// replacing them with spaces, so titles stay readable: separators like "|"
// do not turn into underscores. Doubled spaces collapse afterwards.
func sanitizeFilename(name string) string {
	// Replace invalid filesystem characters.
	invalidChars := []string{"/", "\\", ":", "*", "?", "\"", "<", ">", "|"}
	result := name
	for _, char := range invalidChars {
		result = strings.ReplaceAll(result, char, " ")
	}
	// Replace control characters (including newlines) with spaces. A title
	// containing a newline would otherwise create a filename with a newline.
	result = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, result)
	// Collapse whitespace and remove leading/trailing spaces and dots.
	result = strings.Join(strings.Fields(result), " ")
	result = strings.Trim(result, " .")
	return result
}

// mediaTypeArgs returns the yt-dlp arguments selecting the requested content.
func (e *Executor) mediaTypeArgs(mediaType, quality string) []string {
	if mediaType == "audio" {
		return []string{"-x", "--audio-format", "mp3", "--audio-quality", "0"}
	}
	// Extractors that do not report format extensions never match the
	// [ext=mp4] filters in the quality selector and fall through to /best,
	// which yt-dlp saves as *.unknown_video. Force a real MP4 container:
	// merge for split video+audio jobs, remux (stream copy) for single
	// formats. Both are no-ops for sources already in MP4.
	return []string{
		"--format", e.getFormatForQuality(quality),
		"--merge-output-format", "mp4",
		"--remux-video", "mp4",
	}
}

func (e *Executor) getFormatForQuality(quality string) string {
	switch quality {
	case "1080p":
		return "bestvideo[height<=1080][ext=mp4]+bestaudio[ext=m4a]/best[height<=1080][ext=mp4]/best"
	case "720p":
		return "bestvideo[height<=720][ext=mp4]+bestaudio[ext=m4a]/best[height<=720][ext=mp4]/best"
	case "480p":
		return "bestvideo[height<=480][ext=mp4]+bestaudio[ext=m4a]/best[height<=480][ext=mp4]/best"
	case "360p":
		return "bestvideo[height<=360][ext=mp4]+bestaudio[ext=m4a]/best[height<=360][ext=mp4]/best"
	case "audio":
		return "bestaudio[ext=m4a]/bestaudio/best"
	default: // "best"
		return "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]/best"
	}
}

func (e *Executor) sendAudio(chatID int64, audioPath, thumbPath, title string, duration int) error {
	if e.botAPI == nil {
		return fmt.Errorf("bot API not initialized")
	}

	file, err := os.Open(audioPath)
	if err != nil {
		return fmt.Errorf("failed to open audio file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat audio file: %w", err)
	}

	// Check file size
	if stat.Size() > e.config.MaxFileSize {
		return fmt.Errorf("file too large: %d bytes (max: %d)", stat.Size(), e.config.MaxFileSize)
	}

	audio := tgbotapi.NewAudio(chatID, tgbotapi.FilePath(audioPath))
	// The local Bot API does not read cover art embedded in the file: the
	// preview must be sent explicitly as a JPEG no larger than 320x320.
	if thumbPath != "" {
		audio.Thumb = tgbotapi.FilePath(thumbPath)
	}
	// Show a proper track name and playing time in the player instead of the
	// raw filename.
	if title != "" {
		audio.Title = title
	}
	audio.Duration = duration

	_, err = e.botAPI.Send(audio)
	if err != nil {
		return fmt.Errorf("failed to send audio: %w", err)
	}
	return nil
}

func (e *Executor) sendVideo(chatID int64, videoPath string) error {
	if e.botAPI == nil {
		return fmt.Errorf("bot API not initialized")
	}

	file, err := os.Open(videoPath)
	if err != nil {
		return fmt.Errorf("failed to open video file: %w", err)
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat video file: %w", err)
	}

	// Check file size
	if stat.Size() > e.config.MaxFileSize {
		return fmt.Errorf("file too large: %d bytes (max: %d)", stat.Size(), e.config.MaxFileSize)
	}

	// Send as a playable video so Telegram shows an inline player. The local
	// Bot API server does not analyze uploads: duration, dimensions and the
	// preview frame must be supplied, or the message renders as a square
	// player showing 00:00 with no thumbnail.
	meta := e.probeVideo(videoPath)
	video := tgbotapi.NewVideo(chatID, tgbotapi.FilePath(videoPath))
	video.Duration = meta.duration
	video.Width = meta.width
	video.Height = meta.height
	if meta.thumb != "" {
		video.Thumb = tgbotapi.FilePath(meta.thumb)
		defer os.Remove(meta.thumb)
	}
	video.SupportsStreaming = true
	if _, err := e.botAPI.Send(video); err == nil {
		return nil
	} else {
		log.Printf("Sending as video failed (%v), falling back to a document", err)
	}

	document := tgbotapi.NewDocument(chatID, tgbotapi.FilePath(videoPath))
	if _, err := e.botAPI.Send(document); err != nil {
		return fmt.Errorf("failed to send video: %w", err)
	}
	return nil
}

// videoMeta holds the facts the local Bot API server needs to render a normal
// video message: it does not analyze uploads, so duration, dimensions and the
// preview frame must be supplied by the bot.
type videoMeta struct {
	duration int
	width    int
	height   int
	thumb    string
}

// probeVideo extracts metadata with ffprobe and captures a preview frame with
// ffmpeg. Missing data is not fatal: the fields stay zero and the message
// falls back to Telegram's generic player.
func (e *Executor) probeVideo(path string) videoMeta {
	var meta videoMeta

	out, err := exec.Command("ffprobe", "-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height",
		"-of", "json", path).Output()
	if err != nil {
		log.Printf("Warning: ffprobe failed for %s: %v", filepath.Base(path), err)
	} else {
		var probe struct {
			Streams []struct {
				Width  int `json:"width"`
				Height int `json:"height"`
			} `json:"streams"`
		}
		if err := json.Unmarshal(out, &probe); err == nil && len(probe.Streams) > 0 {
			meta.width = probe.Streams[0].Width
			meta.height = probe.Streams[0].Height
		}
	}

	meta.duration = probeDuration(path)
	meta.thumb = e.generateThumbnail(path, meta.duration)
	return meta
}

// probeDuration returns the duration of a media file in whole seconds, rounded
// (0 when it cannot be determined).
func probeDuration(path string) int {
	out, err := exec.Command("ffprobe", "-v", "error",
		"-show_entries", "format=duration", "-of", "json", path).Output()
	if err != nil {
		return 0
	}
	var probe struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &probe); err != nil {
		return 0
	}
	seconds, err := strconv.ParseFloat(probe.Format.Duration, 64)
	if err != nil || seconds <= 0 {
		return 0
	}
	return int(math.Round(seconds))
}

// generateThumbnail saves an early frame as JPEG scaled to fit Telegram's
// 320x320 preview limit. Returns "" when the frame cannot be extracted.
func (e *Executor) generateThumbnail(path string, duration int) string {
	thumb := filepath.Join(e.config.TmpfsPath, fmt.Sprintf("thumb_%d.jpg", time.Now().UnixNano()))
	args := []string{"-y", "-v", "error"}
	if duration > 0 {
		// Grab a frame a little way in: frame 0 is often a black lead-in.
		args = append(args, "-ss", fmt.Sprintf("%.2f", math.Min(float64(duration)*0.1, 5)))
	}
	args = append(args,
		"-i", path,
		"-frames:v", "1",
		"-vf", "scale=320:320:force_original_aspect_ratio=decrease",
		thumb,
	)
	if err := exec.Command("ffmpeg", args...).Run(); err != nil {
		os.Remove(thumb)
		return ""
	}
	if info, err := os.Stat(thumb); err != nil || info.Size() == 0 {
		os.Remove(thumb)
		return ""
	}
	return thumb
}

func (e *Executor) sendMessage(chatID int64, text string) {
	if e.botAPI == nil {
		return
	}
	msg := tgbotapi.NewMessage(chatID, text)
	e.botAPI.Send(msg)
}

func (e *Executor) checkMemory() error {
	var info syscall.Sysinfo_t
	if err := syscall.Sysinfo(&info); err != nil {
		return err
	}

	// Use configurable minimum free memory (default 256MB)
	minFreeMemory := uint64(e.config.MinFreeMemMB * 1024 * 1024)
	freeMB := info.Freeram / (1024 * 1024)

	if info.Freeram < minFreeMemory {
		log.Printf("Memory check failed: %d MB free (minimum: %d MB)", freeMB, e.config.MinFreeMemMB)
		return fmt.Errorf("insufficient memory: %d MB free (minimum: %d MB)", freeMB, e.config.MinFreeMemMB)
	}

	return nil
}

func (e *Executor) Close() {
	if e.thermalMon != nil {
		if m, ok := e.thermalMon.(interface{ Stop() }); ok {
			m.Stop()
		}
	}
}
