package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"food-order/bot/internal/applog"
	"food-order/shared/contracts/catalog"
	"food-order/shared/contracts/core"
	"food-order/shared/contracts/gateway"
	"food-order/shared/contracts/notify"
	"food-order/shared/contracts/users"
	"food-order/shared/rpc"
)

// maxAnswer is the largest answer of the gateway the bot reads.
const maxAnswer = 1 << 20

// GatewayError is a "no" from the gateway or from a service behind it. Code
// is stable, so the bot can choose the words; Message is for the logs.
type GatewayError struct {
	Status  int
	Code    string
	Message string
	Details map[string]any
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("gateway %d: %s: %s", e.Status, e.Code, e.Message)
}

// Text is the sentence for the person, for the services that write one
// themselves (core, catalog). "" when there is none.
func (e *GatewayError) Text() string {
	t, _ := e.Details["text"].(string)
	return t
}

// IsNotFound says whether err is a "no such thing" from the gateway.
func IsNotFound(err error) bool {
	var e *GatewayError
	return errors.As(err, &e) && e.Code == rpc.CodeNotFound
}

// Number reads a number from Details (retry_after, attempts_left).
func (e *GatewayError) Number(key string) int {
	v, _ := e.Details[key].(float64) // JSON numbers arrive as float64
	return int(v)
}

// Gateway is the client of the api-gateway. Every method speaks for one
// person: externalID is the Telegram user id.
type Gateway struct {
	base string
	key  string
	http *http.Client
	log  *slog.Logger
}

// NewGateway makes a client. baseURL is like http://gateway:8080; key is the
// key of the bot (GATEWAY_BOT_KEY).
func NewGateway(baseURL, key string, hc *http.Client, log *slog.Logger) *Gateway {
	return &Gateway{base: strings.TrimRight(baseURL, "/"), key: key, http: hc, log: log}
}

// call makes one request. route is a constant of contracts/gateway, like
// "POST /v1/auth/email". The body is never logged: it may hold an email, a
// code or a phone number.
func (g *Gateway) call(ctx context.Context, externalID, route string, in, out any) error {
	start := time.Now()
	status, err := g.do(ctx, externalID, route, in, out)
	var refused *GatewayError
	switch {
	case err == nil:
		g.log.DebugContext(ctx, "gateway call", "route", route, "status", status, "ms", applog.Since(start))
	case errors.As(err, &refused) && refused.Status < 500:
		g.log.DebugContext(ctx, "gateway refused", "route", route, "status", status, "code", refused.Code, "ms", applog.Since(start))
	default:
		g.log.WarnContext(ctx, "gateway call failed", "route", route, "status", status, "err", err, "ms", applog.Since(start))
	}
	return err
}

