package tg

import (
	"fmt"
	"strings"

	"github.com/go-telegram/bot/models"

	"food-order/bot/internal/store"
)

// Texts and keyboards of /grup.

const groupHelp = `👥 <b>Grupul</b> — colegii de la aceeași companie.

<code>/grup nou Denumire</code> — creez un grup (tu devii owner)
<code>/grup iesi</code> — ies din grup

Doar owner-ul:
<code>/grup invita @nickname</code> sau <code>/grup invita +37369123456</code>
<code>/grup membri</code> — cine e în grup (cu butoane de scos)
<code>/grup elimina @nickname</code>
<code>/grup stat</code> — cât a comandat grupul luna asta
<code>/grup stat 30</code> · <code>/grup stat 2026-10-01 2026-10-31</code>
<code>/grup stat @nickname</code> — pe o persoană
<code>/grup nume Denumire nouă</code>

Intri într-un grup doar dacă primești invitație de la owner.`

const groupStatsHelp = `Exemple:
<code>/grup stat</code> — luna aceasta
<code>/grup stat 30</code> — ultimele 30 de zile
<code>/grup stat 01.10.2026 31.10.2026</code>
<code>/grup stat @nickname 30</code>`

const groupNoneText = "👥 Nu ești în niciun grup.\n\n" +
	"Creează unul: <code>/grup nou Denumire</code>\n" +
	"Sau așteaptă o invitație de la owner-ul grupului companiei tale.\n\n/grup help — toate comenzile"

// displayName: "Ion Popescu (@ion)", "Ion", "@ion" or "utilizatorul #3".
func displayName(u store.User) string {
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	switch {
	case name != "" && u.Username != "":
		return name + " (@" + u.Username + ")"
	case name != "":
		return name
	case u.Username != "":
		return "@" + u.Username
	}
	return fmt.Sprintf("utilizatorul #%d", u.ID)
}

func notFoundText(query string) string {
	q := strings.TrimSpace(query)
	how := "Persoana trebuie să-i scrie botului /start mai întâi."
	if store.NormalizePhone(q) != "" {
		how = "Persoana trebuie să-i scrie botului /start și să-și trimită numărul din /profil."
	}
	return fmt.Sprintf("Nu există așa utilizator: %s. %s", esc(q), how)
}

func groupInfoText(g store.Group, owner store.User, count int, isOwner bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "👥 <b>%s</b>\n", esc(g.Name))
	if isOwner {
		b.WriteString("Owner: tu\n")
	} else {
		fmt.Fprintf(&b, "Owner: %s\n", esc(displayName(owner)))
	}
	fmt.Fprintf(&b, "Membri: %d\nCreat: %s\n\n", count, g.CreatedAt.Format("02.01.2006"))
	if isOwner {
		b.WriteString("<code>/grup invita @nickname</code> · <code>/grup membri</code> · <code>/grup stat</code>\n" +
			"<code>/grup elimina @nickname</code> · <code>/grup nume …</code> · <code>/grup iesi</code>")
	} else {
		b.WriteString("<code>/grup iesi</code> — ies din grup")
	}
	return b.String()
}

func inviteText(from store.User, g store.Group) string {
	return fmt.Sprintf("👥 %s te invită în grupul «<b>%s</b>».\n\n"+
		"În grup, owner-ul vede cine e membru și cât ai comandat cât ești în grup. "+
		"Poți ieși oricând cu /grup iesi.", esc(displayName(from)), esc(g.Name))
}

func inviteKeyboard(inviteID int64) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "✅ Accept", CallbackData: encodeInvite(inviteID, true)},
		{Text: "❌ Refuz", CallbackData: encodeInvite(inviteID, false)},
	}}}
}

func confirmLeaveKeyboard(groupID int64) models.InlineKeyboardMarkup {
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: "🚪 Da, ies", CallbackData: encodeLeave(groupID)},
	}}}
}

func membersText(g store.Group, members []store.Member) string {
	var b strings.Builder
	fmt.Fprintf(&b, "👥 <b>%s</b> — %d membri\n\n", esc(g.Name), len(members))
	for i, m := range members {
		role := ""
		if m.User.ID == g.OwnerID {
			role = " 👑"
		}
		stopped := ""
		if m.User.Status != store.StatusActive {
			stopped = " <i>(a oprit botul)</i>"
		}
		fmt.Fprintf(&b, "%d. %s%s%s — din %s\n", i+1, esc(displayName(m.User)), role, stopped, m.JoinedAt.Format("02.01.2006"))
	}
	if len(members) > 1 {
		b.WriteString("\nApasă pe un nume ca să-l scoți din grup.")
	}
	return b.String()
}

func membersKeyboard(g store.Group, members []store.Member) models.InlineKeyboardMarkup {
	var rows [][]models.InlineKeyboardButton
	for _, m := range members {
		if m.User.ID == g.OwnerID {
			continue
		}
		if len(rows) == 50 {
			break
		}
		rows = append(rows, []models.InlineKeyboardButton{{
			Text: "❌ Scoate " + shorten(displayName(m.User), 40), CallbackData: encodeKick(m.User.ID),
		}})
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
