# catalog — food-order

Serviciul pentru **tot ce ține de mâncare**: feluri, meniul fiecărei zile, gramaj, componente, metodă de preparare, valori nutriționale **pe porție** (fiecare cu `min` și `max`, plus sursa lor), alergeni, etichete, embeddings. Nu știe nimic despre utilizatori (doar id-ul celui care a confirmat alergenii).

`catalog` **nu calculează** nutriția: o primește de la sursă (bucătărie) sau de la `llm` și o salvează, cu sursa (`kitchen` / `llm` / `missing`).

Notițe: Obsidian `projects/food-order/Catalog`, `Comunicare`, `Arhitectura`, `Modele`.

## Pornire

```bash
go mod tidy              # prima dată (go.sum e copiat din core, tidy îl completează dacă lipsește ceva)
go test ./...
cp .env.example .env     # opțional: toate valorile au implicit
go run .                 # sau: go run . -env alt.env
```

Baza `data/catalog.db` se creează singură; migrările rulează la pornire (`schema_version`). Driver SQLite: `github.com/ncruces/go-sqlite3` (Go pur, fără CGO), ca în `core` și `bot`.

**Stream-ul `FOOD`** îl creează `core`; `catalog` îl așteaptă (în log: „stream not found yet”) și pornește singur când apare. Consumatorul durabil se numește `catalog` și ia **toate** mesajele din stream, deci un meniu trimis cât `catalog` era oprit nu se pierde.

Fără NATS: `CATALOG_NATS_URL=` (gol) → doar HTTP; `llm` nu e întrebat, meniul se încarcă doar prin `POST /v1/import`.

## Ce se întâmplă cu un meniu

1. Vine `food.menu.fetched` `{request_id, source, days[{date, items[]}]}` (sau `POST /v1/import` cu același corp).
2. Fiecare fel e găsit sau creat în `dishes`: după `external_id` dacă sursa îl dă, altfel după `name_norm` (litere mici, spații unice, ş→ș, ţ→ț, ghilimele unificate).
3. Se salvează `menu_items` cu ce a dat sursa: preț, categorie, gramaj, componente, nutriție, alergeni. Componentele pot veni ca text (`"Supă de linte"`, cum le dă parserul) sau ca obiect `{name, grams, method}`.
4. Pentru **ce lipsește** pleacă `food.dish.analyze.requested` cu `need` ⊆ `components`, `nutrition`, `allergens`, `portion`, `tags`. Ce a dat sursa nu se mai cere; ce e deja `known` se trimite ca context.
5. `food.dish.analyzed` → se salvează cu sursa `llm`, fără să suprascrie ce a dat sursa. Răspunsul pentru un fel care între timp a dispărut (revizie nouă) e confirmat și ignorat.
6. Când toate felurile zilelor din cerere au ce trebuie → `food.menu.ready` `{request_id, from, to, dish_ids}` către `core`, o singură dată.
7. Dacă `llm` nu răspunde în `CATALOG_ANALYSIS_TIMEOUT` (30m) → `food.alert` cu felurile, iar meniul se marchează gata cu ce există (`nutrition_source = missing`). Un răspuns întârziat tot se salvează.
8. După analiză (sau dacă nu era nevoie de ea) pleacă `food.dish.embed.requested` `{dish_id, text}` pentru felurile fără embedding. Textul: nume, categorie, descriere, componente (cu metoda), etichete. `food.menu.ready` **nu** așteaptă embeddings (`core` folosește similitudinea doar dacă există).

Tot ce trebuie trimis (cereri la `llm`, `food.menu.ready`) se trimite după fiecare eveniment și la fiecare `CATALOG_TICK`, deci dacă brokerul a căzut, se retrimite când revine.

## API (HTTP :9082, intern)

Erori: `{"error": {"code", "message"}}`, `message` în română.

