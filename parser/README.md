# parser (food-order)

Serviciu fără stare: aduce meniul de pe site și îl dă mai departe ca listă de feluri.
Nu scrie în nicio bază de date. Specificația: Obsidian → `projects/food-order/Parser`, `Comunicare`.

**Cine îl cheamă:** `core`, prin NATS (fluxul normal). HTTP-ul (`/v1/fetch`) e doar pentru depanare, de mână.

```
core ──food.menu.requested {request_id, from, to?}──► parser
parser ──food.menu.fetched {request_id, source, days[...]}──► catalog
parser ──food.menu.error {request_id, code, message}──► core
```

## Pornire

```
go mod tidy                      # prima dată (și după ce s-a adăugat nats.go): completează go.sum
go test ./...
cp .env.example .env             # opțional: toate valorile au implicit; schimbi doar ce ai nevoie
go run .                         # citește https://bistro.md/ro/catalog?date={date}
curl "http://localhost:8083/v1/fetch?date=2026-10-28"   # depanare
```

`{date}` din URL se înlocuiește cu data cerută (`YYYY-MM-DD`). Fără `date` în cerere se ia **mâine** (fusul `PARSER_TZ`).

## NATS (fluxul normal)

Ascultă `food.menu.requested` `{request_id, from, to?}` cu consumatorul durabil `parser` pe stream-ul `FOOD`.
Stream-ul îl creează `core`; parserul îl așteaptă (în log: „stream not found yet”) și pornește singur când apare.

- **Succes** → `food.menu.fetched` către `catalog`: `{request_id, source: "parser", days:[{date, items:[{name, category, price?, weight_text?, image_url?, components[]}]}]}`.
  Parserul nu normalizează numele: asta face `catalog`. Prețul 0 (lipsă) nu se trimite.
- **Eroare** → `food.menu.error` către `core`, cu codul:

| Cod | Când |
|---|---|
| `no_menu` | pagina se încarcă, dar nu are carduri (meniu nepublicat) |
| `source_unavailable` | site-ul nu răspunde, răspunde cu eroare, timeout, sau orice altă eroare necunoscută |
| `parse_failed` | sunt carduri, dar fără feluri în ele (site schimbat) |
| `bad_request` | `from`/`to` nu sunt date `AAAA-LL-ZZ`, `to` < `from`, sau perioada are peste 14 zile |

- **O perioadă** (`to`) se cere zi cu zi. Zilele fără meniu (de ex. weekend) lipsesc din `days`; dacă nicio zi nu are meniu → `no_menu`.
- **Reîncercările le face `core`**, nu parserul. Parserul doar protejează site-ul: un meniu găsit e ținut `PARSER_CACHE_TTL`, iar aceeași zi nu se cere mai des de `PARSER_MIN_INTERVAL`. Dacă `core` întreabă mai repede, primește din nou **ultimul răspuns** (de ex. tot `no_menu`), fără o nouă cerere la site.
- **Dubluri**: id-urile evenimentelor se țin minte o oră, în memorie (parserul n-are bază). O dublură după repornire doar retrimite același meniu, pe care `catalog` îl ignoră.
- **Prima pornire**: consumatorul ia și cererile din ultima oră (dacă `core` a cerut înainte ca parserul să pornească), nu toată săptămâna din stream. După aceea își păstrează locul: o cerere trimisă cât parserul era oprit tot ajunge.
- Dacă răspunsul nu poate fi publicat (broker căzut), cererea se reprimește (5s, 30s, 2m, 5m, 10m), apoi `food.alert`.

`PARSER_NATS_ENABLED=false` → doar HTTP (nu răspunde nimeni la cererile lui `core`).

## API (HTTP :8083, depanare)

| Cerere | Răspuns |
|---|---|
| `GET /healthz` | `200 {"status":"ok"}` |
| `GET /v1/fetch?date=YYYY-MM-DD` | `200 {date, fetched_at, cached, items:[{name, name_norm, category, price, components[]}]}` |

