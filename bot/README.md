# bot — food-order

Serviciul Telegram: interfața dintre utilizatori și restul serviciilor. Logica despre mâncare rămâne în `core`. Botul **are baza lui** (`bot.db`, SQLite) doar cu **utilizatorii** (cine sunt și cum îi găsesc în Telegram) și **setările lor** (ora și zilele de comandă, ...). `core` și celelalte servicii cunosc doar `users.id`.

Notițe: Obsidian `projects/food-order/Bot` și `Comunicare`.

## Pornire

```bash
cp .env.example .env      # completează BOT_TELEGRAM_TOKEN, BOT_ADMIN_CHAT_ID, BOT_WEBHOOK_*
go mod tidy               # după actualizare: completează go.sum pentru SQLite
go run .                  # sau: go run . -env alt.env
```

Fără `core` și NATS botul pornește oricum: comenzile răspund cu „serviciul nu răspunde", iar cu `BOT_NATS_URL=` gol evenimentele sunt oprite. Baza `data/bot.db` se creează singură la prima pornire.

Teste: `go test ./...` — inclusiv teste cap-coadă cu un Telegram fals, un `core` fals și o bază reală într-un folder temporar.

## Utilizatori și setări

**Conectare.** Oricine (sau doar cei din `BOT_ALLOWED_CHAT_IDS`) trimite `/start` → botul îl salvează în `users` cu numele și nickname-ul din Telegram, primește setările implicite și e întrebat (opțional) de telefon, cu butonul „📱 Trimite numărul". Până la `/start`, orice altă comandă răspunde „Trimite /start". Doar chat-uri private; grupurile sunt ignorate.

**Ce păstrează botul** (`bot.db`):

| Tabel | Ce e în el |
|---|---|
| `users` | `id` (id-ul din tot proiectul), `chat_id`, `tg_user_id`, `first_name`, `last_name`, `username`, `phone`, `language_code`, `status` (`active` / `stopped`), date |
| `user_settings` | `(user_id, key) → value` — o setare pe rând, text |
| `reminders` | `(user_id, for_date)` — recomandarea automată cerută deja pentru ziua asta (să nu plece de două ori, nici după repornire) |
| `schema_version` | versiunea schemei; migrările rulează singure la pornire |

Numele și nickname-ul se actualizează singure la fiecare mesaj. Telefonul se primește doar dacă utilizatorul apasă butonul (Telegram nu-l dă altfel) și doar al lui însuși.

**Setări** (`internal/settings`):

| Cheie | Implicit | Cum o schimb |
|---|---|---|
| `order_time` — ora de comandă | `16:00` | `/setari ora 16:30` sau butoanele ±30 min |
| `order_days` — zilele de comandă (ISO, 1 = luni) | `1,2,3,4,5` | `/setari zile luni-vineri`, `/setari zile lu,mi,vi` sau butoanele cu zile |
| `timezone` — fusul orar | `Europe/Chisinau` | `/setari fus Europe/Chisinau` |

**Setare nouă:** adaugi un `Def` în `settings.Defs` (cheie, alias-uri, implicit, `Parse`, `Format`) și, dacă o folosește codul, o metodă pe `Settings`. Nu trebuie migrare: valorile stau în `user_settings` ca `key/value`.

**Ora de comandă.** Programatorul botului (`internal/scheduler`) verifică în fiecare minut: dacă azi e zi de comandă și a venit ora (fereastră de 2 h, pentru repornire), cere la `core` recomandarea **pentru următoarea zi de comandă** — luni–vineri la 16:00: vineri → luni, luni → marți. O singură dată pe zi (tabelul `reminders`); dacă `core` nu răspunde, reîncearcă la fiecare minut de cel mult 5 ori, apoi îi scrie utilizatorului. Meniul tot `core` îl cere, cu programatorul lui.

`/stop` → `status = stopped`: nu mai primește recomandări automate, comenzile cer `/start`. Dacă utilizatorul blochează botul, Telegram răspunde 403 și botul îl trece singur pe `stopped`. Datele rămân.

## Grupuri (companii)

Utilizatorii de la aceeași companie se adună într-un grup. Tot în `bot.db`; `core` nu știe de grupuri.

**Reguli**
- Un utilizator e în **cel mult un grup**.
- Cine creează grupul îi dă denumirea și devine **owner** (și primul membru).
- În grup intri **doar prin invitație** de la owner: îți vine mesaj cu „✅ Accept / ❌ Refuz". Invitația expiră în 7 zile.
- Owner-ul invită după **@nickname** sau **număr de telefon**. Dacă nu există un asemenea utilizator (nu i-a scris botului `/start`, sau nu și-a trimis telefonul), owner-ul primește răspuns că nu există.
- Owner-ul vede membrii, îi poate scoate, vede statistica grupului sau a fiecărui membru pe o perioadă.
- Oricine poate ieși. Dacă iese owner-ul, devine owner cel mai vechi membru. **Grupul fără membri se șterge** singur, cu invitațiile lui.

