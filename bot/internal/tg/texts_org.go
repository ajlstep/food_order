package tg

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/services"
	"food-order/shared/contracts/catalog"
	"food-order/shared/contracts/core"
	"food-order/shared/contracts/food"
	"food-order/shared/contracts/users"
)

// Texts for companies, groups, invitations and partners.

const companyHelp = `Comenzi:
<code>/companie nou Acme</code> — creezi o companie
<code>/companie invita @nickname</code> sau <code>+37369123456</code>
<code>/companie membri</code> — lista, cu buton de scos
<code>/companie nume Acme SRL</code>
<code>/companie taxa 20</code> — taxa maximă de livrare, în lei (0 = doar gratuită, <code>fara</code> = fără limită)
<code>/companie raport</code> — cine și când a comandat (<code>raport 7</code> = ultimele 7 zile)
<code>/companie iesi</code>`

const groupHelp = `Comenzi:
<code>/grup nou Etajul 3 | Str. Mare 1, of. 5</code> — grup în compania ta
<code>/grup liber Vecinii | Str. Mică 2</code> — grup fără companie
<code>/grup invita @nickname</code> sau <code>+37369123456</code>
<code>/grup membri</code>
<code>/grup iesi</code>
<code>/grup sterge</code>`

const (
	noCompanyText    = "Nu ești într-o companie.\n\n" + companyHelp
	noGroupText      = "Nu ești într-un grup.\n\n" + groupHelp
	inviteSentText   = "✅ Am trimis invitația. Expiră în 7 zile."
	leftCompanyText  = "Ai ieșit din companie."
	leftGroupText    = "Ai ieșit din grup."
	groupDeletedText = "Grupul a fost șters."
)

// orgRefusals are the words for the "no" of the org zone.
var orgRefusals = map[string]string{
	users.ErrNotOwner:         "Asta poate doar proprietarul.",
	users.ErrNotInCompany:     "Nu ești într-o companie.",
	users.ErrNotInGroup:       "Nu ești într-un grup.",
	users.ErrInCompany:        "Ești deja într-o companie. Ieși întâi din ea.",
	users.ErrInGroup:          "Ești deja într-un grup. Ieși întâi din el.",
	users.ErrOwnerStays:       "Proprietarul nu poate ieși cât mai sunt membri în companie.",
	users.ErrUserNotFound:     "Nu există așa utilizator. Trebuie să-i fi scris botului și să fi confirmat emailul.",
	users.ErrInviteExists:     "Persoana are deja invitația asta.",
	users.ErrInviteExpired:    "Invitația a expirat.",
	users.ErrOtherCompany:     "Într-un grup de companie intră doar oameni din aceeași companie.",
	users.ErrCannotInviteSelf: "Nu te poți invita pe tine.",
}

// isRefusal says whether err is this "no" of the gateway.
func isRefusal(err error, code string) bool {
	var e *services.GatewayError
	return errors.As(err, &e) && e.Code == code
}

func kindWord(kind string) string {
	if kind == food.OrgCompany {
		return "compania"
	}
	return "grupul"
}

// inviteText also says what the owner will see: nobody joins without knowing.
func inviteText(kind, name string) string {
	text := fmt.Sprintf("✉️ Ai o invitație în %s <b>%s</b>.", kindWord(kind), esc(name))
	if kind == food.OrgCompany {
		text += "\n\nProprietarul va vedea cine și când a comandat la adresele companiei. Comenzile personale nu le vede."
	}
	return text
}

func inviteKeyboard(id string) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "✅ Accept", CallbackData: encodeInvite(id, true)},
		{Text: "❌ Refuz", CallbackData: encodeInvite(id, false)},
	}}}
}

func noticeText(d food.OrgNotice) string {
	where := kindWord(d.Kind) + " <b>" + esc(d.Name) + "</b>"
	switch d.Notice {
	case food.NoticeAccepted:
		return "✅ O invitație în " + where + " a fost acceptată."
	case food.NoticeDeclined:
		return "❌ O invitație în " + where + " a fost refuzată."
	case food.NoticeRemoved:
		return "Ai fost scos din " + where + "."
	case food.NoticeLeft:
		return "Un membru a ieșit din " + where + "."
	case food.NoticeOwner:
		return "Acum tu ești proprietarul: " + where + "."
	}
	return "S-a schimbat ceva în " + where + "."
}

