# parser (food-order)

Serviciu fără stare: aduce meniul de pe site pentru o zi și îl întoarce ca listă de feluri.
Nu scrie în nicio bază de date. Specificația: Obsidian → `projects/food-order/Parser`.

## Pornire

```
go mod tidy                      # prima dată: aduce goquery, creează go.sum
go test ./...
cp .env.example .env             # opțional: toate valorile au implicit; schimbi doar ce ai nevoie
go run .                         # citește https://bistro.md/ro/catalog?date={date}
curl "http://localhost:8082/v1/fetch?date=2026-10-28"
```

`{date}` din URL se înlocuiește cu data cerută (`YYYY-MM-DD`). Fără `date` în cerere se ia **mâine** (fusul `PARSER_TZ`).

## API

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
- `components`: la Stand — lista din primul `.product_info_text`, separată prin `;`. La Maxi — supa (după `+`) urmată de componentele de la Stand.
- `category` = titlul secțiunii („Lunch Box”). Pozele și stocul nu se citesc.
- Pagină fără carduri → 0 feluri → `404 menu_empty`. Carduri fără niciun fel în ele (site schimbat) → eroare `502 fetch_failed`.
- Pagina reală salvată pentru teste: `internal/bistro/testdata/Catalog.html` (2026-10-27).

Alt site = alt pachet în `internal/<site>` care implementează `menu.Fetcher`, plus un `case` în `newFetcher` din `main.go`.

## Structură

```
main.go                      # pornire; alege modulul de site (newFetcher)
internal/config/             # Config, Load, citirea .env, validare
internal/menu/               # Item, Fetcher, Service (cache + pauză între cereri), NormName, ParsePrice, RawStore
internal/api/                # /healthz, /v1/fetch
internal/bistro/             # bistro.md: HTTP + goquery, parser scris special pentru site
internal/htmlsrc/            # parser generic cu selectori din JSON; nefolosit acum
```

Trepte următoare (din notiță): **v2** — cererea JSON din spate (un nou `Fetcher` în `internal/`), **v3** — `chromedp`.
Restul programului depinde doar de `menu.Fetcher`, deci nu se schimbă.

## De făcut

- de verificat ce întoarce bistro.md pentru o zi nepublicată (acum: fără carduri = meniu gol);
- legarea la NATS (`food.menu.fetch.requested` → `food.menu.fetched` / `food.menu.empty`) când există `internal/bus`.