**Tabele**

| Tabel | Ce e în el |
|---|---|
| `groups` | `id`, `name`, `owner_id`, date |
| `group_members` | `(group_id, user_id)`, `joined_at`; `user_id` unic = un singur grup |
| `group_invites` | `id`, `group_id`, `user_id` (invitat), `invited_by`, `status` (`pending` / `accepted` / `declined` / `expired`), date; cel mult o invitație în așteptare pe grup și persoană |

**Comenzi**

| Comandă | Cine | Ce face |
|---|---|---|
| `/grup` | toți | grupul meu (sau cum creez unul) |
| `/grup nou <denumire>` | cine nu e în grup | creează grupul |
| `/grup invita @nickname` / `+37369…` | owner | trimite invitația |
| `/grup membri` | owner | lista, cu buton „❌ Scoate" la fiecare |
| `/grup elimina @nickname` | owner | scoate un membru |
| `/grup stat` · `/grup stat 30` · `/grup stat 2026-10-01 2026-10-31` | owner | cât a comandat grupul (total + pe fiecare) |
| `/grup stat @nickname [perioadă]` | owner | statistica unui membru |
| `/grup nume <denumire>` | owner | redenumește |
| `/grup iesi` | toți | ies (cu confirmare) |

Statistica grupului: botul cere la `core` `GET /v1/stats` pentru fiecare membru (cu `X-User-ID`-ul lui) și le adună. Fiecare membru se numără **doar de când e în grup**; cine a ieșit nu mai apare.

## Cum vede `core` utilizatorul

Fiecare apel HTTP de la bot la `core` (și la `catalog`) are antetul **`X-User-ID: <users.id>`**. `core` ține comenzile, preferințele (alergeni etc.), recomandările și statistica pe `user_id` și nu vede niciodată chat id, nume sau telefon. Evenimentele către bot poartă `user_id`, iar botul găsește chat-ul în `bot.db`:

```json
food.recommendation.ready   {"user_id": 3, "date": "2026-11-02", "picks": [...]}
food.rating.requested       {"user_id": 3, "order_id": 41}
food.alert                  {"service": "core", "level": "error", "text": "...", "user_id": 3}   // user_id opțional; merge la BOT_ADMIN_CHAT_ID
```

## Cum vin mesajele de la Telegram

**Webhook (implicit, `BOT_MODE=webhook`).** La pornire botul îi spune lui Telegram (`setWebhook`): „trimite-mi totul la `BOT_WEBHOOK_URL`, cu antetul secret `BOT_WEBHOOK_SECRET`”. De atunci, la fiecare mesaj sau buton apăsat, **Telegram face `POST`** la adresa asta. Botul verifică antetul `X-Telegram-Bot-Api-Secret-Token` (altfel 401) și dă update-ul handler-ilor. Dacă botul e oprit, Telegram păstrează update-urile până la 24 h și le trimite când revine.

```
Telegram ──HTTPS POST──▶ ajl.duckdns.org:443 ──▶ reverse proxy ──HTTP──▶ bot:8086/tg/webhook
```