func companyText(c users.Company) string {
	var b strings.Builder
	fmt.Fprintf(&b, "🏢 <b>%s</b>\nMembri: %d\n", esc(c.Name), c.MemberCount)
	if c.MaxDeliveryFee != nil {
		fmt.Fprintf(&b, "Taxa maximă de livrare: %s\n", feeText(*c.MaxDeliveryFee))
	} else {
		b.WriteString("Taxa maximă de livrare: fără limită\n")
	}
	if c.IsOwner {
		b.WriteString("Tu ești proprietarul.\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func feeText(bani int64) string {
	if bani == 0 {
		return "doar livrare gratuită"
	}
	return money(float64(bani) / 100)
}

func groupText(g users.Group) string {
	var b strings.Builder
	fmt.Fprintf(&b, "📍 <b>%s</b>\nAdresa: %s\n", esc(g.Name), esc(g.Address))
	if g.CompanyName != "" {
		fmt.Fprintf(&b, "Compania: %s\n", esc(g.CompanyName))
	}
	fmt.Fprintf(&b, "Membri: %d", len(g.Members))
	return b.String()
}

func memberName(m users.Member) string {
	name := m.DisplayName
	if name == "" {
		name = m.Nickname
	}
	if name == "" {
		name = "fără nume"
	}
	return name
}

func membersText(name string, members []users.Member) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<b>%s</b>\n", esc(name))
	for i, m := range members {
		fmt.Fprintf(&b, "\n%d. %s", i+1, esc(memberName(m)))
		if m.Nickname != "" {
			fmt.Fprintf(&b, " (@%s)", esc(m.Nickname))
		}
		if m.IsOwner {
			b.WriteString(" — proprietar")
		}
	}
	return b.String()
}

// membersKeyboard has a button for every member but the owner; no buttons at
// all when the owner is alone (Telegram refuses an empty keyboard).
func membersKeyboard(members []users.Member, kind string) models.ReplyMarkup {
	var rows [][]models.InlineKeyboardButton
	for _, m := range members {
		if m.IsOwner {
			continue
		}
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: "❌ Scoate " + shorten(memberName(m), 30), CallbackData: encodeKick(kind, m.UserID),
		}})
	}
	if len(rows) == 0 {
		return nil
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func reportText(r core.CompanyReport, members []users.Member) string {
	if len(r.Items) == 0 {
		return "Nu sunt comenzi la adresele companiei în perioada asta."
	}
	names := make(map[string]string, len(members))
	for _, m := range members {
		names[m.UserID] = memberName(m)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 <b>%s — %s</b>\nComenzi: %d, în total %s\n", esc(humanDate(r.From)), esc(humanDate(r.To)), len(r.Items), money(r.Spent))
	for _, it := range r.Items {
		name := names[it.UserID]
		if name == "" {
			name = "fost membru"
		}
		fmt.Fprintf(&b, "\n%s — %s: %s", esc(humanDate(it.Date)), esc(name), esc(it.DishName))
		if it.Price != nil {
			b.WriteString(" · " + money(*it.Price))
		}
	}
	return b.String()
}

func partnersText(date string, list []catalog.Partner, now time.Time, loc *time.Location) string {
	if len(list) == 0 {
		return "Nu am încă bucătării în listă."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "🍳 <b>Pentru %s</b>\n", esc(humanDate(date)))
	for _, p := range list {
		fmt.Fprintf(&b, "\n<b>%s</b>: ", esc(p.Name))
		switch {
		case p.Closed && p.Reason != "":
			b.WriteString("închis (" + esc(p.Reason) + ")")
		case p.Closed:
			b.WriteString("închis")
		case p.OrderUntilMs > 0 && now.UnixMilli() > p.OrderUntilMs:
			b.WriteString("a trecut ora limită (" + esc(humanMoment(p.OrderUntilMs, loc)) + ")")
		default:
			b.WriteString("comenzi până " + esc(humanMoment(p.OrderUntilMs, loc)))
			if p.DeliveryFrom != "" {
				fmt.Fprintf(&b, ", livrare %s–%s", esc(p.DeliveryFrom), esc(p.DeliveryTo))
			}
		}
	}
	return b.String()
}