Erori (`{"error":{"code","message"}}`): `404 menu_empty` (0 feluri — meniu nepublicat sau site schimbat; chemătorul reîncearcă peste o oră),
`429 throttled` (+`Retry-After`), `400 bad_date`, `502 fetch_failed`, `504 timeout`.

## Configurare

Toate setările sunt într-un singur loc: structul `config.Config` din `internal/config` (încărcare, valori implicite, validare).
Fiecare variabilă, cu explicație și valoarea implicită, e în **`.env.example`**; un test verifică că fișierul chiar e egal cu valorile implicite.

Ordinea: variabile de mediu reale → fișierul `.env` (din directorul curent sau `go run . -env cale/.env`) → implicit.
O valoare goală înseamnă „nesetat". Fișierul `.env` lipsă e în regulă; unul stricat sau o valoare greșită oprește pornirea cu toate erorile odată.
`.env` e în `.gitignore`.

## bistro.md

Pagina e HTML static (server-side), deci un GET + selectori CSS (`goquery`) ajunge. Detaliile sunt în comentariul din `internal/bistro/bistro.go`.

- Se citește doar `#_catalog-complex` (vederea cu carduri). `#_catalog-complex_list` repetă aceleași carduri și e ignorat, altfel fiecare fel ar ieși dublu.
- Un card (`.main-catalog__product`: LUNCH 1, LUNCH 2, DIETA, POST) dă **2 feluri**: `LUNCH 1 Stand` (63 lei) și `LUNCH 1 Maxi` (75 lei). Prețul vine din `data-price`.
- Dacă pagina datei nu se poate parsa sau n-are feluri, se mai încearcă o dată aceeași pagină cu săptămâna ISO: `?date=2026-10-30&week=2026-W44` (`PARSER_SITE_URL_WEEK`; implicit = URL-ul de bază + `&week={week}`). Dacă nici ea nu dă feluri, rămâne răspunsul primei pagini (`no_menu` sau `parse_failed`). Site-ul căzut nu se cere de două ori.
- `components`: la Stand — lista din primul `.product_info_text`, separată prin `;`. La Maxi — supa (după `+`) urmată de componentele de la Stand.
- `category` = titlul secțiunii („Lunch Box”). Pozele și stocul nu se citesc.
- Pagină fără carduri → 0 feluri → `404 menu_empty`. Carduri fără niciun fel în ele (site schimbat) → eroare `502 fetch_failed`.
- Pagina reală salvată pentru teste: `internal/bistro/testdata/Catalog.html` (2026-10-27).

Alt site = alt pachet în `internal/<site>` care implementează `menu.Fetcher`, plus un `case` în `newFetcher` din `main.go`.

## Structură

```
main.go                      # pornire; alege modulul de site (newFetcher)
internal/config/             # Config, Load, citirea .env, validare
internal/menu/               # Item, Fetcher, Service (cache + pauză între cereri), erori tipizate, NormName, ParsePrice, RawStore
internal/contracts/          # evenimentele NATS ale contractului de sursă de meniu
internal/events/             # food.menu.requested -> food.menu.fetched / food.menu.error
internal/bus/                # NATS JetStream: publish, consumator durabil (așteaptă stream-ul), ULID
internal/api/                # /healthz, /v1/fetch
internal/bistro/             # bistro.md: HTTP + goquery, parser scris special pentru site
internal/htmlsrc/            # parser generic cu selectori din JSON; nefolosit acum
```

Trepte următoare (din notiță): **v2** — cererea JSON din spate (un nou `Fetcher` în `internal/`), **v3** — `chromedp`.
Restul programului depinde doar de `menu.Fetcher`, deci nu se schimbă.

## De făcut

- de verificat ce întoarce bistro.md pentru o zi nepublicată (acum: fără carduri = meniu gol);
- un `Fetcher` nou poate întoarce erori împachetate cu `menu.ErrSourceUnavailable` / `menu.ErrParseFailed` (`fmt.Errorf("%w: ...")`), ca să ajungă codul potrivit la `core`; altfel e tratat ca `source_unavailable`.
