package tg

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/store"
)

// /grup — groups of users (a company). The rules are in store/groups.go:
// one group per user, join only by invitation, the owner invites (by
// @nickname or phone), sees the members and their statistics, removes
// members; anybody can leave; an empty group is deleted.

var groupSub = map[string]string{
	"nou": "new", "creeaza": "new", "creează": "new", "new": "new", "create": "new",
	"invita": "invite", "invită": "invite", "invite": "invite",
	"membri": "members", "members": "members",
	"elimina": "kick", "elimină": "kick", "scoate": "kick", "kick": "kick",
	"iesi": "leave", "ieși": "leave", "ieşi": "leave", "leave": "leave",
	"nume": "rename", "rename": "rename",
	"stat": "stats", "stats": "stats", "statistica": "stats", "statistică": "stats",
}

func (b *Bot) onGroup(ctx context.Context, _ *bot.Bot, u *models.Update) {
	args := commandArgs(u)
	word, rest, _ := strings.Cut(args, " ")
	rest = strings.TrimSpace(rest)
	sub := groupSub[strings.ToLower(word)]
	if word == "" {
		b.groupShow(ctx)
		return
	}
	if w := strings.ToLower(word); w == "help" || w == "ajutor" {
		b.reply(ctx, groupHelp)
		return
	}
	if sub == "" {
		b.reply(ctx, "Nu am înțeles.\n\n"+groupHelp)
		return
	}
	if sub == "new" {
		b.groupCreate(ctx, rest)
		return
	}

	s := sessionFrom(ctx)
	g, err := b.users.GroupOf(ctx, s.user.ID)
	if errors.Is(err, store.ErrNoGroup) {
		b.reply(ctx, groupNoneText)
		return
	}
	if err != nil {
		b.fail(ctx, "group of", err)
		return
	}
	owner := g.OwnerID == s.user.ID

	switch sub {
	case "leave":
		b.replyWith(ctx, fmt.Sprintf("Sigur ieși din grupul «%s»?", esc(g.Name)), confirmLeaveKeyboard(g.ID))
		return
	}
	if !owner {
		b.reply(ctx, "Doar owner-ul grupului poate face asta.")
		return
	}
	switch sub {
	case "invite":
		b.groupInvite(ctx, g, rest)
	case "members":
		b.groupMembers(ctx, g)
	case "kick":
		b.groupKickByQuery(ctx, g, rest)
	case "rename":
		name, err := b.users.RenameGroup(ctx, g.ID, s.user.ID, rest)
		if errors.Is(err, store.ErrBadGroupName) {
			b.reply(ctx, "Denumirea trebuie să aibă 2–64 de caractere. Exemplu: <code>/grup nume Endava Chișinău</code>")
			return
		}
		if err != nil {
			b.fail(ctx, "rename group", err)
			return
		}
		b.reply(ctx, "✅ Grupul se numește acum «"+esc(name)+"».")
	case "stats":
		b.groupStats(ctx, g, rest)
	}
}

func (b *Bot) groupShow(ctx context.Context) {
	s := sessionFrom(ctx)
	g, err := b.users.GroupOf(ctx, s.user.ID)
	if errors.Is(err, store.ErrNoGroup) {
		b.reply(ctx, groupNoneText)
		return
	}
	if err != nil {
		b.fail(ctx, "group of", err)
		return
	}
	members, err := b.users.Members(ctx, g.ID)
	if err != nil {
		b.fail(ctx, "members", err)
		return
	}
	var owner store.User
	for _, m := range members {
		if m.User.ID == g.OwnerID {
			owner = m.User
		}
	}
	b.reply(ctx, groupInfoText(g, owner, len(members), g.OwnerID == s.user.ID))
}

func (b *Bot) groupCreate(ctx context.Context, name string) {
	s := sessionFrom(ctx)
	if strings.TrimSpace(name) == "" {
		b.reply(ctx, "Scrie și denumirea grupului. Exemplu: <code>/grup nou Endava Chișinău</code>")
		return
	}
	g, err := b.users.CreateGroup(ctx, s.user.ID, name)
	b.log.InfoContext(ctx, "create group", "name", name, "group_id", g.ID, "err", err)
	switch {
	case errors.Is(err, store.ErrBadGroupName):
		b.reply(ctx, "Denumirea trebuie să aibă 2–64 de caractere. Exemplu: <code>/grup nou Endava Chișinău</code>")
	case errors.Is(err, store.ErrAlreadyInGroup):
		cur, _ := b.users.GroupOf(ctx, s.user.ID)
		b.reply(ctx, fmt.Sprintf("Ești deja în grupul «%s». Ca să creezi altul, ieși întâi: /grup iesi", esc(cur.Name)))
	case err != nil:
		b.fail(ctx, "create group", err)
	default:
		b.reply(ctx, fmt.Sprintf("✅ Am creat grupul «%s». Tu ești owner-ul.\n\n"+
			"Invită colegii după nickname sau telefon:\n<code>/grup invita @nickname</code>\n<code>/grup invita +37369123456</code>", esc(g.Name)))
	}
}

