# dzung-personal-shopper — implementation plan

An MCP server that tracks a personal shopping wishlist, finds the same item cheaper
elsewhere, and watches for price drops and restocks.

Written in Go. Status: step 1 complete.

---

## 0. Reality checks

Assumptions that did not survive contact with the real world. Recorded here because the
corrections shaped the architecture.

### 0.1 Shopify belongs in P0 — as one adapter, not the foundation

Mytheresa, SSENSE and NET-A-PORTER are **not Shopify stores**. They run custom/enterprise
platforms behind bot protection. Probed directly, an unauthenticated request gets:

| Site | Response | Edge |
|---|---|---|
| ssense.com | `403` | Cloudflare |
| net-a-porter.com | `403` | Akamai |
| therealreal.com | `403` | Varnish |
| mytheresa.com | `200` (HTML only, no product API) | — |

Separately, the Shopify Storefront and Admin APIs are **per-store credentialed** — you get a
token for a store you own. There is no "query Shopify for any product" endpoint.

**But Shopify still ships in P0.** Every Shopify storefront exposes its catalog at
`/products.json` with no key, including per-variant price and availability. More importantly:
*an interface with one implementation is a fake interface.* You cannot tell whether `Source` is
the right abstraction until two genuinely different things implement it. `manual` (no network,
user-supplied) and `shopify` (HTTP, JSON, paginated, currency from a separate endpoint) are
maximally different, so building both in P0 is what validates the design.

Shopify does not *replace* manual entry — it cannot see Mytheresa or SSENSE. Manual is the
coverage story; Shopify is the automation story. Both are permanent.

### 0.2 There is no global Shopify search

Shopify is not a marketplace with one index. It is software that ~5 million independent stores
each run separately. You can only ask *one specific store* about *its own* catalog. So resolving
a brand name to live prices requires:

```
"Khaite"  →  khaite.com  →  /search/suggest.json?q=romare+coat  →  the product
```

The per-store search endpoint works and returns everything needed in one call:

```bash
curl -s "https://khaite.com/search/suggest.json?q=coat&resources%5Btype%5D=product&resources%5Blimit%5D=3"
# → {"products":[{"title":"Romare Coat in Army Melange","handle":"romare-coat-in-army-melange",
#     "price":"4400.00","available":true,"image":"https://cdn.shopify.com/..."}]}
```