| Cerere | Răspuns |
|---|---|
| `GET /healthz` | `{"status":"ok"}` (verifică și baza) |
| `GET /v1/menu?date=` | `{date, items:[Dish]}` în ordinea de pe site; fără `date` = mâine (`CATALOG_TZ`); zi fără meniu = `items: []` |
| `GET /v1/dishes?ids=12,15,18&date=` | `{items:[Dish]}` cu valorile din meniul zilei `date`; fără `date` = ultima zi în care a fost felul în meniu. Id-urile necunoscute (sau care nu sunt în meniul zilei) lipsesc din listă |
| `GET /v1/dishes/{id}?date=` | `Dish`; `404 not_found` |
| `POST /v1/similar` `{liked_ids, candidate_ids}` | `{scores:[{id, score 0..1, like_id}]}` — cea mai mare similitudine cosinus cu felurile plăcute (același model); un fel nu se compară cu el însuși; candidații fără embedding lipsesc |
| `POST /v1/dishes/{id}/allergens/confirm` `{allergens[]}` + `X-User-ID` | `Dish`; listă goală = „confirmat: fără alergeni”; fără antet → `400 missing_user` |
| `POST /v1/import` | același corp ca `data` din `food.menu.fetched` → `{request_id, days:[{date, revision, items, skipped, changed}]}`. Doar pentru depanare (`CATALOG_IMPORT_ENABLED`) |

`Dish` (citit de `core` ca `CatalogDish`):

```json
{"id": 2, "menu_item_id": 2, "date": "2026-11-02", "name": "LUNCH 1 Maxi", "category": "Lunch Box", "price": 75,
 "weight_g": 380, "weight_text": "", "weight_source": "llm",
 "nutrition": {"kcal": {"min": 761, "max": 911}, "protein_g": {"min": 20, "max": 30}, "fat_g": {...}, "carbs_g": {...},
               "saturated_fat_g": {...}, "sugars_g": {...}, "fiber_g": {...}, "salt_g": {...}},
 "nutrition_source": "llm",
 "components": [{"name": "Supă de linte", "source": "site"}, {"name": "piept de pui", "grams": 120, "method": "boiled", "source": "llm"}],
 "allergens": {"given": [], "confirmed": ["gluten"], "possible": ["gluten", "țelină"]},
 "tags": ["cald"], "status": "confirmed", "analysis": "done", "source": "parser", "revision": 1}
```

`nutrition` lipsește când `nutrition_source = missing`. `allergens.confirmed` lipsește = nimeni n-a confirmat; `[]` = confirmat „fără”. `analysis`: `none` (nu lipsea nimic), `pending` (cererea n-a plecat încă), `requested`, `done`, `timeout`, `off` (analiza oprită).

Forma e exact cea presupusă în `core/internal/contracts` (`CatalogDish`, `{"items": [...]}`, `{"scores": [...]}`), deci `core` nu are nevoie de schimbări. Am verificat cap-coadă: `core` → `food.menu.requested` → parser simulat → `catalog` → llm simulat → `food.menu.ready` → recomandare și comandă cu snapshot în `core`.

## Evenimente (NATS JetStream)

Ascultă: `food.menu.fetched`, `food.dish.analyzed`, `food.dish.embedded`.
Publică: `food.dish.analyze.requested`, `food.dish.embed.requested`, `food.menu.ready`, `food.alert`.

Plicul comun `{id, type, version, ts, data}`; `id` e ULID și e și `Nats-Msg-Id`. Idempotență: `processed_events` (păstrate `CATALOG_PROCESSED_KEEP`). Eveniment stricat / tip necunoscut / date invalide → aruncat (`Term`); eroare temporară (baza) → reîncercat cu pauză (5s, 30s, 2m, 5m, 10m), apoi `food.alert`.

Ce acceptă `food.menu.fetched` (tolerant, ca să meargă și cu parserul de acum, și cu API-ul de mai târziu):

```json
{"request_id": "01J...", "source": "parser", "revision": 0,
 "days": [{"date": "2026-11-02", "revision": 0, "items": [
   {"external_id": "k-17", "name": "Ciorbă de pui", "category": "Supe", "price": 35,
    "weight_g": 300, "weight_text": "250/50",          // sau "weight": "250/50", cum îl numește parserul
    "description": "...", "image_url": "...",
    "components": ["Supă de linte", {"name": "morcov", "grams": 40, "method": "boiled"}],
    "nutrition": {"per": "100g", "kcal": 40, "protein_g": {"min": 3, "max": 4}},   // fără "per" = pe porție
    "allergens": ["gluten"]}                            // lipsă = nu știe; [] = fără alergeni
 ]}]}
```

