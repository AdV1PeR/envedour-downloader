package bot

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"envedour-bot/internal/config"
	"envedour-bot/internal/executor"
	"envedour-bot/internal/groups"
	"envedour-bot/internal/queue"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

type Bot struct {
	api         *tgbotapi.BotAPI
	config      *config.Config
	queue       queue.Queue
	executor    *executor.Executor
	workerPool  chan struct{}
	preferences *PreferencesStore
	limiter     *rateLimiter
	dedup       *urlDedup
	groups      *groups.Store
	admin       *adminState
}

func NewBot(cfg *config.Config, q queue.Queue, exec *executor.Executor) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return nil, err
	}

	// Use local API if configured
	// SetAPIEndpoint expects format with placeholders: "http://localhost:8081/bot%s/%s"
	// We need to escape % to %% so fmt.Sprintf in library works correctly
	if cfg.LocalAPIURL != "" {
		// Remove trailing slash if present
		baseURL := strings.TrimSuffix(cfg.LocalAPIURL, "/")
		// Add http:// if protocol is missing
		if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
			baseURL = "http://" + baseURL
		}
		// Escape % signs for proper formatting: %s becomes %%s
		endpoint := fmt.Sprintf("%s/bot%%s/%%s", baseURL)
		api.SetAPIEndpoint(endpoint)
	}

	// Initialize preferences store
	var prefsStore *PreferencesStore
	if redisClient := q.GetClient(); redisClient != nil {
		prefsStore = NewPreferencesStore(redisClient)
	}

	// Runtime group whitelist. A broken file must not stop the bot: the
	// static ALLOWED_GROUP_IDS list still works, and the admin can re-add
	// groups with /admin.
	groupStore := groups.New(cfg.AllowedGroupsFile)
	if err := groupStore.Load(); err != nil {
		log.Printf("WARNING: cannot load dynamic group whitelist %s: %v; continuing with ALLOWED_GROUP_IDS only", cfg.AllowedGroupsFile, err)
	}

	bot := &Bot{
		api:         api,
		config:      cfg,
		queue:       q,
		executor:    exec,
		preferences: prefsStore,
		workerPool:  make(chan struct{}, cfg.WorkerCount+2),
		limiter:     newRateLimiter(),
		dedup:       newURLDedup(5 * time.Minute),
		groups:      groupStore,
		admin:       newAdminState(),
	}

	return bot, nil
}

func (b *Bot) Start(ctx context.Context) {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 30 // 30s timeout for poor connections

	updates := b.api.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return
		case update := <-updates:
			b.workerPool <- struct{}{} // Acquire worker
			go b.handleUpdate(update)
		}
	}
}

// Stop stops the library's update-polling goroutines during shutdown.
func (b *Bot) Stop() {
	b.api.StopReceivingUpdates()
}

