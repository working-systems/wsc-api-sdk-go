# wsc-sdk-go

Client SDK for Working Systems' Go microservice APIs. Standard library only.

| Package | What it is |
|---|---|
| `orgapi` | The WSC Organization API's list wire types (`OrganizationDTO`, `ClientDTO`, list bodies, tombstones), the sync protocol's ETag rules, typed errors, and an HTTP `Client` for `GET /api/v1/organizations[/version]` and `GET /api/v1/clients[/version]`. |
| `orgapi/orgapitest` | An in-memory fake of those four endpoints for testing consumers without the real service. |

The server (`wsc-api-organization`) imports the same DTO types for its own
handlers, so a client built on this module decodes exactly what the server
encodes.

## Install

```
go get github.com/working-systems/wsc-sdk-go@latest
```

## Keeping a mirror current

The registry publishes one gapless version number that moves on every change.
A consumer holds the version it last applied and, once per tick, makes **one**
request:

```go
c, err := orgapi.NewClient("https://organization.api.workingsystems.com", os.Getenv("ORGANIZATION_API_KEY"),
	orgapi.WithUserAgent("my-service/1.0"))

list, err := c.ListOrganizations(ctx, orgapi.ListQuery{Since: held, IfNoneMatch: orgapi.ETag(held)})
switch {
case errors.Is(err, orgapi.ErrNotModified):
	// still current: nothing to do
case errors.Is(err, orgapi.ErrRegistryReset):
	// the registry was restored to an earlier point: drop the copy, reload with ListQuery{}
case err != nil:
	// network, auth, or an unsupported payload schema — try again next tick
default:
	// replace each of list.Organizations, drop each of list.Removed, then store list.Version
}
```

Every row in a delta is complete (replace, don't patch). Retirement is an
ordinary row change (`status: "retired"`); `Removed` carries only owner-side
hard deletes. `ListClients` works the same way for the phonelog-ID lens, where
`Removed` also reports a CLIENT_ID that stopped being live.

If you only want to know whether anything moved: `c.Version(ctx, orgapi.ETag(held))`.

## Testing a consumer

```go
reg := orgapitest.New()          // version 1, empty
srv := reg.Serve()               // httptest server
defer srv.Close()
reg.Put(orgapi.OrganizationDTO{OrganizationID: 43, DisplayName: "IBEW Local 43"})
c, _ := orgapi.NewClient(srv.URL, "wscorg_anything")
// ... exercise your code; reg.Remove, reg.SetClientIDs, reg.ResetTo, reg.SetDown, reg.Requests()
```

## Working on this module and a consumer at once

In the consumer checkout: `go work init . ../wsc-api-sdk-go` (gitignored there; the
folder is `wsc-api-sdk-go`, the module path stays `wsc-sdk-go`).
Builds and tests then use this checkout; `go mod tidy` does not — it resolves
the tag named in the consumer's `go.mod`, so tag here (`task release -- vX.Y.Z`)
before tidying or building an image there. Never retag.
