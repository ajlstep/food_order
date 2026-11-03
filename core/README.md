# core — food-order

Serviciul de **business logic**. Pe `user_id` (id-ul din `users`, text) ține comenzile cu snapshot-ul felului și adresa unde merg, notele, comentariile și etichetele; preferințele și alergenii îi cere de la `users`; cere meniul (unul pentru toți), aplică filtrele dure, calculează scorul, orchestrează recomandarea (`catalog` → scor → `llm` → `bot`) și face statistica. Nu are tabel de utilizatori și nu vede date personale.

Notițe: Obsidian `projects/food-order/Core`, `Comunicare`, `Arhitectura`, `etape/E7-E8-Core-Catalog`.

## Pornire

```bash
go test ./...
cp .env.example .env     # sau ../init-env.sh
go run .                 # sau: go run . -env alt.env
```

Vorbește doar prin NATS, nu are port. Cine lucrează cu el trece prin `gateway`. Nu pornește fără NATS. Serviciul `llm` nu există încă, deci implicit `CORE_LLM_ENABLED=false`: recomandarea vine din scor, imediat.

Baza `data/core.db` se creează singură; migrările rulează la pornire (`schema_version`). Stream-urile și consumatorul `core` le creează `nats-setup`.

## Cereri (NATS, `rpc.core.*`) — vin de la `gateway`, cu persoana în plic

Erorile sunt refuzuri cu cod stabil; textul pentru om e în `details.text`.

| Subiect | Ce face |
|---|---|
| `recommendations.create` `{date}` | Pornește recomandarea: `waiting_menu`, `generating` sau `ready`. Rezultatul vine ca `food.recommendation.ready`. |
| `recommendations.get` `{date}` | Ultima recomandare a zilei; `not_found`. |
| `orders.create` `{date, dish_id, price?, forced, personal}` | Comanda, cu snapshot de la `catalog` și adresa grupului (dacă nu e `personal`). O comandă pe zi: a doua o înlocuiește pe prima. După ora limită a partenerului: `order_closed`. |
| `orders.rate` `{order_id, rating, comment}` | Nota; cu comentariu pleacă `food.feedback.parse.requested`. |
| `orders.list` `{from, to}` | Comenzile, cele vechi întâi. |
| `stats` `{from, to}` | Cheltuieli, „din lipsă”, nota medie, kcal și macro ca interval. |
| `reports.company` `{from, to}` | Cine, când și ce a comandat la adresele companiei. Doar proprietarul companiei (întreabă `users`). Comenzile personale nu apar. |

`core` întreabă singur: `rpc.catalog.menu.get`, `dishes.get`, `similar` și `rpc.users.profile.food.get`, `profile.due`, `org.snapshot`, `org.company.get`. În `nuts/nats.conf` are drept doar la acestea.

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

- la fiecare tick întreabă `users` cine are acum ora de comandă (`rpc.users.profile.due`, în fereastra `CORE_ORDER_WINDOW`) și pornește recomandarea pentru cine nu o are deja

## Recomandarea

1. `rpc.catalog.menu.get` → felurile zilei; ce nu se mai poate comanda (partener închis, a trecut ora limită) iese din listă
2. **filtre dure**: alergeni (sursa > confirmați > ghiciți de model; plus cuvinte-cheie ro/ru pe nume și componente: „lapte" prinde și smântână, brânză, сметана…), `dislike` dur, `diet` dur (felul trebuie să o conțină în nume/componente/etichete)
3. **scor** (`internal/rules`), ponderi în `CORE_SCORE_WEIGHTS`: nota (medie bayesiană), frecvența alegerilor „ne-din-lipsă" (relativ la alternativele zilei), similitudinea (`rpc.catalog.similar`, dacă merge), `like`/`diet` moale, ținta de calorii, noutate; minus `dislike` moale + etichete de plângere pe comenzile trecute, minus repetiția din ultimele `CORE_SCORE_REPEAT_DAYS` zile
4. primele `CORE_REC_CANDIDATES` → `food.recommendation.requested` (fără date personale, context scurt: ultimele 7 zile, feluri plăcute/neplăcute, preferințe moi, suma lunii)
5. `food.recommendation.generated` → se păstrează doar id-urile din listă, fără dubluri, maxim `CORE_REC_PICKS`; nimic valid → din scor
6. `food.recommendation.ready` → `bot`. `allergens_possible` = doar ghicirile modelului neconfirmate (botul întreabă „conține X?")

## Decizii pentru punctele „De completat" din notiță

Le-am ales eu ca să meargă; schimbă-le dacă vrei altfel:

- **Ținta de calorii**: ca preferință în `users` (`kind: target`, `value: kcal=700`, și `protein_g`, `fat_g`, `carbs_g`); se compară mijlocul intervalului (`CORE_SCORE_TARGET_MODE=mid`) sau capătul mai rău (`worst`). Felurile fără valori primesc 0,5 (neutru). Botul nu are încă o comandă pentru ea.
- **`llm` nu răspunde la timp**: recomandare din scor, fără motiv (`CORE_LLM_TIMEOUT`).
- **Revizie nouă de meniu** pentru o zi: oprit implicit (`CORE_REC_REGENERATE_ON_REVISION=false`); pornit → recomandare nouă pentru cine nu a ales încă.
- **O comandă pe zi** de utilizator; schimbarea alegerii merge până la notă.
- **Ștergerea datelor unui utilizator**: nu e făcută încă.

## Structură

```
main.go                 # pornire: config, baza, NATS, programator
internal/config/        # Config, Load, .env, validare (un test verifică .env.example = implicitele)
internal/store/         # core.db: orders, recommendations, menu_requests, processed_events
internal/remote/        # cum întreabă core alte servicii, prin NATS
internal/rules/         # filtrele dure și scorul (determinist)
internal/service/       # logica: meniu, recomandare, comenzi, statistică, raport, tick
internal/events/        # evenimente NATS -> service (idempotent)
internal/scheduler/     # bucla programatorului
internal/api/           # rpc.core.* + teste cap-coadă (catalog fals, broker fals, ceas mutat de test)
```

Coloane în plus față de schema din notiță (pentru programator): `orders.kitchen_feedback`, `rating_requested_at`, `rated_at`, `created_at`, `updated_at`; `recommendations.candidates`, `updated_at`, `notified_at`; `menu_requests.next_retry_at`, `alerted`.

## De făcut în celelalte servicii

- **llm**: nu există. Când apare: are nevoie de un utilizator în `nuts/nats.conf` și de `CORE_LLM_ENABLED=true`.