func (b *Bot) handleUpdate(update tgbotapi.Update) {
	defer func() { <-b.workerPool }() // Release worker
	defer func() {
		// A panic in an update handler must not kill the whole process:
		// log it and move on to the next update.
		if r := recover(); r != nil {
			log.Printf("PANIC while handling an update: %v\n%s", r, debug.Stack())
		}
	}()

	// Handle callback queries (button presses)
	if update.CallbackQuery != nil {
		b.handleCallbackQuery(update.CallbackQuery)
		return
	}

	if update.Message == nil {
		return
	}

	msg := update.Message
	chatID := msg.Chat.ID

	var senderID int64
	if msg.From != nil {
		senderID = msg.From.ID
	}
	isAdmin := b.isAdmin(senderID)

	// Group chats are opt-in via ALLOWED_GROUP_IDS (plus the runtime list
	// managed by /admin). The one exception is /admin itself: an admin must
	// be able to enable a group from inside that group.
	if !msg.Chat.IsPrivate() && !b.isGroupAllowed(chatID) {
		if isAdmin && msg.IsCommand() && msg.Command() == "admin" {
			b.handleAdminCommand(msg)
			return
		}
		log.Printf("Ignoring message from non-whitelisted group chat %d (%s); add it to ALLOWED_GROUP_IDS or use /admin in the group", chatID, msg.Chat.Title)
		return
	}

	// Private chats can be limited to known users (ALLOWED_USER_IDS) — the
	// bot runs downloads on the owner's device and with the owner's cookies.
	// Admins are always allowed to reach /admin.
	if msg.Chat.IsPrivate() && !b.isUserAllowed(senderID) && !isAdmin {
		log.Printf("Ignoring private message from non-whitelisted user %d; add them to ALLOWED_USER_IDS to enable", senderID)
		return
	}

	// A click on "Добавить группу" arms a one-shot prompt: the next text
	// from this admin is treated as a group ID or @username.
	if msg.Chat.IsPrivate() && isAdmin && !msg.IsCommand() && b.admin.consumeAddPending(senderID) {
		if isValidURL(strings.TrimSpace(msg.Text)) {
			b.sendMessage(chatID, "❌ Это ссылка, а не группа. Нажми «Добавить» и пришли ID группы или @username.")
			return
		}
		b.handleAdminAddInput(msg, senderID)
		return
	}

	// Check if user is donor. In groups the chat is the group itself, so
	// also credit the sender: the admin must not be throttled in their own
	// groups.
	isDonor := b.isDonor(chatID) || (msg.From != nil && b.isDonor(msg.From.ID))

	switch {
	case msg.IsCommand():
		b.handleCommand(msg, isDonor, isAdmin)
	case msg.Text != "":
		b.handleURL(msg, isDonor)
	}
}

func (b *Bot) handleCommand(msg *tgbotapi.Message, isDonor bool, isAdmin bool) {
	chatID := msg.Chat.ID
	command := msg.Command()

	if isAdmin && msg.From != nil {
		b.admin.clearAddPending(msg.From.ID)
	}

	switch command {
	case "start":
		helpText := "Привет! Я бот для скачивания видео.\n\n" +
			"📥 Отправь ссылку на видео для скачивания\n\n" +
			"Используй кнопки ниже для настройки:"
		msg := tgbotapi.NewMessage(chatID, helpText)
		msg.ReplyMarkup = createMainKeyboard()
		b.api.Send(msg)
		return
	case "help":
		helpText := "📋 Справка:\n\n" +
			"💡 Просто отправь ссылку на видео для скачивания!\n\n" +
			"🛑 /stop - остановить свои загрузки\n\n" +
			"Используй кнопки для настройки качества и типа медиа.\n\n" +
			"⚙️ Качество - выбери разрешение видео\n" +
			"🎵 Аудио/Видео - выбери тип скачивания\n" +
			"📊 Статус - посмотри очередь и настройки"
		msg := tgbotapi.NewMessage(chatID, helpText)
		msg.ReplyMarkup = createMainKeyboard()
		b.api.Send(msg)
		return
	case "status":
		b.showStatus(chatID)
	case "admin":
		if !isAdmin {
			b.sendMessage(chatID, "❌ Недостаточно прав.")
			return
		}
		b.handleAdminCommand(msg)
	case "stop", "cancel":
		b.handleStop(msg)
	case "quality", "audio", "video":
		// These commands are now handled via inline buttons
		// Show main menu
		msg := tgbotapi.NewMessage(chatID, "Используй кнопки для настройки:")
		msg.ReplyMarkup = createMainKeyboard()
		b.api.Send(msg)
	default:
		b.sendMessage(chatID, "❌ Неизвестная команда. Используйте /help")
	}
}