func (b *Bot) groupInvite(ctx context.Context, g store.Group, query string) {
	s := sessionFrom(ctx)
	if query == "" {
		b.reply(ctx, "Pe cine invit? Exemplu: <code>/grup invita @nickname</code> sau <code>/grup invita +37369123456</code>")
		return
	}
	target, err := b.users.FindUser(ctx, query)
	switch {
	case errors.Is(err, store.ErrNotFound):
		b.reply(ctx, "❌ "+notFoundText(query))
		return
	case err != nil:
		b.fail(ctx, "find user", err)
		return
	case target.ID == s.user.ID:
		b.reply(ctx, "Ești deja în grup. 🙂")
		return
	case target.Status != store.StatusActive:
		b.reply(ctx, fmt.Sprintf("❌ %s a oprit botul, nu-i pot trimite invitația.", esc(displayName(target))))
		return
	}
	inv, err := b.users.CreateInvite(ctx, g.ID, s.user.ID, target.ID)
	switch {
	case errors.Is(err, store.ErrAlreadyMember):
		b.reply(ctx, fmt.Sprintf("%s e deja în grup.", esc(displayName(target))))
		return
	case errors.Is(err, store.ErrInvitePending):
		b.reply(ctx, fmt.Sprintf("%s are deja o invitație de la tine; aștept să răspundă.", esc(displayName(target))))
		return
	case err != nil:
		b.fail(ctx, "create invite", err)
		return
	}
	b.log.InfoContext(ctx, "group invite created", "group_id", g.ID, "invite_id", inv.ID, "target_user_id", target.ID, "query", query)
	if err := b.sendUser(ctx, target, inviteText(s.user, g), inviteKeyboard(inv.ID)); err != nil {
		b.fail(ctx, "send invite", err)
		return
	}
	b.reply(ctx, fmt.Sprintf("📨 Am trimis invitația lui %s. Îți scriu când răspunde.", esc(displayName(target))))
}

func (b *Bot) groupMembers(ctx context.Context, g store.Group) {
	members, err := b.users.Members(ctx, g.ID)
	if err != nil {
		b.fail(ctx, "members", err)
		return
	}
	kb := membersKeyboard(g, members)
	if len(kb.InlineKeyboard) == 0 { // only the owner: nothing to remove, no keyboard
		b.reply(ctx, membersText(g, members)+"\n\nInvită colegii: <code>/grup invita @nickname</code>")
		return
	}
	b.replyWith(ctx, membersText(g, members), kb)
}

func (b *Bot) groupKickByQuery(ctx context.Context, g store.Group, query string) {
	if query == "" {
		b.groupMembers(ctx, g) // the list has a button per member
		return
	}
	target, err := b.users.FindUser(ctx, query)
	if errors.Is(err, store.ErrNotFound) {
		b.reply(ctx, "❌ "+notFoundText(query))
		return
	}
	if err != nil {
		b.fail(ctx, "find user", err)
		return
	}
	b.groupKick(ctx, g, target)
}

func (b *Bot) groupKick(ctx context.Context, g store.Group, target store.User) {
	s := sessionFrom(ctx)
	_, err := b.users.RemoveMember(ctx, g.ID, s.user.ID, target.ID)
	b.log.InfoContext(ctx, "remove group member", "group_id", g.ID, "target_user_id", target.ID, "err", err)
	switch {
	case errors.Is(err, store.ErrCannotRemoveOwner):
		b.reply(ctx, "Pe tine nu te poți scoate. Ca să ieși: /grup iesi")
		return
	case errors.Is(err, store.ErrNotMember):
		b.reply(ctx, fmt.Sprintf("%s nu e în grupul tău.", esc(displayName(target))))
		return
	case errors.Is(err, store.ErrNotOwner):
		b.reply(ctx, "Doar owner-ul grupului poate face asta.")
		return
	case err != nil:
		b.fail(ctx, "remove member", err)
		return
	}
	b.reply(ctx, fmt.Sprintf("✅ L-am scos pe %s din grup.", esc(displayName(target))))
	b.notifyUserID(ctx, target.ID, fmt.Sprintf("👥 Ai fost scos din grupul «%s».", esc(g.Name)))
}