Ce trebuie pe server:
- un domeniu public cu **HTTPS valid** (Let's Encrypt e ok) pe portul **443, 80, 88 sau 8443** — Telegram nu acceptă alte porturi;
- portul deschis în router spre server;
- un reverse proxy care trimite **doar** calea webhook-ului la bot (nu și `/healthz`). Exemplu Caddy:

```
ajl.duckdns.org {
    handle /tg/webhook {
        reverse_proxy bot:8086
    }
}
```

nginx: `location = /tg/webhook { proxy_pass http://bot:8086; }`.

Secretul: `openssl rand -hex 32`. Verificare din partea Telegram: `https://api.telegram.org/bot<TOKEN>/getWebhookInfo` (arată URL-ul, câte update-uri așteaptă și ultima eroare).

**Polling (`BOT_MODE=polling`), pentru rulare locală.** Botul întreabă singur Telegram (`getUpdates`) în buclă; nu are nevoie de adresă publică. La pornire **șterge webhook-ul** (Telegram nu permite ambele deodată), deci după ce rulezi local, pornește din nou botul de pe server ca să-l reînregistreze.

## Comenzi

| Comandă | Ce face | Apel |
|---|---|---|
| `/start` | conectare (sau revenire după `/stop`) | `bot.db` |
| `/setari` | setările cu butoane; `/setari ora 16:30`, `/setari zile luni-vineri`, `/setari fus …` | `bot.db` |
| `/profil` | nume, nickname, telefon, id; butonul pentru telefon | `bot.db` |
| `/stop` | oprește recomandările automate | `bot.db` |
| `/meniu` | cere recomandarea pentru următoarea zi de comandă; rezultatul vine ca eveniment | `POST /v1/recommendations` |
| `/alege` (`/alege azi`) | arată ultima recomandare | `GET /v1/recommendations?date=` |
| `/nota` | ultima masă nenotată (14 zile) → stele → comentariu | `GET /v1/orders`, `POST /v1/orders/{id}/rating` |
| `/istoric` (`/istoric 60`) | ultimele alegeri cu note | `GET /v1/orders?from=&to=` |
| `/preferinte` | listă; `add alergen lapte`, `add nu! ciuperci`, `add nu prăjit`, `add place supă`, `add dieta …`, `del 3` | `GET`/`PUT /v1/prefs` |
| `/stat` (`/stat 7`) | luna curentă sau ultimele N zile | `GET /v1/stats?from=&to=` |

Datele (azi, mâine, luna) se calculează în fusul orar al fiecărui utilizator.

Butoane la recomandare: câte unul per fel (`POST /v1/orders`), „Nimic nu mi-a plăcut, aleg din lipsă" (comută butoanele pe `forced=true`), iar la felurile cu alergeni posibili „da, conține / nu conține" (`POST /v1/dishes/{id}/allergens/confirm` la `catalog`).

## Starea mesajelor (fără bază)

- **Ce înseamnă un buton** e scris în `callback_data` (max 64 bytes): `o:<data>:<dish_id>:<forced>:<preț>`, `f:<data>:<0|1>`, `r:<order_id>:<nota>`, `n:<order_id>`, `a:<data>:<dish_id>:<y|n>`, `d:<1..7>`, `t:<±min>`. Utilizatorul vine din chat, nu din buton. Vezi `internal/tg/callback.go`.
- **Comutarea „din lipsă"** reconstruiește tastatura din butoanele existente ale mesajului.
- **Buton vechi**: dacă data felului e în trecut, botul spune că recomandarea a expirat și scoate butoanele.
- **În memorie**, pe utilizator (se pierd la repornire, fără pagube): comentariul așteptat după stele (30 min), felurile ultimelor recomandări (pentru alergeni; la nevoie le cere din nou de la `core`), id-urile evenimentelor deja procesate.

## Evenimente (NATS JetStream)

Consumator durabil `bot` pe stream-ul `FOOD`, filtrat pe `food.recommendation.ready`, `food.rating.requested`, `food.alert`. Confirmare explicită; la eroare temporară (ex. Telegram nu răspunde) mesajul se retrimite cu pauze 5s → 30s → 2m → 5m → 10m; JSON invalid, tip necunoscut sau `user_id` inexistent → aruncat (`Term`); utilizator oprit → confirmat și sărit. Botul **nu creează** stream-ul: dacă lipsește, așteaptă și reîncearcă la 10 s. Botul nu publică evenimente.

## Ce am presupus (nu e fixat în notițe)

Tipurile sunt în `internal/contracts`. Câmpurile de mai jos le-am ales eu — de aliniat când scrii `core`:

- Utilizatorul vine în antetul `X-User-ID`; `core` răspunde 400 dacă lipsește.
- `GET /v1/recommendations` întoarce `{date, status, picks[]}`, 404 dacă nu există.
- `POST /v1/orders` întoarce comanda salvată (`{id, dish_name, …}`).
- `GET /v1/orders` întoarce o listă de comenzi; `PUT /v1/prefs` primește **toată lista** `{items: [{kind, value, hard}]}`.
- `GET /v1/stats` întoarce `{orders, spent, forced, avg_rating, kcal{min,max}, protein_g, fat_g, carbs_g, without_nutrition, top_dishes[]}`.
- Opțional în `picks`: `kcal_min`, `kcal_max`, `allergens_possible[]` (dacă `core` le pune, botul le afișează).
- Confirmarea alergenilor trimite `{allergens: [...]}`; lista goală = „nu conține".
- Portul botului: `:8086` (`/healthz` și calea webhook-ului; `llm` are deja 8084 în arhitectură).

## Securitate

- Webhook-ul acceptă doar `POST` cu antetul secret corect (comparat în timp constant); corp maxim 1 MB.
- `BOT_ALLOWED_CHAT_IDS` gol = oricine găsește botul se poate conecta; pune id-urile ca să-l ții privat.
- Telefonul se salvează doar dacă utilizatorul îl trimite singur, și doar dacă e al lui.
- `bot.db` are date personale (nume, nickname, telefon): `data/` e în `.gitignore`; fă backup ca la `core.db`.
- Tokenul și secretul doar în `.env` / variabile de mediu; `.env` e în `.gitignore`, **`.env.example` nu** — acolo rămân doar valori goale.
