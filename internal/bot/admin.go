package bot

import (
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"envedour-bot/internal/groups"

	tgbotapi "github.com/OvyFlash/telegram-bot-api"
)

const adminAddPromptTTL = 5 * time.Minute

// adminState holds short-lived UI state. It is in-memory on purpose: after a
// restart the admin simply opens /admin again.
type adminState struct {
	mu         sync.Mutex
	addPending map[int64]time.Time
}

func newAdminState() *adminState {
	return &adminState{addPending: make(map[int64]time.Time)}
}

func (s *adminState) setAddPending(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.addPending[userID] = time.Now().Add(adminAddPromptTTL)
}

func (s *adminState) clearAddPending(userID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.addPending, userID)
}

func (s *adminState) consumeAddPending(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	deadline, ok := s.addPending[userID]
	delete(s.addPending, userID)
	return ok && time.Now().Before(deadline)
}

func (b *Bot) handleAdminCommand(msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	userID := int64(0)
	if msg.From != nil {
		userID = msg.From.ID
	}
	if !b.isAdmin(userID) {
		b.sendMessage(chatID, "❌ Недостаточно прав.")
		return
	}
	if b.admin != nil {
		b.admin.clearAddPending(userID)
	}

	// In a group the command means "allow this group". This is the most
	// reliable way to add a group: no need to copy its ID by hand.
	if !msg.Chat.IsPrivate() {
		b.addGroupFromChat(msg.Chat, userID, chatID)
		return
	}

	args := strings.Fields(msg.CommandArguments())
	if len(args) == 0 {
		b.showAdminMenu(chatID)
		return
	}

	switch strings.ToLower(args[0]) {
	case "add":
		if len(args) < 2 {
			if b.admin != nil {
				b.admin.setAddPending(userID)
			}
			b.sendMessage(chatID, "➕ Пришли ID группы (например -1001234567890) или @username.\n\n"+
				"Ещё проще: добавь бота в нужную группу и отправь /admin прямо там.")
			return
		}
		b.addGroupTarget(chatID, userID, args[1])
	case "list":
		b.showAdminList(chatID)
	case "remove", "del", "delete":
		if len(args) < 2 {
			b.showAdminRemove(chatID)
			return
		}
		b.removeGroupTarget(chatID, userID, args[1])
	default:
		b.showAdminMenu(chatID)
	}
}

func (b *Bot) handleAdminAddInput(msg *tgbotapi.Message, userID int64) {
	target := strings.TrimSpace(msg.Text)
	if target == "" {
		b.sendMessage(msg.Chat.ID, "❌ Пришли ID группы или @username.")
		return
	}
	b.addGroupTarget(msg.Chat.ID, userID, target)
}

func (b *Bot) handleAdminCallback(query *tgbotapi.CallbackQuery) {
	if query.Message == nil || query.From == nil {
		return
	}
	chatID := query.Message.Chat.ID
	userID := query.From.ID
	messageID := query.Message.MessageID

	switch {
	case query.Data == "admin_menu":
		b.editAdminMenu(chatID, messageID)
	case query.Data == "admin_add":
		if b.admin != nil {
			b.admin.setAddPending(userID)
		}
		text := "➕ Добавление группы\n\n" +
			"Пришли ID группы (например -1001234567890) или @username.\n\n" +
			"Ещё проще: добавь бота в нужную группу и отправь /admin прямо там."
		b.editAdminText(chatID, messageID, text, createAdminBackKeyboard())
	case query.Data == "admin_list":
		b.editAdminList(chatID, messageID)
	case query.Data == "admin_remove":
		b.editAdminRemove(chatID, messageID)
	case query.Data == "admin_close":
		b.api.Send(tgbotapi.NewDeleteMessage(chatID, messageID))
	case strings.HasPrefix(query.Data, "admin_del:"):
		id, err := strconv.ParseInt(strings.TrimPrefix(query.Data, "admin_del:"), 10, 64)
		if err != nil {
			b.sendMessage(chatID, "❌ Некорректный ID группы.")
			return
		}
		b.removeGroupByID(chatID, userID, id)
		b.editAdminRemove(chatID, messageID)
	}
}