// handleStop stops everything the caller started: queued requests are pulled
// out of the queue, running ones are cancelled in the executor. Nobody can
// touch anyone else's jobs.
func (b *Bot) handleStop(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	userID := int64(0)
	if msg.From != nil {
		userID = msg.From.ID
	}

	queued := b.queue.RemoveMatching(func(j *queue.Job) bool {
		return j.FromID == userID
	})
	stopped := b.executor.CancelByUser(userID)

	switch {
	case queued == 0 && len(stopped) == 0:
		b.sendMessage(chatID, "🛑 Твоих активных загрузок нет.")
	case len(stopped) == 0:
		b.sendMessage(chatID, fmt.Sprintf("🛑 Остановлено: убрано из очереди — %d.", queued))
	default:
		text := fmt.Sprintf("🛑 Остановлено: активных загрузок — %d", len(stopped))
		for _, s := range stopped {
			text += "\n  • " + s
		}
		if queued > 0 {
			text += fmt.Sprintf("\n• убрано из очереди: %d", queued)
		}
		b.sendMessage(chatID, text)
	}
}

func (b *Bot) handleURL(msg *tgbotapi.Message, isDonor bool) {
	url := msg.Text
	chatID := msg.Chat.ID
	isGroup := !msg.Chat.IsPrivate()

	// Validate URL - check if it's a valid URL format
	// Also check for URL entities in case Telegram parsed it
	if !isValidURL(url) {
		// Check if message has URL entities
		if len(msg.Entities) > 0 {
			for _, entity := range msg.Entities {
				if entity.Type == "url" {
					url = msg.Text[entity.Offset : entity.Offset+entity.Length]
					break
				}
			}
		}
		// If still not valid, reject
		if !isValidURL(url) {
			if isGroup {
				// Staying quiet in groups: replying to every non-URL
				// message the bot sees would be spam.
				return
			}
			b.sendMessage(chatID, "Пожалуйста, отправьте валидную ссылку на видео.")
			return
		}
	}

	// Anti-spam gates. In groups the key is the sender, so one spammer
	// cannot burn the whole group's quota. Donors (the admin) bypass limits.
	senderID := chatID
	if msg.From != nil {
		senderID = msg.From.ID
	}
	if !isDonor {
		if b.isDuplicate(url, isDonor) {
			b.sendMessage(chatID, "🚫 Эта ссылка уже скачивается или стоит в очереди.")
			return
		}
		if b.dedup.seen(senderID, url) {
			b.sendMessage(chatID, "⏳ Эта ссылка уже скачивалась недавно. Подожди немного.")
			return
		}
		if ok, wait := b.limiter.allow(senderID, b.config.RateLimitPerHour, time.Hour); !ok {
			b.sendMessage(chatID, fmt.Sprintf("⚠️ Слишком много запросов. Следующее скачивание будет доступно через %d мин.", int(wait.Minutes())+1))
			return
		}
	}

	// Check if URL is from Instagram or TikTok - auto-download best quality
	if isInstagramURL(url) || isTikTokURL(url) {
		job := &queue.Job{
			ID:        generateJobID(),
			URL:       url,
			ChatID:    chatID,
			Priority:  queue.PriorityLow,
			Quality:   "best",
			MediaType: "video", // Always video for Instagram/TikTok
			CreatedAt: time.Now(),
		}

		if isDonor {
			job.Priority = queue.PriorityHigh
			job.FromDonor = true
		}
		job.FromID = senderID

		if err := b.enqueueJob(job, isDonor); err != nil {
			b.sendMessage(chatID, err.Error())
		}
		return
	}

	jobID := generateJobID()
	isPlaylist := isYouTubePlaylistURL(url)

	if b.preferences != nil {
		if err := b.preferences.SavePending(jobID, &PendingDownload{URL: url, IsPlaylist: isPlaylist}); err != nil {
			b.sendMessage(chatID, "❌ Ошибка при обработке ссылки. Попробуйте позже.")
			return
		}
	}

	// Show quality selection keyboard
	text := "📥 Выбери качество для скачивания:"
	if isPlaylist {
		text = "📋 Найден плейлист. Выбери качество для всех видео:"
	}
	keyboard := createDownloadQualityKeyboard(jobID)
	message := tgbotapi.NewMessage(chatID, text)
	message.ReplyMarkup = &keyboard
	b.api.Send(message)
}

