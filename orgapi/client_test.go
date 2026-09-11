package orgapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/working-systems/wsc-api-sdk-go/orgapi"
	"github.com/working-systems/wsc-api-sdk-go/orgapi/orgapitest"
)

func newClient(t *testing.T, srv *httptest.Server, key string, opts ...orgapi.Option) *orgapi.Client {
	t.Helper()
	c, err := orgapi.NewClient(srv.URL, key, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestModuleIsStdlibOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "require") {
			t.Fatalf("wsc-api-sdk-go must stay dependency-free (it is the client SDK, not the service kit); go.mod has: %s", line)
		}
	}
}

func TestNewClientValidation(t *testing.T) {
	for _, bad := range []string{"", "127.0.0.1:8082", "ftp://x", "http://"} {
		if _, err := orgapi.NewClient(bad, "k"); err == nil {
			t.Errorf("NewClient(%q) accepted", bad)
		}
	}
	if _, err := orgapi.NewClient("http://x", ""); err == nil {
		t.Error("an empty key must be refused up front")
	}
	c, err := orgapi.NewClient("https://organization.api.example/prefix/?x=1#f", "k")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.BaseURL(); got != "https://organization.api.example/prefix" {
		t.Errorf("BaseURL = %q", got)
	}
}

func TestVersionAndNotModified(t *testing.T) {
	reg := orgapitest.New()
	srv := reg.Serve()
	defer srv.Close()
	c := newClient(t, srv, "wscorg_test")
	ctx := context.Background()

	v, err := c.Version(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if v.SchemaVersion != "v1" || v.Version != 1 {
		t.Fatalf("fresh registry: %+v", v)
	}
	for _, inm := range []string{orgapi.ETag(1), "1", `W/"1"`, "*"} {
		if _, err := c.Version(ctx, inm); !errors.Is(err, orgapi.ErrNotModified) {
			t.Errorf("If-None-Match %q: err = %v, want ErrNotModified", inm, err)
		}
	}
	reg.Put(orgapi.OrganizationDTO{DisplayName: "IBEW Local 43"})
	if v, err := c.Version(ctx, orgapi.ETag(1)); err != nil || v.Version != 2 {
		t.Fatalf("after a change: %+v, %v", v, err)
	}
	if cv, err := c.ClientsVersion(ctx, ""); err != nil || cv.Version != 2 {
		t.Fatalf("both lists share the one version: %+v, %v", cv, err)
	}
}

func TestListFullDeltaAndTombstones(t *testing.T) {
	reg := orgapitest.New()
	srv := reg.Serve()
	defer srv.Close()
	c := newClient(t, srv, "wscorg_test")
	ctx := context.Background()

	a := reg.Put(orgapi.OrganizationDTO{OrganizationID: 43, UnionCode: "IBEW", LocalNumber: "43", DisplayName: "IBEW Local 43",
		Aliases: []orgapi.AliasDTO{{AliasID: 1, Alias: "Local 43"}}})
	b := reg.Put(orgapi.OrganizationDTO{OrganizationID: 24, UnionCode: "IBEW", LocalNumber: "24", Status: "retired"})

	full, err := c.ListOrganizations(ctx, orgapi.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if full.Version != reg.Version() || full.Since != 0 || len(full.Organizations) != 2 || len(full.Removed) != 0 {
		t.Fatalf("full list: %+v", full.ListMeta)
	}
	if full.Organizations[0].OrganizationID != 24 || full.Organizations[1].OrganizationID != 43 {
		t.Fatalf("ordered by id: %d, %d", full.Organizations[0].OrganizationID, full.Organizations[1].OrganizationID)
	}
	if got := full.Organizations[1]; got.Version != a.Version || len(got.Aliases) != 1 || got.ExternalIDs != nil || got.OrgTypes == nil {
		t.Fatalf("row 43 round trip: %+v", got)
	}
	if b.RetiredOn == nil || full.Organizations[0].RetiredOn == nil {
		t.Fatal("a retired row carries retiredOn")
	}
	if full.Removed == nil {
		t.Fatal("Removed must never be nil")
	}

	// Status filter on the full list; refused on a delta.
	if active, err := c.ListOrganizations(ctx, orgapi.ListQuery{Status: "active"}); err != nil || len(active.Organizations) != 1 {
		t.Fatalf("status filter: %v %v", err, active)
	}
	if _, err := c.ListOrganizations(ctx, orgapi.ListQuery{Since: 1, Status: "active"}); !errors.Is(err, orgapi.ErrBadRequest) {
		t.Fatalf("since+status: %v", err)
	}

	// The one-request sync tick: since=v plus If-None-Match: v → 304 while current.
	v0 := full.Version
	if _, err := c.ListOrganizations(ctx, orgapi.ListQuery{Since: v0, IfNoneMatch: orgapi.ETag(v0)}); !errors.Is(err, orgapi.ErrNotModified) {
		t.Fatalf("current mirror must get 304: %v", err)
	}

	reg.Put(orgapi.OrganizationDTO{OrganizationID: 43, UnionCode: "IBEW", LocalNumber: "43", DisplayName: "IBEW Local 43 (renamed)"})
	reg.Remove(24)
	delta, err := c.ListOrganizations(ctx, orgapi.ListQuery{Since: v0, IfNoneMatch: orgapi.ETag(v0)})
	if err != nil {
		t.Fatal(err)
	}
	if delta.Since != v0 || delta.Version != reg.Version() {
		t.Fatalf("delta meta: %+v", delta.ListMeta)
	}
	if len(delta.Organizations) != 1 || delta.Organizations[0].DisplayName != "IBEW Local 43 (renamed)" {
		t.Fatalf("delta rows: %+v", delta.Organizations)
	}
	if len(delta.Removed) != 1 || delta.Removed[0].OrganizationID != 24 || delta.Removed[0].Version <= v0 {
		t.Fatalf("tombstones: %+v", delta.Removed)
	}
	reqs := reg.Requests()
	last := reqs[len(reqs)-1]
	if last.Path != orgapi.PathOrganizations || last.RawQuery != "since=3" || last.IfNoneMatch != orgapi.ETag(v0) || last.APIKey != "wscorg_test" {
		t.Fatalf("recorded request: %+v", last)
	}
}

func TestListClients(t *testing.T) {
	reg := orgapitest.New()
	srv := reg.Serve()
	defer srv.Close()
	c := newClient(t, srv, "wscorg_test")
	ctx := context.Background()

	reg.Put(orgapi.OrganizationDTO{OrganizationID: 7, DisplayName: "Seven"})
	reg.Put(orgapi.OrganizationDTO{OrganizationID: 8, DisplayName: "Eight (no phonelog id)"})
	reg.SetClientIDs(7, 700, 701)
	v0 := reg.Version()

	full, err := c.ListClients(ctx, orgapi.ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	if len(full.Clients) != 2 || full.Clients[0].ClientID != 700 || full.Clients[1].ClientID != 701 || full.Clients[0].OrganizationID != 7 {
		t.Fatalf("lens rows: %+v", full.Clients)
	}
	reg.SetClientIDs(7, 701) // 700 stops being live: the lens row vanishes, the organization does not
	delta, err := c.ListClients(ctx, orgapi.ListQuery{Since: v0})
	if err != nil {
		t.Fatal(err)
	}
	if len(delta.Clients) != 1 || delta.Clients[0].ClientID != 701 {
		t.Fatalf("delta rows: %+v", delta.Clients)
	}
	if len(delta.Removed) != 1 || delta.Removed[0].ClientID != 700 || delta.Removed[0].OrganizationID != 7 {
		t.Fatalf("client tombstones: %+v", delta.Removed)
	}
	orgs, err := c.ListOrganizations(ctx, orgapi.ListQuery{Since: v0})
	if err != nil || len(orgs.Removed) != 0 || len(orgs.Organizations) != 1 {
		t.Fatalf("the organization list only sees the touched parent: %+v %v", orgs, err)
	}
}

func TestRegistryReset(t *testing.T) {
	reg := orgapitest.New()
	srv := reg.Serve()
	defer srv.Close()
	c := newClient(t, srv, "wscorg_test")
	ctx := context.Background()

	reg.Put(orgapi.OrganizationDTO{DisplayName: "one"})
	reg.Put(orgapi.OrganizationDTO{DisplayName: "two"})
	held := reg.Version()
	reg.ResetTo(1)
	_, err := c.ListOrganizations(ctx, orgapi.ListQuery{Since: held, IfNoneMatch: orgapi.ETag(held)})
	if !errors.Is(err, orgapi.ErrRegistryReset) {
		t.Fatalf("since ahead of the registry: %v", err)
	}
	var se *orgapi.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusConflict || !strings.Contains(se.Detail, "restored or reset") {
		t.Fatalf("StatusError: %+v", se)
	}
	full, err := c.ListOrganizations(ctx, orgapi.ListQuery{})
	if err != nil || full.Version != 1 || len(full.Organizations) != 2 {
		t.Fatalf("full reload after reset: %+v %v", full, err)
	}
}

func TestAuthAndOutage(t *testing.T) {
	reg := orgapitest.New(orgapitest.WithAPIKey("wscorg_right"))
	srv := reg.Serve()
	defer srv.Close()
	ctx := context.Background()

	if _, err := newClient(t, srv, "wscorg_wrong").Version(ctx, ""); !errors.Is(err, orgapi.ErrUnauthorized) {
		t.Fatalf("wrong key: %v", err)
	}
	c := newClient(t, srv, "wscorg_right")
	if _, err := c.Version(ctx, ""); err != nil {
		t.Fatalf("right key: %v", err)
	}
	reg.SetDown(true)
	_, err := c.Version(ctx, "")
	var se *orgapi.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusServiceUnavailable {
		t.Fatalf("outage: %v", err)
	}
	for _, sentinel := range []error{orgapi.ErrNotModified, orgapi.ErrUnauthorized, orgapi.ErrForbidden, orgapi.ErrRegistryReset, orgapi.ErrBadRequest} {
		if errors.Is(err, sentinel) {
			t.Errorf("a 503 must not look like %v", sentinel)
		}
	}
	reg.SetDown(false)
	if _, err := c.Version(ctx, ""); err != nil {
		t.Fatalf("after recovery: %v", err)
	}
}

func TestSchemaRefusal(t *testing.T) {
	reg := orgapitest.New(orgapitest.WithSchema("v2"))
	srv := reg.Serve()
	defer srv.Close()
	c := newClient(t, srv, "wscorg_test")
	if _, err := c.Version(context.Background(), ""); !errors.Is(err, orgapi.ErrSchema) {
		t.Fatalf("schema v2: %v", err)
	}
	if _, err := c.ListOrganizations(context.Background(), orgapi.ListQuery{}); !errors.Is(err, orgapi.ErrSchema) {
		t.Fatalf("schema v2 list: %v", err)
	}
}

func TestETagBodyDisagreement(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"5"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"$schema":"x","schema":"v1","version":6,"organizations":[],"removed":[]}`))
	}))
	defer srv.Close()
	c := newClient(t, srv, "k")
	_, err := c.ListOrganizations(context.Background(), orgapi.ListQuery{})
	if err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("mixed response must be refused: %v", err)
	}
}

func TestRequestShape(t *testing.T) {
	var got *http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		w.Header().Set("ETag", `"1"`)
		_, _ = w.Write([]byte(`{"schema":"v1","version":1}`))
	}))
	defer srv.Close()
	c := newClient(t, srv, "wscorg_k", orgapi.WithUserAgent("assetlib-api/2.0.0.0"))
	if _, err := c.Version(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if got.URL.Path != orgapi.PathOrganizationsVersion || got.Header.Get(orgapi.HeaderAPIKey) != "wscorg_k" ||
		got.Header.Get("If-None-Match") != "1" || got.Header.Get("User-Agent") != "assetlib-api/2.0.0.0" ||
		got.Header.Get("Accept") != "application/json" {
		t.Fatalf("request: %s %v", got.URL, got.Header)
	}
}