func (b *Bot) showAdminMenu(chatID int64) {
	text := "🛠 Админка групп\n\n" +
		"• «Добавить» — пришли ID группы или @username\n" +
		"• В группе проще: отправь /admin прямо в нужной группе\n" +
		"• «Список» — все разрешённые группы\n" +
		"• «Удалить» — убрать группу, добавленную через /admin"
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = createAdminKeyboard()
	b.api.Send(msg)
}

func (b *Bot) editAdminMenu(chatID int64, messageID int) {
	text := "🛠 Админка групп\n\n" +
		"• «Добавить» — пришли ID группы или @username\n" +
		"• В группе проще: отправь /admin прямо в нужной группе\n" +
		"• «Список» — все разрешённые группы\n" +
		"• «Удалить» — убрать группу, добавленную через /admin"
	keyboard := createAdminKeyboard()
	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	msg.ReplyMarkup = &keyboard
	b.api.Send(msg)
}

func (b *Bot) editAdminText(chatID int64, messageID int, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	msg := tgbotapi.NewEditMessageText(chatID, messageID, text)
	msg.ReplyMarkup = &keyboard
	b.api.Send(msg)
}

func (b *Bot) showAdminList(chatID int64) {
	text := b.adminListText()
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = createAdminBackKeyboard()
	b.api.Send(msg)
}

func (b *Bot) editAdminList(chatID int64, messageID int) {
	b.editAdminText(chatID, messageID, b.adminListText(), createAdminBackKeyboard())
}

func (b *Bot) adminListText() string {
	var lines []string

	if b.groups != nil && b.groups.Len() > 0 {
		lines = append(lines, "➕ Добавлены через /admin:")
		for _, group := range b.groups.All() {
			lines = append(lines, fmt.Sprintf("• %s — %d", groupDisplayName(group), group.ID))
		}
	}

	if len(b.config.AllowedGroupIDs) > 0 {
		if len(lines) > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, "🔒 Из .env (ALLOWED_GROUP_IDS):")
		for _, id := range b.config.AllowedGroupIDs {
			lines = append(lines, fmt.Sprintf("• %d", id))
		}
	}

	if len(lines) == 0 {
		lines = append(lines, "Пока нет разрешённых групп.")
	}
	return "📋 Группы:\n\n" + strings.Join(lines, "\n")
}

func (b *Bot) showAdminRemove(chatID int64) {
	msg := tgbotapi.NewMessage(chatID, b.adminRemoveText())
	msg.ReplyMarkup = b.adminRemoveKeyboard()
	b.api.Send(msg)
}

func (b *Bot) editAdminRemove(chatID int64, messageID int) {
	b.editAdminText(chatID, messageID, b.adminRemoveText(), b.adminRemoveKeyboard())
}

func (b *Bot) adminRemoveText() string {
	text := "➖ Удаление групп\n\nВыбери группу, добавленную через /admin."
	if b.groups == nil || b.groups.Len() == 0 {
		text = "➖ Удаление групп\n\nНет групп, добавленных через /admin."
	}
	if len(b.config.AllowedGroupIDs) > 0 {
		text += "\n\n🔒 Группы из .env удалить через бота нельзя."
	}
	return text
}

func (b *Bot) adminRemoveKeyboard() tgbotapi.InlineKeyboardMarkup {
	var list []groups.Group
	if b.groups != nil {
		list = b.groups.All()
	}
	return createAdminRemoveKeyboard(list)
}

func (b *Bot) addGroupTarget(chatID int64, userID int64, target string) {
	info, err := b.resolveGroup(target)
	if err != nil {
		b.sendMessage(chatID, "❌ "+err.Error())
		return
	}
	b.addResolvedGroup(chatID, userID, info.ID, info.Title)
}

func (b *Bot) addGroupFromChat(chat tgbotapi.Chat, userID int64, replyChatID int64) {
	if !chat.IsGroup() && !chat.IsSuperGroup() {
		b.sendMessage(replyChatID, "❌ Это не группа.")
		return
	}
	b.addResolvedGroup(replyChatID, userID, chat.ID, chat.Title)
}

