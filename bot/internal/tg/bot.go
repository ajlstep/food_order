// Package tg is the Telegram side of the bot: commands, buttons and the
// messages sent when an event arrives. Food business logic stays in core;
// the bot only owns the users (who they are, how to reach them) and their
// settings (order time, order days, ...), kept in bot.db.
package tg

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/contracts"
	"food-order/bot/internal/events"
	"food-order/bot/internal/services"
	"food-order/bot/internal/settings"
	"food-order/bot/internal/store"
)

// Core is what the bot needs from the core service. userID is users.id.
type Core interface {
	RequestRecommendation(ctx context.Context, userID int64, date string) error
	Recommendation(ctx context.Context, userID int64, date string) (contracts.Recommendation, error)
	CreateOrder(ctx context.Context, userID int64, o contracts.CreateOrderRequest) (contracts.Order, error)
	Rate(ctx context.Context, userID, orderID int64, r contracts.RatingRequest) error
	Orders(ctx context.Context, userID int64, from, to string) ([]contracts.Order, error)
	Prefs(ctx context.Context, userID int64) (contracts.Prefs, error)
	SetPrefs(ctx context.Context, userID int64, p contracts.Prefs) error
	Stats(ctx context.Context, userID int64, from, to string) (contracts.Stats, error)
}

// Catalog is what the bot needs from the catalog service.
type Catalog interface {
	ConfirmAllergens(ctx context.Context, userID, dishID int64, allergens []string) error
}

// Users is the bot database (implemented by store.Store).
type Users interface {
	Register(ctx context.Context, p store.Profile) (store.User, bool, error)
	Touch(ctx context.Context, u store.User, p store.Profile) (store.User, error)
	UserByChat(ctx context.Context, chatID int64) (store.User, error)
	UserByID(ctx context.Context, id int64) (store.User, error)
	SetPhone(ctx context.Context, userID int64, phone string) error
	SetStatus(ctx context.Context, userID int64, status string) error
	Settings(ctx context.Context, userID int64) (settings.Settings, error)
	SetSetting(ctx context.Context, userID int64, key, value string) error
}

// Options to make a Bot.
type Options struct {
	Token        string
	AdminChatID  int64          // gets food.alert (0 = only logged)
	AllowedChats map[int64]bool // empty = anybody can /start
	Users        Users
	Core         Core
	Catalog      Catalog
	Log          *slog.Logger

	ServerURL string // optional: a local Bot API server, or a fake one in tests
}

// pendingRating: the user tapped a star and the bot waits for the comment.
type pendingRating struct {
	orderID int64
	rating  int
	until   time.Time
}

const commentWait = 30 * time.Minute

// Bot is the Telegram bot. It also implements events.Notifier.
type Bot struct {
	api     *bot.Bot
	admin   int64
	allowed map[int64]bool
	users   Users
	core    Core
	catalog Catalog
	log     *slog.Logger
	now     func() time.Time

	// small in-memory state, per user id (lost on restart, no harm)
	mu      sync.Mutex
	pending map[int64]*pendingRating
	picks   map[int64]map[string]map[int64]contracts.Pick // user -> date -> dish -> pick (for allergens)
}

