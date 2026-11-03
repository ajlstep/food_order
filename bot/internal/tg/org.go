package tg

import (
	"context"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/shared/contracts/users"
)

// Companies, groups and invitations. The rules are in users; here only the
// words the person types and what they see.

// onCompany: "/companie" shows the company; the first word after it says
// what to do.
func (b *Bot) onCompany(ctx context.Context, _ *bot.Bot, u *models.Update) {
	word, rest, _ := strings.Cut(commandArgs(u), " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(word) {
	case "":
		b.showCompany(ctx)
	case "nou":
		b.createCompany(ctx, rest)
	case "invita":
		b.simple(ctx, "company invite", inviteSentText, b.users.InviteToCompany(ctx, personFrom(ctx).ExternalID, rest))
	case "iesi":
		b.simple(ctx, "leave company", leftCompanyText, b.users.LeaveCompany(ctx, personFrom(ctx).ExternalID))
	case "membri":
		b.companyMembers(ctx)
	case "nume":
		b.updateCompany(ctx, users.CompanyUpdateRequest{Name: &rest})
	case "taxa":
		b.setDeliveryFee(ctx, rest)
	case "raport":
		b.companyReport(ctx, rest)
	default:
		b.reply(ctx, companyHelp)
	}
}

// simple answers a call that only succeeds or refuses.
func (b *Bot) simple(ctx context.Context, what, done string, err error) {
	if err != nil {
		b.fail(ctx, what, err)
		return
	}
	b.reply(ctx, done)
}

func (b *Bot) showCompany(ctx context.Context) {
	c, err := b.users.Company(ctx, personFrom(ctx).ExternalID)
	if isRefusal(err, users.ErrNotInCompany) {
		b.reply(ctx, noCompanyText)
		return
	}
	if err != nil {
		b.fail(ctx, "company", err)
		return
	}
	if c.IsOwner {
		b.reply(ctx, companyText(c)+"\n\n"+companyHelp)
		return
	}
	b.reply(ctx, companyText(c))
}

func (b *Bot) createCompany(ctx context.Context, name string) {
	if name == "" {
		b.reply(ctx, companyHelp)
		return
	}
	c, err := b.users.CreateCompany(ctx, personFrom(ctx).ExternalID, name)
	if err != nil {
		b.fail(ctx, "create company", err)
		return
	}
	b.log.InfoContext(ctx, "company created", "company_id", c.ID)
	b.reply(ctx, "✅ Ai creat compania <b>"+esc(c.Name)+"</b>.\n\n"+companyHelp)
}

func (b *Bot) companyMembers(ctx context.Context) {
	c, err := b.users.Company(ctx, personFrom(ctx).ExternalID)
	if err != nil {
		b.fail(ctx, "company", err)
		return
	}
	if !c.IsOwner {
		b.reply(ctx, orgRefusals[users.ErrNotOwner])
		return
	}
	b.replyWith(ctx, membersText(c.Name, c.Members), membersKeyboard(c.Members, kindKickCompany))
}

func (b *Bot) updateCompany(ctx context.Context, in users.CompanyUpdateRequest) {
	c, err := b.users.UpdateCompany(ctx, personFrom(ctx).ExternalID, in)
	if err != nil {
		b.fail(ctx, "update company", err)
		return
	}
	b.reply(ctx, "✅ Salvat.\n\n"+companyText(c))
}

// setDeliveryFee: "/companie taxa 20" (lei), "0" = only free delivery,
// "fara" = no limit.
func (b *Bot) setDeliveryFee(ctx context.Context, v string) {
	if strings.EqualFold(v, "fara") || strings.EqualFold(v, "fără") {
		b.updateCompany(ctx, users.CompanyUpdateRequest{ClearFee: true})
		return
	}
	lei, err := strconv.ParseInt(v, 10, 64)
	if err != nil || lei < 0 {
		b.reply(ctx, companyHelp)
		return
	}
	bani := lei * 100
	b.updateCompany(ctx, users.CompanyUpdateRequest{MaxDeliveryFee: &bani})
}

// companyReport: "/companie raport" is this month, "/companie raport 7" the
// last 7 days; both also show the days already ordered ahead. core gives the
// orders, the names come from the members.
func (b *Bot) companyReport(ctx context.Context, args string) {
	p := personFrom(ctx)
	st := b.settingsOf(ctx, p)
	today := b.today(st)
	from := today.AddDate(0, 0, -today.Day()+1).Format(dateLayout)
	if n, err := strconv.Atoi(args); err == nil && n > 0 && n <= 366 {
		from = b.day(st, -(n - 1))
	}
	report, err := b.users.CompanyReport(ctx, p.ExternalID, from, b.day(st, reportAhead))
	if err != nil {
		b.fail(ctx, "company report", err)
		return
	}
	c, err := b.users.Company(ctx, p.ExternalID)
	if err != nil {
		b.fail(ctx, "company", err)
		return
	}
	b.reply(ctx, reportText(report, c.Members))
}