func (b *Bot) sendMessage(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	b.api.Send(msg)
}

func (b *Bot) isDonor(chatID int64) bool {
	for _, id := range b.config.DonorChatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}

func isValidURL(s string) bool {
	if len(s) < 8 {
		return false
	}
	if len(s) >= 7 && s[:7] == "http://" {
		return true
	}
	if len(s) >= 8 && s[:8] == "https://" {
		return true
	}
	return false
}

func isInstagramURL(url string) bool {
	return strings.Contains(url, "instagram.com") || strings.Contains(url, "instagr.am")
}

func isTikTokURL(url string) bool {
	return strings.Contains(url, "tiktok.com") || strings.Contains(url, "vt.tiktok.com")
}

// idInList reports whether id is present in the whitelist.
func idInList(list []int64, id int64) bool {
	for _, item := range list {
		if item == id {
			return true
		}
	}
	return false
}

// isGroupAllowed reports whether the bot accepts messages from this group
// chat. Private chats are always allowed; groups must be whitelisted via
// ALLOWED_GROUP_IDS or the runtime list managed through /admin.
func (b *Bot) isGroupAllowed(chatID int64) bool {
	return b.isStaticGroup(chatID) || (b.groups != nil && b.groups.Contains(chatID))
}

// isStaticGroup reports whether the group comes from ALLOWED_GROUP_IDS in
// .env. Such groups are displayed but cannot be removed with /admin.
func (b *Bot) isStaticGroup(chatID int64) bool {
	return idInList(b.config.AllowedGroupIDs, chatID)
}

// isAdmin reports whether this Telegram user is allowed to manage groups.
func (b *Bot) isAdmin(userID int64) bool {
	return idInList(b.config.AdminUserIDs, userID)
}

// isUserAllowed reports whether the bot accepts private messages from this
// Telegram user. When ALLOWED_USER_IDS is empty, any user may DM the bot.
func (b *Bot) isUserAllowed(userID int64) bool {
	if len(b.config.AllowedUserIDs) == 0 {
		return true
	}
	return idInList(b.config.AllowedUserIDs, userID)
}

// isYouTubePlaylistURL reports whether the link points at a YouTube playlist
// itself (youtube.com/playlist?list=...). Links to a single video that merely
// carry a list= parameter are treated as single videos.
func isYouTubePlaylistURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host != "youtube.com" && host != "www.youtube.com" && host != "music.youtube.com" {
		return false
	}
	if strings.TrimSuffix(parsed.Path, "/") != "/playlist" {
		return false
	}
	return parsed.Query().Get("list") != ""
}

// enqueueJob applies the queue-depth cap and enqueues the job. The returned
// error carries a user-facing message. Donor jobs bypass the cap: the admin
// must never be blocked by other people's backlog.
func (b *Bot) enqueueJob(job *queue.Job, isDonor bool) error {
	if !isDonor && b.config.MaxQueueSize > 0 && b.queue.GetStatus() >= b.config.MaxQueueSize {
		return fmt.Errorf("⚠️ Очередь переполнена, попробуйте позже")
	}
	if err := b.queue.Enqueue(job); err != nil {
		return fmt.Errorf("Ошибка при добавлении задачи в очередь. Попробуйте позже")
	}
	return nil
}

func generateJobID() string {
	return fmt.Sprintf("job_%d", time.Now().UnixNano())
}