// New connects to Telegram (checks the token) and registers the handlers.
func New(o Options) (*Bot, error) {
	b := &Bot{
		admin:   o.AdminChatID,
		allowed: o.AllowedChats,
		users:   o.Users,
		core:    o.Core,
		catalog: o.Catalog,
		log:     o.Log,
		now:     time.Now,
		pending: map[int64]*pendingRating{},
		picks:   map[int64]map[string]map[int64]contracts.Pick{},
	}
	opts := []bot.Option{
		bot.WithMiddlewares(b.auth),
		bot.WithDefaultHandler(b.onText),
		bot.WithErrorsHandler(func(err error) { b.log.Warn("telegram", "err", err) }),
		bot.WithAllowedUpdates(bot.AllowedUpdates(allowedUpdates)),
	}
	if o.ServerURL != "" {
		opts = append(opts, bot.WithServerURL(o.ServerURL))
	}
	api, err := bot.New(o.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("telegram: %w", err)
	}
	b.api = api

	for name, h := range map[string]bot.HandlerFunc{
		"start":      b.onStart,
		"help":       b.onHelp,
		"meniu":      b.onMenu,
		"alege":      b.onChoose,
		"nota":       b.onRate,
		"istoric":    b.onHistory,
		"preferinte": b.onPrefs,
		"stat":       b.onStats,
		"setari":     b.onSettings,
		"profil":     b.onProfile,
		"stop":       b.onStop,
	} {
		api.RegisterHandler(bot.HandlerTypeMessageText, name, bot.MatchTypeCommandStartOnly, h)
	}
	for _, kind := range []string{kindOrder, kindForced, kindRate, kindNoNote, kindAllergen, kindDay, kindTime} {
		api.RegisterHandler(bot.HandlerTypeCallbackQueryData, kind+":", bot.MatchTypePrefix, b.onButton)
	}
	return b, nil
}

// setCommands fills the "/" menu in Telegram.
func (b *Bot) setCommands(ctx context.Context) {
	_, err := b.api.SetMyCommands(ctx, &bot.SetMyCommandsParams{Commands: []models.BotCommand{
		{Command: "meniu", Description: "Cere recomandarea pentru următoarea zi de comandă"},
		{Command: "alege", Description: "Arată ultima recomandare"},
		{Command: "nota", Description: "Notează ultima masă"},
		{Command: "istoric", Description: "Ce am ales"},
		{Command: "preferinte", Description: "Alergeni și preferințe"},
		{Command: "stat", Description: "Statistica"},
		{Command: "setari", Description: "Ora și zilele de comandă"},
		{Command: "profil", Description: "Numele, nickname-ul, telefonul"},
		{Command: "stop", Description: "Opresc recomandările automate"},
	}})
	if err != nil {
		b.log.Warn("set commands", "err", err)
	}
}

// --- who is talking ---

// session is the user of the current update with its settings.
type session struct {
	user     store.User
	settings settings.Settings
}

type sessionKey struct{}

func withSession(ctx context.Context, s session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func sessionFrom(ctx context.Context) session {
	s, _ := ctx.Value(sessionKey{}).(session)
	return s
}

func profileOf(chatID int64, from *models.User) store.Profile {
	p := store.Profile{ChatID: chatID}
	if from != nil {
		p.TGUserID, p.FirstName, p.LastName, p.Username, p.LanguageCode = from.ID, from.FirstName, from.LastName, from.Username, from.LanguageCode
	}
	return p
}

func updateChat(u *models.Update) (chatID int64, from *models.User, private bool) {
	switch {
	case u.Message != nil:
		return u.Message.Chat.ID, u.Message.From, u.Message.Chat.Type == models.ChatTypePrivate
	case u.CallbackQuery != nil && u.CallbackQuery.Message.Message != nil:
		m := u.CallbackQuery.Message.Message
		return m.Chat.ID, &u.CallbackQuery.From, m.Chat.Type == models.ChatTypePrivate
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID, &u.CallbackQuery.From, true
	}
	return 0, nil, false
}

func isCommand(text, name string) bool {
	cmd, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /start@my_bot
	return cmd == "/"+name
}

// auth runs before every handler: only private chats, only allowed chats,
// and only registered users (except /start, which registers). It puts the
// user and its settings into ctx.
func (b *Bot) auth(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, api *bot.Bot, u *models.Update) {
		chat, from, private := updateChat(u)
		if chat == 0 {
			return
		}
		if !private {
			b.log.Info("ignored update from a group", "chat_id", chat)
			return
		}
		if len(b.allowed) > 0 && !b.allowed[chat] {
			b.log.Info("ignored update from a chat not in the allowed list", "chat_id", chat)
			return
		}
		if u.Message != nil && isCommand(u.Message.Text, "start") {
			next(ctx, api, u) // onStart registers the user
			return
		}

		usr, err := b.users.UserByChat(ctx, chat)
		switch {
		case errors.Is(err, store.ErrNotFound):
			b.answerCallback(ctx, u, "")
			b.sendTo(ctx, chat, needStartText, nil)
			return
		case err != nil:
			b.log.Error("user by chat", "err", err)
			b.answerCallback(ctx, u, "Eroare internă")
			return
		case usr.Status == store.StatusStopped:
			b.answerCallback(ctx, u, "")
			b.sendTo(ctx, chat, stoppedNeedStartText, nil)
			return
		}
		if usr, err = b.users.Touch(ctx, usr, profileOf(chat, from)); err != nil {
			b.log.Warn("touch user", "err", err)
		}
		st, err := b.users.Settings(ctx, usr.ID)
		if err != nil {
			b.log.Error("user settings", "err", err)
			st = settings.WithDefaults(nil)
		}
		next(withSession(ctx, session{user: usr, settings: st}), api, u)
	}
}