The missing link is **brand → domain**, for which no API exists. MVP: a small hand-seeded table,
grown as brands are added. See [Future directions](#4-future-directions) for the automation.

### 0.3 The Google Search API is closed

Google's Custom Search JSON API is **closed to new customers** and shuts down
**January 1, 2027**. You cannot sign up at any price.

Free tiers, verified against each provider's own pricing page:

| Provider | Free tier | Card required? |
|---|---|---|
| **Tavily** | 1,000 credits/month, renewing | **no** |
| Serper | 2,500 queries (reads as one-time) | no |
| Brave | $5 credit/month, approx. 1,000 queries | **yes** (identity check, not charged) |

**Default: Tavily** — no card, renews monthly, built for LLM-facing search. Put it behind a
`SearchProvider` interface so Brave, Serper or Exa drop in without touching the matcher.

Volume is not a constraint here. `find_elsewhere` runs once per wishlist item; thirty items
re-searched weekly is ~120 queries/month, roughly 12% of the smallest free tier. A domain
allowlist of ~8 sites keeps it there.

### 0.4 Do not scrape bot-protected retailers

Against their ToS, breaks constantly, and "I defeated Akamai" is a worse interview story than
"I designed around it." The MCP server does not have to be the thing that fetches the page.

A `manual` source lets **Claude fetch or the user paste** the page; the server normalizes →
stores → diffs → alerts. The wishlist works on day one for Mytheresa/SSENSE/NAP with zero ToS
risk, and the interesting engineering is untouched by where the bytes came from.

The sanctioned path to bulk catalog data is **affiliate network product feeds** — Mytheresa is
on CJ Affiliate and Rakuten Advertising; SSENSE on Rakuten, Impact and ShareASale; NET-A-PORTER
similarly. Publisher accounts are free, approval varies. Deferred to step 8.

### 0.5 Verified brand coverage

Measured, not assumed. Reproduce any row with:

```bash
curl -sL -m 8 -A "Mozilla/5.0" "https://<host>/meta.json"
```

| Brand | Shopify? | Evidence |
|---|---|---|
| Khaite | yes | `myshopify_domain: khaite.myshopify.com` |
| Aimé Leon Dore | yes | `myshopify_domain: aime-leon-dore.myshopify.com` |
| Floyd (furniture) | yes | full catalog, per-variant availability |
| Big Blanket | yes | `200` on both endpoints |
| Aritzia | no | `403` — custom platform |
| Sezane | no | serves `{}` — valid JSON, no shop fields |
| Sephora | no | `404` |
| Article, Burrow, Bed Bath & Beyond | no | `404` |

Two lessons. **Furniture skews Shopify harder than luxury fashion.** And **Sezane returning `{}`**
is why detection keys on `myshopify_domain` rather than on a `200` — valid JSON is not proof.

### 0.6 Third-party integrations, checked

**ShopMy** — a real API, but not self-serve:
- A **Brand Partner API** (order reporting).
- A **Developer OAuth API**: `Search Catalog` ("search the catalog or resolve a product URL"),
  `Fetch/Create/Edit Collection`, `Fetch Links`, `Fetch Profile`.
- **No API key exists for creators.** Access requires emailing them.

`Search Catalog` is the most interesting third-party endpoint for this project — URL → canonical
product across many brands is exactly the resolver step. Worth an email; not worth blocking on.
Design `discovery` so ShopMy is one `SearchProvider` among several.

**CAKE** — an invite-only $100/yr membership giving ~10 stored-value cards ($50–$450) plus early
access. No public API. Model it as a generic `perks` table populated by paste-import, so LTK,
cashback, student codes and store credit all land in the same place.

---

## 1. Architecture

```
cmd/dzung-personal-shopper/   single binary, subcommands: start | watch | migrate
internal/
  detect/       platform detection (step 1, done)
  model/        Item, Snapshot, Variant, Observation, Alert, Purchase, Perk, Match
  store/        SQLite (modernc.org/sqlite — pure Go, no cgo)
  source/       source.go (the interface)
    manual/     Claude- or user-supplied snapshots
    shopify/    /products.json, /search/suggest.json, /meta.json
    affiliate/  later: CJ / Rakuten / Impact feeds
  discovery/
    search/     SearchProvider iface: tavily, brave, serper, shopmy
    match/      brand+title normalization, scoring, confidence
  rules/        diff engine: price drop, sale started, restock, variant-back
  notify/       Notifier iface: ntfy, pushover, smtp
  mcpserver/    tool + resource + prompt handlers
```

`cmd/` is convention: the directory name becomes the binary name. `internal/` is **enforced by
the compiler** — packages under it cannot be imported by any other module, so their APIs stay
free to change.

### 1.1 The one abstraction that matters

```go
// Source turns a product reference into a normalized point-in-time snapshot.
// Adding a retailer means adding one implementation and nothing else.
type Source interface {
    Name() string
    CanHandle(u *url.URL) bool
    Fetch(ctx context.Context, ref ProductRef) (*Snapshot, error)
}

type Snapshot struct {
    FetchedAt    time.Time
    Brand        string
    Title        string
    Currency     string
    PriceCents   int64
    CompareCents int64 // list/"was" price; 0 when absent
    Available    bool
    Variants     []Variant
    ImageURL     string
    Raw          json.RawMessage
}
```

Money is `int64` minor units, never a float. Shopify sends `"590.00"` as a **string**; parsing
that into a `float64` introduces rounding error into a system whose entire purpose is comparing
prices.

Currency is not in `products.json` — it comes from `/meta.json`, a separate cacheable per-host
call. That constraint is exactly why building Shopify early was the right call.

### 1.2 Storage: append-only observations

Never overwrite an item's price. Every poll appends an `observations` row; alerts come from
comparing the two most recent. Price history is free, and the 14-day price-match feature becomes
a query rather than a subsystem.

```sql
items(id, url, source, brand, title, variant, image_url, notes, added_at, archived_at)  -- UNIQUE(url, variant)
observations(id, item_id, fetched_at, price_cents, compare_cents, currency,
             available, variants_json)
alerts(id, item_id, kind, dedupe_key, payload_json, created_at, read_at, notified_at)
matches(id, item_id, retailer, url, price_cents, size, condition, confidence,
        first_seen, last_seen)
purchases(id, item_id, retailer, order_ref, paid_cents, currency,
          purchased_at, window_days, resolved_at)
perks(id, provider, code, description, retailer, value_cents, kind, expires_at, used_at)
brands(id, name, domain, platform, verified_at)   -- the brand → domain registry
```

`alerts.dedupe_key` (e.g. `item:42|kind:price_drop|price:38000`) is what stops a flapping price
from sending forty push notifications. Unique index on it.

### 1.3 Two processes, one database

An MCP stdio server **only runs while its client runs it** — it cannot poll on a schedule. So:

- `start` — MCP server over stdio, invoked by Claude Code / Claude Desktop.
- `watch` — long-lived poller. Ticker with jitter, per-source rate limits, backoff.

Both open the same SQLite file (WAL mode, `_busy_timeout` set). A `launchd` plist keeps `watch`
alive across reboots. Notifications default to **ntfy.sh** — a topic name is the whole config.

### 1.4 Keep LLM-shaped work on the Claude side

| Job | Where it runs |
|---|---|
| Parse a messy pasted perk list | Claude → calls `add_perks` with structured JSON |
| Decide if two listings are the same item | Go scores; the human adjudicates |
| Write the price-match email | Claude, from structured facts the server returns |

`price_match_evidence` returns **facts** — retailer, order ref, paid price, current price, delta,
days left in window — not prose. The server stays deterministic, testable and cheap.

---

## 2. MCP tool surface

**Wishlist** — `add_item`, `list_items`, `remove_item`, `archive_item`, `check_item`,
`record_snapshot`, `get_price_history`

**Discovery** — `find_elsewhere(item_id, include_secondhand?)`, `list_matches(item_id)`

**Brands** — `inspect_url` *(built)*, `add_brand`, `list_brands`

**Alerts** — `list_alerts`, `ack_alerts`

**Perks** — `add_perks`, `list_perks`, `mark_perk_used`, `best_perk_for_item`

**Price match** — `record_purchase`, `list_price_match_candidates`, `price_match_evidence`

**Resources:** `shopper://items/{id}`, `shopper://history/{id}`, `shopper://digest/weekly`
**Prompts:** `price_match_email`, `weekly_digest`

---

## 3. Steps

Each step ends with something runnable. One file at a time, reviewed before moving on.

| # | Step | Status |
|---|---|---|
| 1 | Module + minimal MCP server + `inspect_url` + tests | **done** |
| 2 | Data layer — SQLite, migrations, model types. No MCP. | next |
| 3 | `Source` interface + `manual` + `shopify`, fixture-tested | |
| 4 | Wishlist MCP tools wired to sources | |
| 5 | **Discovery — Tavily search, matcher, `find_elsewhere`** | |
| 6 | Watcher + diff engine + ntfy alerts | |
| 7 | Perks + purchases + price matching | |
| 8 | README, diagrams, CI, demo | |

Discovery precedes the watcher deliberately. The goal is **"show me candidate links, ranked,
with prices"** — the human clicks through and verifies. That is a search call, a title
normalizer and a sort, not an identity-resolution problem. Scoping it that way is what makes it
cheap enough to build early.

**Prerequisite for step 5:** a [Tavily](https://tavily.com/) API key.
Free tier, 1,000 credits/month, no credit card required.

---

## 4. Future directions

Deliberately out of scope for v1, recorded so the design leaves room for them.

### 4.1 Automatic brand → domain resolution

Today the `brands` table is seeded by hand. The obvious automation is to **guess `<brand>.com`
and verify with `inspect_url`**. Tested against the brands above:

```
khaite       → khaite.com        200  SHOPIFY
aimeleondore → aimeleondore.com  200  SHOPIFY
floydhome    → floydhome.com     200  SHOPIFY
aritzia      → aritzia.com       403  -
sezane       → sezane.com        403  -
sephora      → sephora.com       404  -
```

3/8 resolved, and — the property that matters — **zero false positives**. That is what makes the
heuristic safe: it only ever *guesses*, and detection is the verifier. A wrong guess costs one
HTTP call and falls back to manual entry. **Guess-then-verify, never guess-and-trust.**

Extensions: slugify multi-word brands (`aimé leon dore` → `aimeleondore`), try `.co`/`.us`/`.fr`
on miss, cache negative results so a known-bad guess is never retried.

### 4.2 Listing authenticity signals

v1 returns matches **sorted by price**, and the human verifies each link. That is the correct
v1 scope: cheapest-first is honest, and price alone is a bad proxy for trust — on secondhand
platforms the cheapest listing is disproportionately likely to be the fake one.

A future version should attach a **trust signal** to each match so the sort can be by value
rather than raw price. Candidate inputs, roughly in order of cost to implement:

- **Platform-level authentication programs** — The RealReal authenticates in-house; Vestiaire
  Collective and Poshmark offer authentication above a price threshold. Whether a listing is
  covered is often visible on the page, and it is the strongest single signal.
- **Seller reputation** — rating, sales volume, account age, where the platform exposes it.
- **Price-anomaly detection** — a listing far below the observed distribution for that item is a
  flag, not a bargain. The `observations` table already provides the distribution to compare
  against, at no extra cost.
- **Retailer tier** — a first-party or authorized stockist outranks a marketplace listing at the
  same price.
- **Image comparison** against the reference product photo (perceptual hash). Highest effort,
  lowest confidence; last on the list.

The output should stay **advisory**. This surfaces evidence for a human decision; it never
declares an item genuine and never auto-purchases. Scope the scoring logic in a future version;
for now, reserve the `matches.confidence` column and keep the ranking function pluggable so
swapping `sortByPrice` for `sortByValue` touches one call site.

### 4.3 Browser extension for wishlist capture

The natural successor to manual entry. Instead of pasting a URL into Claude, a browser
extension adds an "add to wishlist" button on any product page; clicking it writes straight
to the database.

Why this is the right shape rather than a workaround:

- **It defeats bot protection without fighting it.** The page is already rendered, in a real
  browser, in an authenticated session. Mytheresa and SSENSE hand the extension the price and
  size availability that a server-side fetch gets a `403` for. No scraping, no ToS violation —
  the user is simply looking at a page they are entitled to look at.
- **It needs no new backend.** The extension posts the same normalized `Snapshot` the `manual`
  source already accepts. It is a second client for an existing path, not a new subsystem.
- **It captures the moment of intent.** Wishlists decay because adding an item costs attention.
  One click while browsing is the difference between a list that reflects what you want and one
  that reflects what you remembered to type up later.

Implementation sketch: a small local HTTP endpoint (`dzung-personal-shopper watch` already runs
continuously, so it can host it), extension posts JSON to `localhost`, same validation and
storage as `record_snapshot`. Per-site content scripts extract price and sizes; a generic
fallback reads schema.org `Product` markup, which most retailers publish for Google Shopping.

Prerequisite: the `manual` source and `record_snapshot` (steps 3-4) must exist first — the
extension is a client for them.

### 4.4 Other directions

- **Email ingestion** for automatic purchase detection, replacing manual `record_purchase`.
  Deliberately deferred — it needs mailbox access, and the manual path proves the feature first.
- **Affiliate feeds** (CJ / Rakuten / Impact) for legitimate bulk catalog access to the
  luxury retailers.
- **ShopMy partner access** for `Search Catalog` as a resolver.
- **Size normalization** across IT/FR/UK/US. v1 stores the size string verbatim.

---

## 5. Making it read as senior work

- **Interface-driven adapters** with table-driven tests over recorded HTTP fixtures. No live
  network in CI.
- **Append-only event store** rather than mutable current-price rows.
- **Idempotent alerting** via dedupe keys — evidence of thinking about flapping.
- **Rate limiting, jitter, backoff** per source, with a documented politeness budget.
- **A README that says what it does not do.** State plainly that it does not defeat bot
  protection and does not scrape retailers that forbid it, and explain the user-in-the-loop
  design that makes that unnecessary. Restraint reads as judgment.
- **`go install`-able** — `modernc.org/sqlite` keeps it pure Go, no cgo toolchain.
- **CI**: build, `go vet`, tests, `golangci-lint`.
- **A 30-second demo GIF** in the README. This is what actually gets clicked from a resume.

---

## 6. Open decisions

1. **Currency/region** — Mytheresa and NAP price per region. Store currency on every observation
   and pin a region per item, or EUR/USD switching will look like a price drop.
2. **Sizes** — normalization across regions is genuinely hard. v1 stores the string verbatim.
3. **Secondhand matching** — TRR/Vestiaire/Depop listings are one-off and condition-varied.
   Treat them as `matches` with a `condition` field, never as the same SKU.
4. **Retention** — observations grow forever. They are tiny; do not prune before it hurts.

---

## Sources

- [MCP Go SDK (official, with Google)](https://github.com/modelcontextprotocol/go-sdk) · [pkg.go.dev](https://pkg.go.dev/github.com/modelcontextprotocol/go-sdk/mcp)
- [MCP Inspector](https://github.com/modelcontextprotocol/inspector) — `npx @modelcontextprotocol/inspector <binary> start`
- [Google Custom Search JSON API — closed to new customers, ends 2027-01-01](https://developers.google.com/custom-search/v1/overview)
- Search providers: [Tavily](https://tavily.com/pricing) (default, no card) · [Serper](https://serper.dev/) · [Brave Search API](https://brave.com/search/api/) (card required)
- [Shopify Storefront API](https://shopify.dev/docs/api/storefront/latest)
- [ShopMy API — Getting Started](https://docs.shopmy.us/reference/getting-started-with-your-api) · [OAuth / Collections / Search Catalog](https://docs.shopmy.us/reference/getting-started-with-your-api-1)
- Affiliate networks: [Mytheresa](https://www.mytheresa.com/euro/en/affiliates) · [SSENSE](https://uppromote.com/affiliate-program-directory/ssense/) · [NET-A-PORTER](https://uppromote.com/affiliate-directory/net-a-porter/)