func (g *Gateway) do(ctx context.Context, externalID, route string, in, out any) (status int, err error) {
	req, err := g.newRequest(ctx, externalID, route, in)
	if err != nil {
		return 0, err
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	var answer gateway.Response
	limited := http.MaxBytesReader(nil, resp.Body, maxAnswer)
	if err := json.NewDecoder(limited).Decode(&answer); err != nil {
		return resp.StatusCode, fmt.Errorf("%s: bad answer: %w", route, err)
	}
	if !answer.OK {
		e := &GatewayError{Status: resp.StatusCode}
		if answer.Error != nil {
			e.Code, e.Message, e.Details = answer.Error.Code, answer.Error.Message, answer.Error.Details
		}
		return resp.StatusCode, e
	}
	if out != nil && len(answer.Data) > 0 {
		if err := json.Unmarshal(answer.Data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s: bad data: %w", route, err)
		}
	}
	return resp.StatusCode, nil
}

func (g *Gateway) newRequest(ctx context.Context, externalID, route string, in any) (*http.Request, error) {
	method, path, _ := strings.Cut(route, " ")
	var body bytes.Buffer
	if in != nil {
		if err := json.NewEncoder(&body).Encode(in); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, g.base+path, &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(gateway.HeaderAuthorization, "Bearer "+g.key)
	req.Header.Set(gateway.HeaderChannel, notify.ChannelTelegram)
	req.Header.Set(gateway.HeaderExternalID, externalID)
	req.Header.Set("Content-Type", "application/json")
	for _, a := range applog.Fields(ctx) {
		if a.Key == "req_id" { // the gateway logs the same id
			req.Header.Set(gateway.HeaderRequestID, a.Value.String())
		}
	}
	return req, nil
}

// --- registration ---

// Identify says who wrote. It is called for every update.
func (g *Gateway) Identify(ctx context.Context, externalID string, in users.ResolveRequest) (users.ResolveResponse, error) {
	var out users.ResolveResponse
	err := g.call(ctx, externalID, gateway.Identify, in, &out)
	return out, err
}

// SetChannelStatus: /stop, /start, or the person blocked the bot.
func (g *Gateway) SetChannelStatus(ctx context.Context, externalID, status string) error {
	return g.call(ctx, externalID, gateway.ChannelStatus, gateway.ChannelStatusRequest{Status: status}, nil)
}

// RequestEmailCode asks for a confirmation code to be sent.
func (g *Gateway) RequestEmailCode(ctx context.Context, externalID, email string) (users.EmailRequestResponse, error) {
	var out users.EmailRequestResponse
	err := g.call(ctx, externalID, gateway.EmailRequest, users.EmailRequestRequest{Email: email}, &out)
	return out, err
}

// ConfirmEmail checks the code the person typed.
func (g *Gateway) ConfirmEmail(ctx context.Context, externalID, code string) (users.EmailConfirmResponse, error) {
	var out users.EmailConfirmResponse
	err := g.call(ctx, externalID, gateway.EmailConfirm, users.EmailConfirmRequest{Code: code}, &out)
	return out, err
}

// SetPhone saves the number the person shared with the messenger button.
func (g *Gateway) SetPhone(ctx context.Context, externalID, phone string) error {
	return g.call(ctx, externalID, gateway.PhoneSet, users.PhoneSetRequest{Phone: phone}, nil)
}

// --- profile ---

// Profile returns the account.
func (g *Gateway) Profile(ctx context.Context, externalID string) (users.ProfileResponse, error) {
	var out users.ProfileResponse
	err := g.call(ctx, externalID, gateway.ProfileGet, nil, &out)
	return out, err
}

// Channels lists the channels of the person.
func (g *Gateway) Channels(ctx context.Context, externalID string) (users.ChannelsResponse, error) {
	var out users.ChannelsResponse
	err := g.call(ctx, externalID, gateway.ChannelsGet, nil, &out)
	return out, err
}

// Settings returns every setting: the saved value, or the default.
func (g *Gateway) Settings(ctx context.Context, externalID string) (map[string]string, error) {
	var out users.SettingsResponse
	err := g.call(ctx, externalID, gateway.SettingsGet, nil, &out)
	return out.Settings, err
}

// SetSettings changes some settings (all or none) and returns all of them.
func (g *Gateway) SetSettings(ctx context.Context, externalID string, values map[string]string) (map[string]string, error) {
	var out users.SettingsResponse
	err := g.call(ctx, externalID, gateway.SettingsSet, users.SettingsSetRequest{Values: values}, &out)
	return out.Settings, err
}

// Allergens returns the list of allergens.
func (g *Gateway) Allergens(ctx context.Context, externalID string) ([]string, error) {
	var out users.AllergensResponse
	err := g.call(ctx, externalID, gateway.AllergensGet, nil, &out)
	return out.Allergens, err
}

// SetAllergens replaces the list and returns it as it was saved.
func (g *Gateway) SetAllergens(ctx context.Context, externalID string, allergens []string) ([]string, error) {
	var out users.AllergensResponse
	err := g.call(ctx, externalID, gateway.AllergensSet, users.AllergensSetRequest{Allergens: nonNil(allergens)}, &out)
	return out.Allergens, err
}

// Prefs returns the food preferences.
func (g *Gateway) Prefs(ctx context.Context, externalID string) ([]users.Pref, error) {
	var out users.PrefsResponse
	err := g.call(ctx, externalID, gateway.PrefsGet, nil, &out)
	return out.Items, err
}

// SetPrefs replaces the list and returns it as it was saved.
func (g *Gateway) SetPrefs(ctx context.Context, externalID string, items []users.Pref) ([]users.Pref, error) {
	var out users.PrefsResponse
	err := g.call(ctx, externalID, gateway.PrefsSet, users.PrefsSetRequest{Items: nonNil(items)}, &out)
	return out.Items, err
}

// --- food ---

// RequestRecommendation asks for the recommendation of a day. The result
// comes later, in the queue.
func (g *Gateway) RequestRecommendation(ctx context.Context, externalID, date string) error {
	return g.call(ctx, externalID, gateway.RecommendationCreate, core.CreateRecommendationRequest{Date: date}, nil)
}

// Recommendation returns the last recommendation of a day.
func (g *Gateway) Recommendation(ctx context.Context, externalID, date string) (core.Recommendation, error) {
	var out core.Recommendation
	route := gateway.RecommendationGet + "?" + url.Values{"date": {date}}.Encode()
	err := g.call(ctx, externalID, route, nil, &out)
	return out, err
}

// CreateOrder saves the choice of the person.
func (g *Gateway) CreateOrder(ctx context.Context, externalID string, o core.CreateOrderRequest) (core.Order, error) {
	var out core.Order
	err := g.call(ctx, externalID, gateway.OrderCreate, o, &out)
	return out, err
}

// Rate saves the rating (1..5) and the comment of an order.
func (g *Gateway) Rate(ctx context.Context, externalID string, orderID int64, rating int, comment string) error {
	route := withID(gateway.OrderRate, strconv.FormatInt(orderID, 10))
	return g.call(ctx, externalID, route, core.RateRequest{Rating: rating, Comment: comment}, nil)
}

// Orders returns the orders between two dates (inclusive), oldest first.
func (g *Gateway) Orders(ctx context.Context, externalID, from, to string) ([]core.Order, error) {
	var out core.OrdersResponse
	err := g.call(ctx, externalID, withPeriod(gateway.OrdersList, from, to), nil, &out)
	return out.Items, err
}

// Stats returns the statistics between two dates (inclusive).
func (g *Gateway) Stats(ctx context.Context, externalID, from, to string) (core.Stats, error) {
	var out core.Stats
	err := g.call(ctx, externalID, withPeriod(gateway.StatsGet, from, to), nil, &out)
	return out, err
}

// ConfirmAllergens saves what the person says about a dish (an empty list
// means "I checked: none of them").
func (g *Gateway) ConfirmAllergens(ctx context.Context, externalID string, dishID int64, allergens []string) error {
	route := withID(gateway.AllergensConfirm, strconv.FormatInt(dishID, 10))
	return g.call(ctx, externalID, route, catalog.ConfirmAllergensRequest{Allergens: nonNil(allergens)}, nil)
}

// Partners returns the partners and how things stand on date.
func (g *Gateway) Partners(ctx context.Context, externalID, date string) ([]catalog.Partner, error) {
	var out catalog.PartnersResponse
	route := gateway.PartnersList + "?" + url.Values{"date": {date}}.Encode()
	err := g.call(ctx, externalID, route, nil, &out)
	return out.Items, err
}

// --- companies, groups, invitations ---

// Company returns the company of the person.
func (g *Gateway) Company(ctx context.Context, externalID string) (users.Company, error) {
	var out users.Company
	err := g.call(ctx, externalID, gateway.CompanyGet, nil, &out)
	return out, err
}

// CreateCompany makes a company with the person as owner.
func (g *Gateway) CreateCompany(ctx context.Context, externalID, name string) (users.Company, error) {
	var out users.Company
	err := g.call(ctx, externalID, gateway.CompanyCreate, users.CompanyCreateRequest{Name: name}, &out)
	return out, err
}

// UpdateCompany changes the company (owner only).
func (g *Gateway) UpdateCompany(ctx context.Context, externalID string, in users.CompanyUpdateRequest) (users.Company, error) {
	var out users.Company
	err := g.call(ctx, externalID, gateway.CompanyUpdate, in, &out)
	return out, err
}

// LeaveCompany takes the person out of the company, and out of its group.
func (g *Gateway) LeaveCompany(ctx context.Context, externalID string) error {
	return g.call(ctx, externalID, gateway.CompanyLeave, nil, nil)
}

// InviteToCompany invites somebody by @nickname or phone.
func (g *Gateway) InviteToCompany(ctx context.Context, externalID, who string) error {
	return g.call(ctx, externalID, gateway.CompanyInvite, users.InviteRequest{Who: who}, nil)
}

// RemoveFromCompany takes a member out (owner only).
func (g *Gateway) RemoveFromCompany(ctx context.Context, externalID, userID string) error {
	return g.call(ctx, externalID, withID(gateway.CompanyRemove, userID), nil, nil)
}

// CompanyReport returns who ordered what at the addresses of the company.
func (g *Gateway) CompanyReport(ctx context.Context, externalID, from, to string) (core.CompanyReport, error) {
	var out core.CompanyReport
	err := g.call(ctx, externalID, withPeriod(gateway.CompanyReport, from, to), nil, &out)
	return out, err
}

// Group returns the group of the person.
func (g *Gateway) Group(ctx context.Context, externalID string) (users.Group, error) {
	var out users.Group
	err := g.call(ctx, externalID, gateway.GroupGet, nil, &out)
	return out, err
}

// CreateGroup makes a group with the person as owner.
func (g *Gateway) CreateGroup(ctx context.Context, externalID string, in users.GroupCreateRequest) (users.Group, error) {
	var out users.Group
	err := g.call(ctx, externalID, gateway.GroupCreate, in, &out)
	return out, err
}

// DeleteGroup deletes the group (owner only).
func (g *Gateway) DeleteGroup(ctx context.Context, externalID string) error {
	return g.call(ctx, externalID, gateway.GroupDelete, nil, nil)
}

// LeaveGroup takes the person out of the group.
func (g *Gateway) LeaveGroup(ctx context.Context, externalID string) error {
	return g.call(ctx, externalID, gateway.GroupLeave, nil, nil)
}

// InviteToGroup invites somebody by @nickname or phone.
func (g *Gateway) InviteToGroup(ctx context.Context, externalID, who string) error {
	return g.call(ctx, externalID, gateway.GroupInvite, users.InviteRequest{Who: who}, nil)
}

// RemoveFromGroup takes a member out (owner only).
func (g *Gateway) RemoveFromGroup(ctx context.Context, externalID, userID string) error {
	return g.call(ctx, externalID, withID(gateway.GroupRemove, userID), nil, nil)
}

// Invites lists the open invitations of the person.
func (g *Gateway) Invites(ctx context.Context, externalID string) ([]users.Invite, error) {
	var out users.InvitesResponse
	err := g.call(ctx, externalID, gateway.InvitesList, nil, &out)
	return out.Items, err
}

// AnswerInvite accepts or declines an invitation.
func (g *Gateway) AnswerInvite(ctx context.Context, externalID, inviteID string, accept bool) error {
	route := gateway.InviteDecline
	if accept {
		route = gateway.InviteAccept
	}
	return g.call(ctx, externalID, withID(route, inviteID), nil, nil)
}

// withID puts an id in a route written with {id}.
func withID(route, id string) string {
	return strings.Replace(route, "{id}", url.PathEscape(id), 1)
}

func withPeriod(route, from, to string) string {
	return route + "?" + url.Values{"from": {from}, "to": {to}}.Encode()
}

// nonNil: an empty list must go as [], which means "clear it", not as null.
func nonNil[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// ExternalID writes a Telegram id the way the gateway receives it.
func ExternalID(telegramID int64) string { return strconv.FormatInt(telegramID, 10) }