func (b *Bot) answerCallback(ctx context.Context, u *models.Update, text string) {
	if u.CallbackQuery != nil {
		_, _ = b.api.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: u.CallbackQuery.ID, Text: text})
	}
}

// --- dates, in the user's time zone ---

func (b *Bot) today(s settings.Settings) time.Time {
	loc := s.Location()
	t := b.now().In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

func (b *Bot) day(s settings.Settings, offset int) string {
	return b.today(s).AddDate(0, 0, offset).Format(dateLayout)
}

func (b *Bot) nextOrderDay(s settings.Settings) string {
	return s.NextOrderDay(b.now()).Format(dateLayout)
}

// --- sending ---

// sendTo sends an HTML message. markup may be nil, an inline keyboard,
// a reply keyboard or a keyboard removal.
func (b *Bot) sendTo(ctx context.Context, chatID int64, text string, markup models.ReplyMarkup) error {
	p := &bot.SendMessageParams{
		ChatID:             chatID,
		Text:               text,
		ParseMode:          models.ParseModeHTML,
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: bot.True()},
	}
	if markup != nil {
		p.ReplyMarkup = markup
	}
	_, err := b.api.SendMessage(ctx, p)
	return err
}

// sendUser sends to a user; if the user blocked the bot, it is stopped.
func (b *Bot) sendUser(ctx context.Context, u store.User, text string, markup models.ReplyMarkup) error {
	err := b.sendTo(ctx, u.ChatID, text, markup)
	if errors.Is(err, bot.ErrorForbidden) {
		b.log.Info("user blocked the bot, stopping it", "user_id", u.ID)
		if serr := b.users.SetStatus(ctx, u.ID, store.StatusStopped); serr != nil {
			b.log.Warn("set status", "err", serr)
		}
		return nil // nothing more to do for this user
	}
	return err
}

// reply answers the user of the current update.
func (b *Bot) reply(ctx context.Context, text string) { b.replyWith(ctx, text, nil) }

func (b *Bot) replyWith(ctx context.Context, text string, markup models.ReplyMarkup) {
	if err := b.sendUser(ctx, sessionFrom(ctx).user, text, markup); err != nil {
		b.log.Error("send", "err", err)
	}
}

// fail logs the error and tells the user in one line what went wrong.
func (b *Bot) fail(ctx context.Context, what string, err error) {
	b.log.Error(what, "user_id", sessionFrom(ctx).user.ID, "err", err)
	b.reply(ctx, "❌ "+userError(err))
}

func userError(err error) string {
	var apiErr *services.APIError
	switch {
	case errors.As(err, &apiErr) && apiErr.Message != "":
		return "Nu a mers: " + esc(apiErr.Message)
	case errors.As(err, &apiErr):
		return fmt.Sprintf("Nu a mers (cod %d).", apiErr.Status)
	case errors.Is(err, context.DeadlineExceeded):
		return "Serviciul nu a răspuns la timp. Încearcă din nou puțin mai târziu."
	default:
		return "Nu a mers: serviciul nu răspunde."
	}
}