func (b *Bot) handleCallbackQuery(query *tgbotapi.CallbackQuery) {
	data := query.Data

	// Admin callbacks are checked before anything else and are never
	// available to regular users.
	if strings.HasPrefix(data, "admin_") {
		if query.From == nil || !b.isAdmin(query.From.ID) {
			b.api.Request(tgbotapi.NewCallbackWithAlert(query.ID, "Недостаточно прав"))
			return
		}
		b.api.Request(tgbotapi.NewCallback(query.ID, ""))
		b.handleAdminCallback(query)
		return
	}

	if query.Message == nil {
		return
	}
	chatID := query.Message.Chat.ID

	// Answer callback to remove loading state
	callback := tgbotapi.NewCallback(query.ID, "")
	b.api.Request(callback)

	switch {
	case data == "menu_main":
		keyboard := createMainKeyboard()
		msg := tgbotapi.NewEditMessageText(chatID, query.Message.MessageID, "Главное меню:")
		msg.ReplyMarkup = &keyboard
		b.api.Send(msg)

	case data == "menu_quality":
		var prefs *UserPreferences
		if b.preferences != nil {
			prefs = b.preferences.GetPreferences(chatID)
		}
		if prefs == nil {
			prefs = &UserPreferences{Quality: "best", MediaType: "video"}
		}
		text := fmt.Sprintf("⚙️ Выбери качество:\n\nТекущее: %s", prefs.Quality)
		keyboard := createQualityKeyboard()
		msg := tgbotapi.NewEditMessageText(chatID, query.Message.MessageID, text)
		msg.ReplyMarkup = &keyboard
		b.api.Send(msg)

	case data == "menu_media":
		var prefs *UserPreferences
		if b.preferences != nil {
			prefs = b.preferences.GetPreferences(chatID)
		}
		if prefs == nil {
			prefs = &UserPreferences{Quality: "best", MediaType: "video"}
		}
		text := fmt.Sprintf("🎵 Выбери тип:\n\nТекущий: %s", prefs.MediaType)
		keyboard := createMediaTypeKeyboard()
		msg := tgbotapi.NewEditMessageText(chatID, query.Message.MessageID, text)
		msg.ReplyMarkup = &keyboard
		b.api.Send(msg)

	case data == "cmd_status":
		b.showStatus(chatID)
		// Delete the button message
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, query.Message.MessageID)
		b.api.Send(deleteMsg)

	case strings.HasPrefix(data, "quality_"):
		if b.preferences == nil {
			b.sendMessage(chatID, "❌ Система предпочтений недоступна")
			return
		}
		quality := strings.TrimPrefix(data, "quality_")
		if err := b.preferences.SetQuality(chatID, quality); err != nil {
			b.sendMessage(chatID, "❌ Ошибка при сохранении настроек")
			return
		}
		mediaType := "видео"
		if quality == "audio" {
			mediaType = "аудио"
		}
		text := fmt.Sprintf("✅ Качество установлено: %s (%s)", quality, mediaType)
		keyboard := createQualityKeyboard()
		msg := tgbotapi.NewEditMessageText(chatID, query.Message.MessageID, text)
		msg.ReplyMarkup = &keyboard
		b.api.Send(msg)

	case strings.HasPrefix(data, "media_"):
		if b.preferences == nil {
			b.sendMessage(chatID, "❌ Система предпочтений недоступна")
			return
		}
		mediaType := strings.TrimPrefix(data, "media_")
		if err := b.preferences.SetMediaType(chatID, mediaType); err != nil {
			b.sendMessage(chatID, "❌ Ошибка при сохранении настроек")
			return
		}
		var text string
		if mediaType == "audio" {
			text = "✅ Режим установлен: скачивание аудио (MP3)\n\nТеперь отправь ссылку на видео."
		} else {
			prefs := b.preferences.GetPreferences(chatID)
			text = fmt.Sprintf("✅ Режим установлен: скачивание видео\nКачество: %s", prefs.Quality)
		}
		keyboard := createMediaTypeKeyboard()
		msg := tgbotapi.NewEditMessageText(chatID, query.Message.MessageID, text)
		msg.ReplyMarkup = &keyboard
		b.api.Send(msg)

	case strings.HasPrefix(data, "dl_q_"):
		// Format: dl_q_<quality>:<jobID>
		parts := strings.SplitN(data, ":", 2)
		if len(parts) != 2 {
			b.sendMessage(chatID, "❌ Ошибка: неверный формат запроса")
			return
		}
		qualityPart := strings.TrimPrefix(parts[0], "dl_q_")
		jobID := parts[1]

		// Retrieve the staged download state from Redis
		if b.preferences == nil {
			b.sendMessage(chatID, "❌ Система недоступна. Попробуйте отправить ссылку снова.")
			return
		}

		pending, err := b.preferences.GetPending(jobID, false)
		if err != nil || !isValidURL(pending.URL) {
			b.sendMessage(chatID, "❌ Ссылка устарела или не найдена. Пожалуйста, отправьте ссылку снова.")
			return
		}

		// Determine media type based on quality
		mediaType := "video"
		if qualityPart == "audio" {
			mediaType = "audio"
		}

		// Save preferences if available
		if b.preferences != nil {
			if qualityPart == "audio" {
				b.preferences.SetMediaType(chatID, "audio")
			} else {
				b.preferences.SetQuality(chatID, qualityPart)
			}
		}

		// Playlists take a second step: the user picks the delivery mode.
		if pending.IsPlaylist {
			pending.Quality = qualityPart
			pending.MediaType = mediaType
			if err := b.preferences.SavePending(jobID, pending); err != nil {
				b.sendMessage(chatID, "❌ Ошибка при обработке ссылки. Попробуйте позже.")
				return
			}
			text := fmt.Sprintf("📋 Плейлист готов к скачиванию (%s).\nКак доставить видео?", qualityPart)
			keyboard := createPlaylistDeliveryKeyboard(jobID)
			msg := tgbotapi.NewEditMessageText(chatID, query.Message.MessageID, text)
			msg.ReplyMarkup = &keyboard
			b.api.Send(msg)
			return
		}

		// Donor status follows the clicker, not the chat: in groups the
		// admin must keep donor perks.
		isDonor := b.isDonor(chatID) || (query.From != nil && b.isDonor(query.From.ID))
		var senderID int64
		if query.From != nil {
			senderID = query.From.ID
		}

		// Duplicate guard: the same link may already be running or queued.
		if b.isDuplicate(pending.URL, isDonor) {
			b.sendMessage(chatID, "🚫 Эта ссылка уже скачивается или стоит в очереди.")
			return
		}

		// Create job
		job := &queue.Job{
			ID:        generateJobID(),
			URL:       pending.URL,
			ChatID:    chatID,
			Priority:  queue.PriorityLow,
			Quality:   qualityPart,
			MediaType: mediaType,
			CreatedAt: time.Now(),
			FromID:    senderID,
		}

		if isDonor {
			job.Priority = queue.PriorityHigh
			job.FromDonor = true
		}

		// Add to queue
		if err := b.enqueueJob(job, isDonor); err != nil {
			b.sendMessage(chatID, err.Error())
			return
		}

		// The staged state has been consumed.
		b.preferences.DeletePending(jobID)

		// Delete the keyboard message
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, query.Message.MessageID)
		b.api.Send(deleteMsg)

	case strings.HasPrefix(data, "retry_err:"):
		// One-tap retry of exactly the videos that failed in a finished
		// playlist job. The stored list is single-use (24h TTL).
		jobID := strings.TrimPrefix(data, "retry_err:")

		rec := b.executor.TakeFailedItems(jobID)
		if rec == nil || len(rec.URLs) == 0 {
			b.sendMessage(chatID, "❌ Список ошибок устарел. Скачайте плейлист заново.")
			return
		}

		isDonor := b.isDonor(chatID) || (query.From != nil && b.isDonor(query.From.ID))
		var senderID int64
		if query.From != nil {
			senderID = query.From.ID
		}

		job := &queue.Job{
			ID:         generateJobID(),
			ChatID:     chatID,
			Priority:   queue.PriorityLow,
			Quality:    rec.Quality,
			MediaType:  rec.MediaType,
			CreatedAt:  time.Now(),
			IsPlaylist: true,
			Delivery:   rec.Delivery,
			Urls:       rec.URLs,
			FromID:     senderID,
		}
		if isDonor {
			job.Priority = queue.PriorityHigh
			job.FromDonor = true
		}

		if err := b.enqueueJob(job, isDonor); err != nil {
			b.sendMessage(chatID, err.Error())
			return
		}

		msg := tgbotapi.NewEditMessageText(chatID, query.Message.MessageID,
			fmt.Sprintf("🔁 Перезапускаю %d видео из списка ошибок…", len(rec.URLs)))
		b.api.Send(msg)

	case strings.HasPrefix(data, "pl_"):
		// Format: pl_<mode>:<jobID>, mode: seq | zip
		parts := strings.SplitN(data, ":", 2)
		if len(parts) != 2 {
			b.sendMessage(chatID, "❌ Ошибка: неверный формат запроса")
			return
		}
		mode := strings.TrimPrefix(parts[0], "pl_")
		jobID := parts[1]

		if b.preferences == nil {
			b.sendMessage(chatID, "❌ Система недоступна. Попробуйте отправить ссылку снова.")
			return
		}

		pending, err := b.preferences.GetPending(jobID, false)
		if err != nil || !pending.IsPlaylist || !isValidURL(pending.URL) {
			b.sendMessage(chatID, "❌ Ссылка устарела или не найдена. Пожалуйста, отправьте ссылку снова.")
			return
		}

		delivery := "sequential"
		if mode == "zip" {
			delivery = "zip"
		}
		quality := pending.Quality
		if quality == "" {
			quality = "best"
		}
		mediaType := pending.MediaType
		if mediaType == "" {
			mediaType = "video"
		}

		// Donor status follows the clicker, not the chat.
		isDonor := b.isDonor(chatID) || (query.From != nil && b.isDonor(query.From.ID))
		var senderID int64
		if query.From != nil {
			senderID = query.From.ID
		}

		// Duplicate guard before consuming the staged state: a blocked
		// request must not invalidate the keyboard.
		if b.isDuplicate(pending.URL, isDonor) {
			b.sendMessage(chatID, "🚫 Этот плейлист уже скачивается или стоит в очереди.")
			return
		}

		job := &queue.Job{
			ID:         generateJobID(),
			URL:        pending.URL,
			ChatID:     chatID,
			Priority:   queue.PriorityLow,
			Quality:    quality,
			MediaType:  mediaType,
			CreatedAt:  time.Now(),
			IsPlaylist: true,
			Delivery:   delivery,
			FromID:     senderID,
		}

		if isDonor {
			job.Priority = queue.PriorityHigh
			job.FromDonor = true
		}

		if err := b.enqueueJob(job, isDonor); err != nil {
			b.sendMessage(chatID, err.Error())
			return
		}

		// The staged state has been consumed.
		b.preferences.DeletePending(jobID)

		// Delete the keyboard message
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, query.Message.MessageID)
		b.api.Send(deleteMsg)
	}
}

func (b *Bot) showStatus(chatID int64) {
	status := b.queue.GetStatus()
	var prefs *UserPreferences
	if b.preferences != nil {
		prefs = b.preferences.GetPreferences(chatID)
	}
	if prefs == nil {
		prefs = &UserPreferences{Quality: "best", MediaType: "video"}
	}
	text := fmt.Sprintf("📊 Очередь: %d задач\n\n⚙️ Текущие настройки:\nКачество: %s\nТип: %s", status, prefs.Quality, prefs.MediaType)

	// Live view: what is downloading right now.
	if active := b.executor.ActiveStatus(); len(active) > 0 {
		text += "\n\n🔄 Сейчас качается:"
		for _, line := range active {
			text += "\n" + line
		}
	} else {
		text += "\n\n✅ Сейчас ничего не качается"
	}

	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = createMainKeyboard()
	b.api.Send(msg)
}

// isDuplicate reports whether this exact URL is already running or queued.
// Donors bypass the check: the admin may intentionally re-download.
func (b *Bot) isDuplicate(url string, isDonor bool) bool {
	return !isDonor && (b.executor.ActiveContainsURL(url) || b.queue.ContainsURL(url))
}