// reportAhead: orders are made for the next days, a report must show them.
const reportAhead = 7

// onGroup: "/grup" shows the group; the first word after it says what to do.
func (b *Bot) onGroup(ctx context.Context, _ *bot.Bot, u *models.Update) {
	word, rest, _ := strings.Cut(commandArgs(u), " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(word) {
	case "":
		b.showGroup(ctx)
	case "nou":
		b.createGroup(ctx, rest, false)
	case "liber":
		b.createGroup(ctx, rest, true)
	case "invita":
		b.simple(ctx, "group invite", inviteSentText, b.users.InviteToGroup(ctx, personFrom(ctx).ExternalID, rest))
	case "iesi":
		b.simple(ctx, "leave group", leftGroupText, b.users.LeaveGroup(ctx, personFrom(ctx).ExternalID))
	case "sterge":
		b.simple(ctx, "delete group", groupDeletedText, b.users.DeleteGroup(ctx, personFrom(ctx).ExternalID))
	case "membri":
		b.groupMembers(ctx)
	default:
		b.reply(ctx, groupHelp)
	}
}

func (b *Bot) showGroup(ctx context.Context) {
	g, err := b.users.Group(ctx, personFrom(ctx).ExternalID)
	if isRefusal(err, users.ErrNotInGroup) {
		b.reply(ctx, noGroupText)
		return
	}
	if err != nil {
		b.fail(ctx, "group", err)
		return
	}
	b.reply(ctx, groupText(g))
}

// createGroup: "/grup nou Etajul 3 | Str. Mare 1". The bar separates the name
// from the address, because both have spaces.
func (b *Bot) createGroup(ctx context.Context, args string, free bool) {
	name, address, ok := strings.Cut(args, "|")
	name, address = strings.TrimSpace(name), strings.TrimSpace(address)
	if !ok || name == "" || address == "" {
		b.reply(ctx, groupHelp)
		return
	}
	g, err := b.users.CreateGroup(ctx, personFrom(ctx).ExternalID, users.GroupCreateRequest{Name: name, Address: address, Free: free})
	if err != nil {
		b.fail(ctx, "create group", err)
		return
	}
	b.log.InfoContext(ctx, "group created", "group_id", g.ID, "company_id", g.CompanyID)
	b.reply(ctx, "✅ Ai creat grupul.\n\n"+groupText(g))
}

func (b *Bot) groupMembers(ctx context.Context) {
	g, err := b.users.Group(ctx, personFrom(ctx).ExternalID)
	if err != nil {
		b.fail(ctx, "group", err)
		return
	}
	var markup models.ReplyMarkup
	if g.IsOwner {
		markup = membersKeyboard(g.Members, kindKickGroup)
	}
	b.replyWith(ctx, membersText(g.Name, g.Members), markup)
}

// onInvites lists the open invitations, each with its buttons.
func (b *Bot) onInvites(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	list, err := b.users.Invites(ctx, personFrom(ctx).ExternalID)
	if err != nil {
		b.fail(ctx, "invites", err)
		return
	}
	if len(list) == 0 {
		b.reply(ctx, "Nu ai invitații.")
		return
	}
	for _, in := range list {
		b.replyWith(ctx, inviteText(in.Kind, in.Name), inviteKeyboard(in.ID))
	}
}

// onPartners shows the kitchens and the order limit for the next meal day.
func (b *Bot) onPartners(ctx context.Context, _ *bot.Bot, _ *models.Update) {
	p := personFrom(ctx)
	st := b.settingsOf(ctx, p)
	date := b.recommendationDay(st)
	list, err := b.users.Partners(ctx, p.ExternalID, date)
	if err != nil {
		b.fail(ctx, "partners", err)
		return
	}
	b.reply(ctx, partnersText(date, list, b.now(), st.Location()))
}

// orgButton handles an answer to an invitation and taking a member out.
func (b *Bot) orgButton(ctx context.Context, cb Callback, answer func(string), clear func()) {
	external := personFrom(ctx).ExternalID
	var err error
	done := "Salvat"
	switch cb.Kind {
	case kindInviteYes:
		err, done = b.users.AnswerInvite(ctx, external, cb.ID, true), "Ai acceptat"
	case kindInviteNo:
		err, done = b.users.AnswerInvite(ctx, external, cb.ID, false), "Ai refuzat"
	case kindKickCompany:
		err = b.users.RemoveFromCompany(ctx, external, cb.ID)
	case kindKickGroup:
		err = b.users.RemoveFromGroup(ctx, external, cb.ID)
	}
	if err != nil {
		answer("Nu a mers")
		b.fail(ctx, "org button", err)
		return
	}
	answer(done)
	clear()
}