func commandArgs(u *models.Update) string {
	if u.Message == nil {
		return ""
	}
	_, rest, _ := strings.Cut(strings.TrimSpace(u.Message.Text), " ")
	return strings.TrimSpace(rest)
}

// --- recommendations ---

func (b *Bot) rememberPicks(userID int64, date string, picks []contracts.Pick) {
	b.mu.Lock()
	defer b.mu.Unlock()
	byDate := b.picks[userID]
	if byDate == nil {
		byDate = map[string]map[int64]contracts.Pick{}
		b.picks[userID] = byDate
	}
	m := make(map[int64]contracts.Pick, len(picks))
	for _, p := range picks {
		m[p.DishID] = p
	}
	byDate[date] = m
	if len(byDate) > 7 { // keep only the last few days
		var dates []string
		for d := range byDate {
			dates = append(dates, d)
		}
		sort.Strings(dates)
		for _, d := range dates[:len(dates)-7] {
			delete(byDate, d)
		}
	}
}

func (b *Bot) showRecommendation(ctx context.Context, u store.User, date string, picks []contracts.Pick) error {
	if len(picks) == 0 {
		return b.sendUser(ctx, u, fmt.Sprintf("Pentru %s nu am găsit nimic potrivit în meniu. 😕", esc(humanDate(date))), nil)
	}
	b.rememberPicks(u.ID, date, picks)
	kb := recommendationKeyboard(date, picks, false)
	return b.sendUser(ctx, u, recommendationText(date, picks), kb)
}

// --- events.Notifier ---

// eventUser finds the user an event is for. An unknown user will never
// exist, so the event is dropped (permanent error).
func (b *Bot) eventUser(ctx context.Context, id int64) (store.User, bool, error) {
	u, err := b.users.UserByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return u, false, fmt.Errorf("%w: user %d does not exist", events.ErrPermanent, id)
	}
	if err != nil {
		return u, false, err // database problem: retry later
	}
	if u.Status != store.StatusActive {
		b.log.Info("event for a stopped user, skipped", "user_id", id)
		return u, false, nil
	}
	return u, true, nil
}

// RecommendationReady sends the picks with their buttons.
func (b *Bot) RecommendationReady(ctx context.Context, r contracts.RecommendationReady) error {
	u, ok, err := b.eventUser(ctx, r.UserID)
	if !ok {
		return err
	}
	return b.showRecommendation(ctx, u, r.Date, r.Picks)
}

// RatingRequested asks the user to rate an order.
func (b *Bot) RatingRequested(ctx context.Context, r contracts.RatingRequested) error {
	u, ok, err := b.eventUser(ctx, r.UserID)
	if !ok {
		return err
	}
	st, err := b.users.Settings(ctx, u.ID)
	if err != nil {
		return err
	}
	o := b.findOrder(ctx, u.ID, st, r.OrderID) // only for the dish name; nil is fine
	kb := ratingKeyboard(r.OrderID)
	return b.sendUser(ctx, u, ratingQuestion(o), kb)
}

// Alert forwards an alert from any service to the admin chat.
func (b *Bot) Alert(ctx context.Context, a contracts.Alert) error {
	if b.admin == 0 {
		b.log.Warn("alert (no admin chat set)", "service", a.Service, "level", a.Level, "text", a.Text, "user_id", a.UserID)
		return nil
	}
	return b.sendTo(ctx, b.admin, alertText(a), nil)
}

// NotifyUser is used by the scheduler.
func (b *Bot) NotifyUser(ctx context.Context, u store.User, text string) {
	if err := b.sendUser(ctx, u, esc(text), nil); err != nil {
		b.log.Error("notify", "user_id", u.ID, "err", err)
	}
}

func (b *Bot) findOrder(ctx context.Context, userID int64, st settings.Settings, id int64) *contracts.Order {
	orders, err := b.core.Orders(ctx, userID, b.day(st, -14), b.day(st, 7))
	if err != nil {
		b.log.Warn("orders for rating question", "err", err)
		return nil
	}
	for i := range orders {
		if orders[i].ID == id {
			return &orders[i]
		}
	}
	return nil
}
