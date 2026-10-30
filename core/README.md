# core — food-order

Serviciul de **business logic**. Pe `user_id` (= `users.id` din `bot.db`) ține comenzile cu snapshot-ul felului, notele, comentariile, etichetele și preferințele de mâncare; cere meniul (unul pentru toți), aplică filtrele dure, calculează scorul, orchestrează recomandarea (`catalog` → scor → `llm` → `bot`) și face statistica. Nu are tabel de utilizatori și nu vede date personale.

Notițe: Obsidian `projects/food-order/Core`, `Comunicare`, `Arhitectura`.

## Pornire

```bash
go mod tidy              # prima dată (go.sum e copiat din bot, dar tidy îl completează dacă lipsește ceva)
go test ./...
cp .env.example .env     # opțional: toate valorile au implicit
go run .                 # sau: go run . -env alt.env ; -debug arată de ce a fost exclus un fel
```

Fără NATS: `CORE_NATS_URL=` (gol) → doar HTTP; meniul nu se cere, iar recomandarea se face doar din scor, fără motiv. Fără `llm` (încă nu există): `CORE_LLM_ENABLED=false` → recomandarea din scor, imediat.

Baza `data/core.db` se creează singură; migrările rulează la pornire (`schema_version`). Driver SQLite: `github.com/ncruces/go-sqlite3` (Go pur, fără CGO), ca în `bot`.

**Stream-ul `FOOD`** (`food.>`) îl creează `core` la pornire — `bot` îl așteaptă. Consumatorul durabil al lui `core` se numește `core`.

## API (HTTP :8081) — apelat de `bot`, cu `X-User-ID`

Fără `X-User-ID` → `400 missing_user`. Erori: `{"error": {"code", "message"}}`; `message` e în română (botul o arată utilizatorului).

| Cerere | Răspuns |
|---|---|
| `GET /healthz` | `{"status":"ok"}` (verifică și baza) |
| `POST /v1/recommendations` `{date}` | `202 {date, status, picks[]}` — `waiting_menu` dacă meniul nu e gata (și `core` îl cere), `generating`, sau `ready`. Rezultatul vine ca `food.recommendation.ready`. Dacă una e deja în lucru pentru ziua asta, o întoarce pe aceea (nu dublează). |
| `GET /v1/recommendations?date=` | ultima recomandare a zilei; `404 not_found` |
| `POST /v1/orders` `{date, dish_id, price?, forced}` | comanda salvată, cu snapshot de la `catalog`. **O comandă pe zi**: a doua alegere o înlocuiește pe prima; după notă → `409 already_rated`. `catalog` căzut → snapshot din candidații salvați la recomandare, altfel `503 catalog_unavailable`; fel necunoscut → `422 dish_not_in_menu` |
| `POST /v1/orders/{id}/rating` `{rating 1-5, comment}` | comanda; cu comentariu → `food.feedback.parse.requested` |
| `GET /v1/orders?from=&to=` | listă, cele vechi întâi (implicit ultimele 30 de zile) |
| `GET /v1/prefs` · `PUT /v1/prefs` | `{items:[{id, kind, value, hard}]}`; PUT înlocuiește toată lista. `kind` ∈ `allergen` (mereu dur), `dislike`, `like`, `diet`, `target` |
| `GET /v1/stats?from=&to=` | cheltuieli, „din lipsă" (nr. și %), nota medie, kcal/proteine/grăsimi/glucide ca interval (min și max adunate separat), `without_nutrition`, `top_dishes`, `days[]`, `months[]` |

## Evenimente (NATS JetStream)

Ascultă: `food.menu.ready`, `food.menu.error`, `food.recommendation.generated`, `food.feedback.parsed`.
Publică: `food.menu.requested`, `food.recommendation.requested`, `food.recommendation.ready` (`user_id`), `food.rating.requested` (`user_id`), `food.feedback.parse.requested`, `food.alert`.

Plicul comun `{id, type, version, ts, data}`; `id` e ULID și e și `Nats-Msg-Id` (dedup în stream). Idempotență: `processed_events`. Eveniment stricat / tip necunoscut → aruncat (`Term`); eroare temporară → reîncercat cu pauză (5s, 30s, 2m, 5m, 10m), apoi `food.alert`. Răspunsuri la cereri pe care `core` nu le-a făcut → confirmate și ignorate.

## Programatorul (`internal/scheduler`, la fiecare `CORE_TICK`)

- de la `CORE_MENU_REQUEST_TIME` (15:00) cere o dată meniul **următoarei zile de servire** (`CORE_MENU_DAYS`, implicit luni–vineri: vineri → luni)
- `no_menu` → reîncearcă la `CORE_MENU_RETRY_INTERVAL` până la `CORE_MENU_DEADLINE` (20:00) din ziua dinaintea meniului, apoi o alertă și se oprește
- `source_unavailable` sau niciun răspuns în `CORE_MENU_TIMEOUT` → pauză crescătoare (1m, 2m, 4m… max 30m), o alertă după `CORE_MENU_MAX_ATTEMPTS` trimiteri, până la ora limită
- `parse_failed` / `bad_request` → alertă imediat, fără reîncercări
- se retrimite cu **același** `request_id`, deci un răspuns întârziat tot contează
- după o cerere eșuată definitiv, programatorul nu mai cere singur ziua aceea, dar o cerere de recomandare a unui utilizator o cere din nou
- recomandările care așteaptă deși meniul e gata (eveniment pierdut, `catalog` căzut) se generează la tick
- `llm` nu răspunde în `CORE_LLM_TIMEOUT` → alegerile din scor, fără motiv
- `food.recommendation.ready` nepublicat (broker căzut) → republicat
- de la `CORE_RATING_TIME` (10:00) → `food.rating.requested` pentru mesele nenotate din ultimele `CORE_RATING_LOOKBACK_DAYS` zile, o dată per comandă