func (b *Bot) addResolvedGroup(replyChatID int64, userID int64, groupID int64, title string) {
	if b.isStaticGroup(groupID) {
		b.sendMessage(replyChatID, fmt.Sprintf("ℹ️ Группа %s (%d) уже разрешена через .env.", groupTitleOrID(title, groupID), groupID))
		return
	}
	if b.groups == nil {
		b.sendMessage(replyChatID, "❌ Хранилище групп недоступно.")
		return
	}

	group, created, err := b.groups.Add(groupID, title, userID)
	if err != nil {
		log.Printf("ADMIN audit: user=%d action=add_group group=%d result=error err=%v", userID, groupID, err)
		b.sendMessage(replyChatID, "❌ Не удалось сохранить группу. Проверь права на ALLOWED_GROUPS_FILE.")
		return
	}

	if created {
		log.Printf("ADMIN audit: user=%d action=add_group group=%d title=%q result=ok", userID, groupID, title)
		b.sendMessage(replyChatID, fmt.Sprintf("✅ Группа %s (%d) добавлена. Сообщения из неё теперь принимаются.", groupTitleOrID(group.Title, group.ID), group.ID))
		return
	}

	log.Printf("ADMIN audit: user=%d action=add_group group=%d title=%q result=already_present", userID, groupID, title)
	b.sendMessage(replyChatID, fmt.Sprintf("ℹ️ Группа %s (%d) уже добавлена.", groupTitleOrID(group.Title, group.ID), group.ID))
}

func (b *Bot) removeGroupTarget(chatID int64, userID int64, target string) {
	id, err := strconv.ParseInt(strings.TrimSpace(target), 10, 64)
	if err != nil {
		b.sendMessage(chatID, "❌ Для удаления нужен числовой ID группы.")
		return
	}
	b.removeGroupByID(chatID, userID, id)
}

func (b *Bot) removeGroupByID(chatID int64, userID int64, groupID int64) {
	if b.isStaticGroup(groupID) {
		b.sendMessage(chatID, fmt.Sprintf("🔒 Группа %d задана в .env (ALLOWED_GROUP_IDS). Убери её там и перезапусти сервис.", groupID))
		return
	}
	if b.groups == nil {
		b.sendMessage(chatID, "❌ Хранилище групп недоступно.")
		return
	}

	removed, err := b.groups.Remove(groupID)
	if err != nil {
		log.Printf("ADMIN audit: user=%d action=remove_group group=%d result=error err=%v", userID, groupID, err)
		b.sendMessage(chatID, "❌ Не удалось удалить группу. Проверь права на ALLOWED_GROUPS_FILE.")
		return
	}
	if !removed {
		b.sendMessage(chatID, fmt.Sprintf("ℹ️ Группы %d нет в динамическом списке.", groupID))
		return
	}

	log.Printf("ADMIN audit: user=%d action=remove_group group=%d result=ok", userID, groupID)
	b.sendMessage(chatID, fmt.Sprintf("✅ Группа %d удалена из динамического списка.", groupID))
}

func (b *Bot) resolveGroup(target string) (*tgbotapi.ChatFullInfo, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, fmt.Errorf("пустой ID или username")
	}

	config := tgbotapi.ChatInfoConfig{}
	if id, err := strconv.ParseInt(target, 10, 64); err == nil {
		if id >= 0 {
			return nil, fmt.Errorf("ID группы должен быть отрицательным")
		}
		config.ChatConfig = tgbotapi.ChatConfig{ChatID: id}
	} else {
		username := strings.TrimPrefix(target, "@")
		if username == "" {
			return nil, fmt.Errorf("укажи ID группы или @username")
		}
		config.ChatConfig = tgbotapi.ChatConfig{ChannelUsername: username}
	}

	info, err := b.api.GetChat(config)
	if err != nil {
		return nil, fmt.Errorf("не удалось получить группу: %v. Добавь бота в группу и попробуй снова", err)
	}
	if !info.IsGroup() && !info.IsSuperGroup() {
		return nil, fmt.Errorf("это не группа, а %s", info.Type)
	}
	return &info, nil
}

func groupTitleOrID(title string, id int64) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "«без названия»"
	}
	return "«" + title + "»"
}

func groupDisplayName(group groups.Group) string {
	if strings.TrimSpace(group.Title) == "" {
		return "без названия"
	}
	return group.Title
}