Valorile nutriționale pot fi număr (exact: `min = max`) sau `{min, max}`. La `food.dish.analyzed` la fel (și `[min, max]`).

## Decizii pentru punctele „De completat” din notiță

Le-am ales eu ca să meargă; schimbă-le dacă vrei altfel:

- **Același fel scris puțin diferit fără `external_id`**: doar `name_norm` (vezi mai sus). Potrivirea pe embeddings e riscantă pentru meniuri de tip „LUNCH 1 / LUNCH 2”, unde numele aproape identice înseamnă feluri diferite.
- **Reanalizarea**: un fel se analizează din nou doar când se schimbă ce vede modelul (nume, descriere, gramaj, componente, poză) — `menu_items.input_hash`. Același fel cu același input în altă zi **preia** analiza (componente, porție, nutriție de la `llm`) fără cerere nouă; același fel în mai multe zile ale aceleiași cereri = **o singură** cerere. Alergenii și etichetele sunt per fel (`dish_info`), restul per zi. Model nou → încă nu (de făcut: un endpoint care pune `analysis = none` și retrimite).
- **Revizii**: dacă sursa nu dă `revision`, `catalog` le numără singur: același conținut = aceeași revizie, altfel +1. O revizie explicită mai mică decât cea salvată e ignorată. Revizia nouă înlocuiește ziua: felurile dispărute se șterg din ziua aceea, cele rămase își păstrează estimările dacă inputul nu s-a schimbat. **O zi fără feluri nu șterge meniul** (e tratată ca „nu există meniu” și sărită).
- **Gramajul din text**: dacă lipsește `weight_g`, se citește din `weight_text` / `weight` („250/50” = 300 g, „0,5 l” = 500, „150+30 гр” = 180); altfel îl estimează `llm` (`weight_source = llm`).
- **Nutriție „la 100 g”**: se înmulțește cu `weight_g / 100`; dacă gramajul lipsește, se cere `portion` de la `llm` și se înmulțește după (sursa rămâne `kitchen`, originalul în `nutrition_raw`).
- **Corectarea manuală** a gramajului sau a componentelor: încă nu există (coloana `weight_source = me` e pregătită).
- **Pozele**: doar URL-ul.
- **Meniu fără `request_id`** (de ex. API-ul de mai târziu, nechemat de `core`): primește un id `catalog-<ULID>`, ca `core` să afle totuși că meniul e gata.

Coloane în plus față de schema din notiță (marcate `+` în migrare): `dishes.embed_requested_at`, `created_at`; `menu_items.position`, `source_hash`, `input_hash`, `analysis`, `need`, `analysis_requested_at`, `analysis_done_at`; `dish_info.confirmed_by`, `confirmed_at`; `embeddings.dims`, `created_at`; tabelul `menu_requests` (cererile lui `core`, ca să știu când trimit `food.menu.ready`).

## Structură

```
main.go                 # pornire: config, baza, NATS (consumator), tick, HTTP
internal/config/        # Config, Load, .env, validare (un test verifică .env.example = implicitele)
internal/contracts/     # tipuri HTTP + evenimente (Range tolerant, Component din text sau obiect)
internal/food/          # NormName, NormList, ParseWeight, ParseNutrition, hash-uri
internal/store/         # catalog.db: import pe zile + revizii, stare analiză, cereri, embeddings, citiri
internal/service/       # logica: Import, OnAnalyzed, OnEmbedded, Flush/Tick, Menu, Dishes, Similar, ConfirmAllergens
internal/events/        # evenimente NATS -> service (idempotent)
internal/bus/           # NATS JetStream: publish, consumator durabil (așteaptă stream-ul), ULID
internal/api/           # HTTP + teste
```

## De făcut în celelalte servicii

- **parser**: legat la NATS (ascultă `food.menu.requested`, publică `food.menu.fetched` / `food.menu.error`). `POST /v1/import` rămâne pentru depanare.
- **llm**: să asculte `food.dish.analyze.requested` / `food.dish.embed.requested` și să răspundă cu `food.dish.analyzed` (doar câmpurile din `need`, `menu_item_id` repetat) și `food.dish.embedded`.
- **parser**: e pe `:9083` (implicit și în `.env`), deci nu se mai ciocnește cu `catalog`.