// groupButton handles: accept/decline an invitation, remove a member,
// confirm leaving.
func (b *Bot) groupButton(ctx context.Context, _ *models.CallbackQuery, cb Callback, answer func(string), clear func()) {
	s := sessionFrom(ctx)
	switch cb.Kind {
	case kindInvite:
		inv, g, err := b.users.AnswerInvite(ctx, cb.ID, s.user.ID, cb.Yes)
		b.log.InfoContext(ctx, "group invite answered", "invite_id", cb.ID, "accept", cb.Yes, "group_id", g.ID, "status", inv.Status, "err", err)
		switch {
		case errors.Is(err, store.ErrInviteGone):
			answer("Invitația nu mai e valabilă")
			clear()
			return
		case errors.Is(err, store.ErrAlreadyInGroup):
			answer("Ești deja într-un grup")
			b.reply(ctx, fmt.Sprintf("Ești deja în grupul «%s». Ca să intri în altul, ieși întâi (/grup iesi), apoi apasă din nou „Accept”.", esc(g.Name)))
			return
		case err != nil:
			answer("Nu a mers")
			b.fail(ctx, "answer invite", err)
			return
		}
		clear()
		if inv.Status == "accepted" {
			answer("Ai intrat în grup")
			b.reply(ctx, fmt.Sprintf("✅ Ai intrat în grupul «%s».\n/grup — detalii, /grup iesi — ieși.", esc(g.Name)))
			b.notifyUserID(ctx, g.OwnerID, fmt.Sprintf("✅ %s a acceptat invitația și e acum în «%s».", esc(displayName(s.user)), esc(g.Name)))
		} else {
			answer("Ai refuzat")
			b.reply(ctx, "Ai refuzat invitația.")
			b.notifyUserID(ctx, g.OwnerID, fmt.Sprintf("❌ %s a refuzat invitația în «%s».", esc(displayName(s.user)), esc(g.Name)))
		}

	case kindKick:
		g, err := b.users.GroupOf(ctx, s.user.ID)
		if err != nil || g.OwnerID != s.user.ID {
			answer("Doar owner-ul poate scoate membri")
			return
		}
		target, err := b.users.UserByID(ctx, cb.ID)
		if err != nil {
			answer("Nu mai găsesc utilizatorul")
			return
		}
		answer("")
		b.groupKick(ctx, g, target)
		if members, err := b.users.Members(ctx, g.ID); err == nil {
			b.editKeyboard(ctx, membersKeyboard(g, members))
		}

	case kindLeave:
		g, err := b.users.GroupOf(ctx, s.user.ID)
		if err != nil || g.ID != cb.ID {
			answer("Nu mai ești în grupul acesta")
			clear()
			return
		}
		res, err := b.users.LeaveGroup(ctx, s.user.ID)
		b.log.InfoContext(ctx, "leave group", "group_id", res.Group.ID, "deleted", res.Deleted, "new_owner", res.NewOwner != nil, "err", err)
		if err != nil {
			answer("Nu a mers")
			b.fail(ctx, "leave group", err)
			return
		}
		answer("Ai ieșit")
		clear()
		if res.Deleted {
			b.reply(ctx, fmt.Sprintf("✅ Ai ieșit. Grupul «%s» nu mai avea membri și l-am șters.", esc(g.Name)))
			return
		}
		b.reply(ctx, fmt.Sprintf("✅ Ai ieșit din grupul «%s».", esc(g.Name)))
		if res.NewOwner != nil {
			b.notifyUserID(ctx, res.NewOwner.ID, fmt.Sprintf("👑 %s a ieșit din «%s». Acum tu ești owner-ul grupului.\n/grup — ce poți face.",
				esc(displayName(s.user)), esc(g.Name)))
		} else {
			b.notifyUserID(ctx, res.Group.OwnerID, fmt.Sprintf("👥 %s a ieșit din «%s».", esc(displayName(s.user)), esc(g.Name)))
		}
	}
}

// editKeyboard replaces the keyboard of the message whose button was pressed.
func (b *Bot) editKeyboard(ctx context.Context, kb models.InlineKeyboardMarkup) {
	u, ok := ctx.Value(updateKey{}).(*models.Update)
	if !ok || u.CallbackQuery == nil || u.CallbackQuery.Message.Message == nil {
		return
	}
	m := u.CallbackQuery.Message.Message
	if _, err := b.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID: m.Chat.ID, MessageID: m.ID, ReplyMarkup: kb,
	}); err != nil {
		b.log.WarnContext(ctx, "edit keyboard", "err", err)
	}
}

type updateKey struct{}

// notifyUserID tells another user something (skipped if the user stopped the bot).
func (b *Bot) notifyUserID(ctx context.Context, userID int64, text string) {
	u, err := b.users.UserByID(ctx, userID)
	if err != nil {
		b.log.WarnContext(ctx, "notify: user", "user_id", userID, "err", err)
		return
	}
	if u.Status != store.StatusActive {
		return
	}
	if err := b.sendUser(ctx, u, text, nil); err != nil {
		b.log.WarnContext(ctx, "notify", "user_id", userID, "err", err)
	}
}
