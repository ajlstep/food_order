# bot — food-order

Serviciul Telegram: interfața dintre oameni și restul serviciilor. **Nu ține date.** Tot ce întreabă merge prin `gateway`: cine e persoana stă în `users`, mâncarea în `core` și `catalog`. Din NATS citește doar coada lui (`notify.telegram`) și își trimite logurile.

Notițe: Obsidian `projects/food-order/etape/E5-Bot-gateway` și `E6-Notificari`.

## Pornire

```bash
cp .env.example .env      # completează BOT_TELEGRAM_TOKEN, BOT_ADMIN_CHAT_ID, BOT_WEBHOOK_*
go run .                  # sau: go run . -env alt.env
```

Are nevoie de `gateway`. Fără `core`, comenzile de mâncare răspund că serviciul nu răspunde. Cu `BOT_NATS_URL=` gol nu primește notificări.

Teste: `go test ./...`, cu un Telegram fals și un gateway fals.

## Unde stau lucrurile

| Pachet | Ce face |
|---|---|
| `main.go` | Doar pornire. |
| `internal/config` | Setările din `.env`. |
| `internal/services` | Clientul HTTP spre gateway: o metodă pentru fiecare rută. |
| `internal/settings` | Cuvintele din `/setari` → forma fixă din `users`, și afișarea lor. |
| `internal/notify` | Citește mesajele din coadă și le dă mai departe după fel. |
| `internal/tg` | Comenzile, butoanele și textele. Vezi mai jos. |

În `internal/tg`:

| Fișier | Ce face |
|---|---|
| `bot.go` | Interfețele de care are nevoie botul, `New`, lista comenzilor. |
| `session.go` | `auth`: rulează înaintea oricărui handler și întreabă „cine e acesta?”. |
| `signup.go` | Înregistrarea: email, cod, retrimitere; textele refuzurilor. |
| `account.go` | `/start`, `/stop`, `/profil`, telefonul, `/setari`. |
| `prefs.go` | `/preferinte`: alergeni și preferințe, două liste în `users`, una pe ecran. |
| `commands.go`, `buttons.go`, `recommendations.go` | Mâncarea. |
| `org.go` | `/companie`, `/grup`, `/invitatii`, `/parteneri`. Textele în `texts_org.go`. |
| `notifications.go` | Ce face botul cu un mesaj din coadă. |
| `send.go` | Trimiterea mesajelor și erorile pentru om. |
| `callback.go`, `keyboard.go` | Ce înseamnă un buton (scris în `callback_data`). |
| `texts*.go` | Textele. |
| `webhook.go` | HTTP: webhook și `/healthz`; polling. |
| `logfields.go` | Ce se scrie în loguri despre un update. Fără textul omului. |