Ora recomandării **nu** e aici: botul apelează `POST /v1/recommendations` la ora de comandă a fiecăruia.

## Recomandarea

1. `GET /v1/menu?date=` la `catalog` → felurile zilei
2. **filtre dure**: alergeni (sursa > confirmați > ghiciți de model; plus cuvinte-cheie ro/ru pe nume și componente: „lapte" prinde și smântână, brânză, сметана…), `dislike` dur, `diet` dur (felul trebuie să o conțină în nume/componente/etichete)
3. **scor** (`internal/rules`), ponderi în `CORE_SCORE_WEIGHTS`: nota (medie bayesiană), frecvența alegerilor „ne-din-lipsă" (relativ la alternativele zilei), similitudinea (`POST /v1/similar`, dacă merge), `like`/`diet` moale, ținta de calorii, noutate; minus `dislike` moale + etichete de plângere pe comenzile trecute, minus repetiția din ultimele `CORE_SCORE_REPEAT_DAYS` zile
4. primele `CORE_REC_CANDIDATES` → `food.recommendation.requested` (fără date personale, context scurt: ultimele 7 zile, feluri plăcute/neplăcute, preferințe moi, suma lunii)
5. `food.recommendation.generated` → se păstrează doar id-urile din listă, fără dubluri, maxim `CORE_REC_PICKS`; nimic valid → din scor
6. `food.recommendation.ready` → `bot`. `allergens_possible` = doar ghicirile modelului neconfirmate (botul întreabă „conține X?")

## Ce am presupus (de confirmat când scrii `catalog`)

`catalog` nu există încă, deci `core` citește tolerant (`{"items": [...]}` sau o listă simplă):

```json
GET /v1/menu?date=2026-11-02   ->  {"date": "...", "items": [
  {"id": 12, "name": "LUNCH 1 Stand", "category": "Lunch Box", "price": 63,
   "weight_g": 350, "weight_text": "250/50", "weight_source": "site",
   "nutrition": {"kcal": {"min": 600, "max": 800}, "protein_g": {...}, "fat_g": {...}, "carbs_g": {...}},
   "nutrition_source": "llm",
   "components": [{"name": "piept de pui", "grams": 120, "method": "boiled"}],
   "allergens": {"given": [], "confirmed": null, "possible": ["gluten"]},
   "tags": ["supă", "cald"]}
]}
GET /v1/dishes?ids=12,15&date=   ->  {"items": [ ...la fel... ]}
POST /v1/similar {liked_ids, candidate_ids}  ->  {"scores": [{"id": 15, "score": 0.83}]}
```

Tipurile sunt în `internal/contracts` (`CatalogDish` etc.).

## Decizii pentru punctele „De completat" din notiță

Le-am ales eu ca să meargă; schimbă-le dacă vrei altfel:

- **Ținta de calorii**: deocamdată ca preferință în `core` (`kind: target`, `value: kcal=700`, și `protein_g`, `fat_g`, `carbs_g`); se compară mijlocul intervalului (`CORE_SCORE_TARGET_MODE=mid`) sau capătul mai rău (`worst`). Felurile fără valori primesc 0,5 (neutru). Botul nu are încă o comandă pentru ea.
- **`llm` nu răspunde la timp**: recomandare din scor, fără motiv (`CORE_LLM_TIMEOUT`).
- **Revizie nouă de meniu** pentru o zi: oprit implicit (`CORE_REC_REGENERATE_ON_REVISION=false`); pornit → recomandare nouă pentru cine nu a ales încă.
- **O comandă pe zi** de utilizator; schimbarea alegerii merge până la notă.
- **Ștergerea datelor unui utilizator**: nu e făcută încă.

## Structură

```
main.go                 # pornire: config, baza, NATS (stream + consumator), programator, HTTP
internal/config/        # Config, Load, .env, validare (un test verifică .env.example = implicitele)
internal/contracts/     # tipuri HTTP + evenimente + forma presupusă a lui catalog
internal/store/         # core.db: orders, prefs, recommendations, menu_requests, processed_events
internal/catalog/       # client HTTP spre catalog
internal/rules/         # filtrele dure și scorul (determinist)
internal/service/       # logica: meniu, recomandare, comenzi, preferințe, statistică, tick
internal/events/        # evenimente NATS -> service (idempotent)
internal/bus/           # NATS JetStream: stream, publish, consumator durabil, ULID
internal/scheduler/     # bucla programatorului
internal/api/           # HTTP + teste cap-coadă (catalog fals, broker fals, ceas mutat de test)
```

Coloane în plus față de schema din notiță (pentru programator): `orders.kitchen_feedback`, `rating_requested_at`, `rated_at`, `created_at`, `updated_at`; `recommendations.candidates`, `updated_at`, `notified_at`; `menu_requests.next_retry_at`, `alerted`.

## De făcut în celelalte servicii

- **parser**: ascultă `food.menu.requested`, trimite `food.menu.fetched` (la `catalog`) sau `food.menu.error` (la `core`) cu codurile `no_menu` / `source_unavailable` / `parse_failed` / `bad_request`. README-ul parserului încă pomenește `food.menu.fetch.requested` / `food.menu.empty` — numele corecte sunt cele din notița `Comunicare`.
- **parser** are implicit portul `:8082`, același cu `catalog`; în notițe parserul e pe `8083`.
