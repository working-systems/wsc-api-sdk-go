# CLAUDE.md

`github.com/working-systems/wsc-api-sdk-go` — the **client SDK** for WSC's Go
microservice APIs: wire types, sync conventions, and HTTP clients that anything
talking TO a WSC API imports. It is deliberately **standard library only**
(a test parses go.mod and fails on any `require`). Server-side plumbing for
BUILDING a WSC service lives in the sibling module `wsc-api-lib-go`
(`D:\Interop\wsc-api-lib-go`), which depends on this one — never the other way round.

Packages: `orgapi` (the Organization API's list DTOs, ETag helpers, typed
errors, `Client` for the four sync endpoints) and `orgapi/orgapitest` (an
in-memory fake registry for consumers' tests). Future WSC API clients land
here as sibling packages (`assetlibapi`, …).

## Commands

Always `task`, never `make` — RAD Studio's Borland MAKE 5.41 shadows PATH and
cannot parse GNU Makefiles.

- `task test` — everything (no Docker, no services)
- `task lint` — gofmt check + go vet + staticcheck
- `task tidy-check` — go.mod must be dependency-free
- `task release -- vX.Y.Z` — tag + push from a clean `main`; refuses an
  existing tag. Then warm the proxy: `go list -m <module>@vX.Y.Z` from a
  consumer checkout.

## Load-bearing invariants

1. **The `orgapi` DTOs ARE wsc-api-organization's wire types.** That server
   imports them (type aliases in its `internal/api/dto.go`), so every json/doc/
   enum tag here is text in its committed OpenAPI spec. A change here is a
   wire-contract change: regenerate the server's treaty (`task spec-export`
   there) in the same sitting, and bump this module's minor version.
2. **Never rename an exported DTO type** — huma names schema components after
   Go type names. `OrganizationList`/`ClientList` are embedded by the server
   inside anonymous body structs precisely so its component names stayed put.
3. **No Go field named `Schema`, no `json:"$schema"` tag** on any type that
   can be a huma response body. huma injects `$schema` itself and silently
   drops it when the type already has one. The payload schema tag is
   `SchemaVersion` with `json:"schema"`.
4. **Standard library only.** Consumers such as the Delphi-adjacent tools, an
   MCP server, or a CLI must not inherit huma/chi/pgx by importing a client.
5. **`ETag`/`ETagMatches` are the registry's own rules**, shared so the fake
   and every consumer agree with the server: quoted, bare integer, `W/"n"`,
   comma lists, `*`.
6. **Tag first, then consumers.** `go mod tidy` in a consumer ignores
   `go.work`, so a tag must exist before a consumer can pin it. Never retag:
   proxies and sum.golang.org pin a tag forever — publish the next version.
7. `orgapitest` restates the contract; it is not a copy of the server's SQL.
   When the server's behaviour changes, change the fake and add the case to
   the server's `sdkclient_test.go` contract test, which drives `orgapi.Client`
   against the real server.
