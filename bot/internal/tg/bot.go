// Package tg is the Telegram side of the bot: commands, buttons and the
// messages sent when a notification arrives. The bot owns no data and asks
// everything from the gateway; who the person is stays in users, food in core.
package tg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/shared/contracts/catalog"
	contracts "food-order/shared/contracts/core"
	"food-order/shared/contracts/users"
)

// Accounts is who the person is and how they sign up. externalID is the
// Telegram user id; the gateway finds the account from it.
type Accounts interface {
	Identify(ctx context.Context, externalID string, in users.ResolveRequest) (users.ResolveResponse, error)
	SetChannelStatus(ctx context.Context, externalID, status string) error
	RequestEmailCode(ctx context.Context, externalID, email string) (users.EmailRequestResponse, error)
	ConfirmEmail(ctx context.Context, externalID, code string) (users.EmailConfirmResponse, error)
	SetPhone(ctx context.Context, externalID, phone string) error
}

// Profiles is what the person sees and sets about themselves.
type Profiles interface {
	Profile(ctx context.Context, externalID string) (users.ProfileResponse, error)
	Channels(ctx context.Context, externalID string) (users.ChannelsResponse, error)
	Settings(ctx context.Context, externalID string) (map[string]string, error)
	SetSettings(ctx context.Context, externalID string, values map[string]string) (map[string]string, error)
}

// FoodLists are the allergens and the preferences of the person.
type FoodLists interface {
	Allergens(ctx context.Context, externalID string) ([]string, error)
	SetAllergens(ctx context.Context, externalID string, allergens []string) ([]string, error)
	Prefs(ctx context.Context, externalID string) ([]users.Pref, error)
	SetPrefs(ctx context.Context, externalID string, items []users.Pref) ([]users.Pref, error)
}

// Food is the recommendations, the orders and what is known about dishes.
type Food interface {
	RequestRecommendation(ctx context.Context, externalID, date string) error
	Recommendation(ctx context.Context, externalID, date string) (contracts.Recommendation, error)
	CreateOrder(ctx context.Context, externalID string, o contracts.CreateOrderRequest) (contracts.Order, error)
	Rate(ctx context.Context, externalID string, orderID int64, rating int, comment string) error
	Orders(ctx context.Context, externalID, from, to string) ([]contracts.Order, error)
	Stats(ctx context.Context, externalID, from, to string) (contracts.Stats, error)
	ConfirmAllergens(ctx context.Context, externalID string, dishID int64, allergens []string) error
	Partners(ctx context.Context, externalID, date string) ([]catalog.Partner, error)
}

// Companies is what the bot asks about a company.
type Companies interface {
	Company(ctx context.Context, externalID string) (users.Company, error)
	CreateCompany(ctx context.Context, externalID, name string) (users.Company, error)
	UpdateCompany(ctx context.Context, externalID string, in users.CompanyUpdateRequest) (users.Company, error)
	LeaveCompany(ctx context.Context, externalID string) error
	InviteToCompany(ctx context.Context, externalID, who string) error
	RemoveFromCompany(ctx context.Context, externalID, userID string) error
	CompanyReport(ctx context.Context, externalID, from, to string) (contracts.CompanyReport, error)
}

// Groups is what the bot asks about a group.
type Groups interface {
	Group(ctx context.Context, externalID string) (users.Group, error)
	CreateGroup(ctx context.Context, externalID string, in users.GroupCreateRequest) (users.Group, error)
	DeleteGroup(ctx context.Context, externalID string) error
	LeaveGroup(ctx context.Context, externalID string) error
	InviteToGroup(ctx context.Context, externalID, who string) error
	RemoveFromGroup(ctx context.Context, externalID, userID string) error
}

// Invites is what the bot asks about invitations.
type Invites interface {
	Invites(ctx context.Context, externalID string) ([]users.Invite, error)
	AnswerInvite(ctx context.Context, externalID, inviteID string, accept bool) error
}

// Users is the gateway, as the bot sees it: everything goes through it.
type Users interface {
	Accounts
	Profiles
	FoodLists
	Food
	Companies
	Groups
	Invites
}

// Options to make a Bot.
type Options struct {
	Token        string
	AdminChatID  int64          // gets the alerts (0 = only logged)
	AllowedChats map[int64]bool // empty = anybody can /start
	Users        Users
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

// Bot is the Telegram bot.
type Bot struct {
	api     *bot.Bot
	admin   int64
	allowed map[int64]bool
	users   Users
	log     *slog.Logger
	now     func() time.Time

	// small in-memory state, per user_id (lost on restart, no harm)
	mu      sync.Mutex
	pending map[string]*pendingRating
	picks   map[string]map[string]map[int64]contracts.Pick // user -> date -> dish -> pick (for allergens)
	emails  map[string]string                              // the address a code went to, for "send again"
}

// New connects to Telegram (checks the token) and registers the handlers.
func New(o Options) (*Bot, error) {
	b := &Bot{
		admin:   o.AdminChatID,
		allowed: o.AllowedChats,
		users:   o.Users,
		log:     o.Log,
		now:     time.Now,
		pending: map[string]*pendingRating{},
		picks:   map[string]map[string]map[int64]contracts.Pick{},
		emails:  map[string]string{},
	}
	opts := []bot.Option{
		bot.WithMiddlewares(b.auth),
		bot.WithDefaultHandler(b.onText),
		bot.WithErrorsHandler(func(err error) { b.log.Warn("telegram api error", "err", err) }),
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
		"companie":   b.onCompany,
		"grup":       b.onGroup,
		"invitatii":  b.onInvites,
		"parteneri":  b.onPartners,
	} {
		api.RegisterHandler(bot.HandlerTypeMessageText, name, bot.MatchTypeCommandStartOnly, h)
	}
	for _, kind := range []string{kindOrder, kindForced, kindRate, kindNoNote, kindAllergen, kindDay, kindTime, kindToggle, kindResend, kindOtherEmail,
		kindInviteYes, kindInviteNo, kindKickCompany, kindKickGroup} {
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
		{Command: "setari", Description: "Ora, zilele de comandă, notificările"},
		{Command: "profil", Description: "Numele, emailul, telefonul"},
		{Command: "companie", Description: "Compania: membri, invitații, raport"},
		{Command: "grup", Description: "Grupul: adresa comună"},
		{Command: "invitatii", Description: "Invitațiile mele"},
		{Command: "parteneri", Description: "Bucătăriile și până când se poate comanda"},
		{Command: "stop", Description: "Opresc notificările"},
	}})
	if err != nil {
		b.log.WarnContext(ctx, "set commands", "err", err)
	}
}
