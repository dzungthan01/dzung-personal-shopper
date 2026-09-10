# dzung-personal-shopper

MCP server that tracks a shopping wishlist, finds items cheaper elsewhere, and
watches for price drops and restocks. See PLAN.md for design and roadmap.

## Working process

- One file at a time. Describe it, get approval, write it, wait for review.
- Do not write ahead. Do not batch files.
- Do not edit files while a review is in progress.

## Layout

- `cmd/` — binaries. Wiring only: flags, signals, transport.
- `internal/` — logic. Compiler-enforced private to this module.
- `internal/mcpserver/` — MCP translation layer. No domain logic.

## Commands

```bash
go build ./... && go vet ./... && go test ./...
go install ./cmd/dzung-personal-shopper
npx @modelcontextprotocol/inspector ~/go/bin/dzung-personal-shopper start
```

## Constraints

- Money is `int64` minor units. Never float.
- stdio MCP servers speak JSON-RPC on stdout — all logging to stderr.
- No scraping past bot protection.

## Style

Naming — full words, camelCase. No abbreviations:

```
server   not srv        userAgent  not ua        httpClient not hc
result   not res        input      not in        output     not out
flagSet  not fs         testCase   not tt        request    not req
```

Exception: method receivers stay short (`func (c *Client)`) per Go convention.

Comments — one or two lines. Say what is non-obvious, then stop.

- No multi-paragraph explanations, no teaching commentary, no rationale essays.
- Do not restate what the code plainly does.
- Package doc comment must sit directly above `package X`. A blank line unlinks it.

```go
// Good
// maxMetaBytes caps the read: a non-Shopify host may answer with a full HTML page.

// Too wordy
// maxMetaBytes caps how much of /meta.json we will read. A non-Shopify host may
// answer with a full HTML page, and we should not pull an unbounded body into
// memory just to reject it.
```

Examples in docs and tests use mid-range brands (Everlane, Cuyana, Girlfriend
Collective), not luxury ones.
