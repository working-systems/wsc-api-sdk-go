// Package orgapitest is an in-memory stand-in for the WSC Organization API's
// four list-sync endpoints, for testing consumers — mirrors, CLIs, anything
// that polls — without a database or the real service. It is the sync
// contract restated, deliberately not a copy of the server's SQL, so it also
// serves as a second implementation the real server's integration tests can be
// compared against.
//
// Semantics reproduced: If-None-Match → 304 with the ETag repeated and no
// body; ?since=N → the complete current rows changed after N plus the
// tombstones since; since ahead of the version → 409; since with status → 422;
// ETag and Cache-Control on every 200; a "$schema" property in every body
// (consumers must ignore it); RFC-7807 problem+json errors; X-API-Key
// required (any non-empty key, or exactly the one given to WithAPIKey).
package orgapitest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/working-systems/wsc-api-sdk-go/orgapi"
)

// Registry is the fake. Every mutator bumps the registry version exactly the
// way the real one does: one increment per change, in commit order.
type Registry struct {
	mu               sync.Mutex
	version          int64
	nextID           int64
	orgs             map[int64]orgapi.OrganizationDTO
	clientIDs        map[int64][]int64
	orgTombstones    []orgapi.RemovedOrganizationDTO
	clientTombstones []orgapi.RemovedClientDTO
	key              string
	schema           string
	down             bool
	requests         []Request
	now              func() time.Time
}

// Request is one call the fake answered, for asserting a consumer's behaviour
// (did it send If-None-Match? did it ask for a delta?).
type Request struct {
	Method      string
	Path        string
	RawQuery    string
	IfNoneMatch string
	APIKey      string
}

// Option configures a Registry.
type Option func(*Registry)

// WithAPIKey makes the fake accept exactly this key (401 otherwise). By
// default any non-empty X-API-Key passes and a missing one is a 401.
func WithAPIKey(key string) Option { return func(r *Registry) { r.key = key } }

// WithSchema makes the fake stamp another schema tag on its payloads, to test
// that a consumer refuses a shape it does not speak.
func WithSchema(tag string) Option { return func(r *Registry) { r.schema = tag } }

// New returns an empty registry at version 1 — the real one starts its
// counter at 1 too, so a consumer starting from since=0 gets everything and
// one starting from 1 gets nothing.
func New(opts ...Option) *Registry {
	r := &Registry{
		version:   1,
		nextID:    1,
		orgs:      map[int64]orgapi.OrganizationDTO{},
		clientIDs: map[int64][]int64{},
		schema:    orgapi.ListSchema,
		now:       func() time.Time { return time.Now().UTC().Truncate(time.Microsecond) },
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Serve starts an httptest server around Handler. Close it when done.
func (r *Registry) Serve() *httptest.Server { return httptest.NewServer(r.Handler()) }

// Handler serves the four sync endpoints and /healthz.
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+orgapi.PathOrganizationsVersion, func(w http.ResponseWriter, req *http.Request) { r.handleVersion(w, req) })
	mux.HandleFunc("GET "+orgapi.PathClientsVersion, func(w http.ResponseWriter, req *http.Request) { r.handleVersion(w, req) })
	mux.HandleFunc("GET "+orgapi.PathOrganizations, func(w http.ResponseWriter, req *http.Request) { r.handleList(w, req, false) })
	mux.HandleFunc("GET "+orgapi.PathClients, func(w http.ResponseWriter, req *http.Request) { r.handleList(w, req, true) })
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

// Version is the current registry version.
func (r *Registry) Version() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.version
}

// Put inserts or replaces an organization and bumps the version. An
// OrganizationID of 0 mints the next id. Defaults are filled the way the
// registry would: Status "active", CreatedOn now, RetiredOn now for a retired
// row without one, non-nil OrgTypes and Aliases. ExternalIDs is served exactly
// as given (nil = undisclosed, which is what a read key sees). The stored row,
// stamped with the version it drew, is returned.
func (r *Registry) Put(o orgapi.OrganizationDTO) orgapi.OrganizationDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	if o.OrganizationID == 0 {
		o.OrganizationID = r.nextID
	}
	if o.OrganizationID >= r.nextID {
		r.nextID = o.OrganizationID + 1
	}
	if o.Status == "" {
		o.Status = "active"
	}
	if o.CreatedOn.IsZero() {
		o.CreatedOn = r.now()
	}
	if o.Status == "retired" && o.RetiredOn == nil {
		t := r.now()
		o.RetiredOn = &t
	}
	if o.Status != "retired" {
		o.RetiredOn = nil
	}
	if o.OrgTypes == nil {
		o.OrgTypes = []string{}
	}
	if o.Aliases == nil {
		o.Aliases = []orgapi.AliasDTO{}
	}
	r.version++
	o.Version = r.version
	r.orgs[o.OrganizationID] = o
	return o
}

// Remove hard-deletes an organization the way an owner-side DELETE would: the
// row vanishes, an organization tombstone is written, and every client row it
// had gets a client tombstone (one version bump each, like the triggers).
func (r *Registry) Remove(id int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.orgs[id]; !ok {
		return false
	}
	delete(r.orgs, id)
	r.version++
	r.orgTombstones = append(r.orgTombstones, orgapi.RemovedOrganizationDTO{
		OrganizationID: id, Version: r.version, RemovedOn: r.now(),
	})
	for _, cid := range r.clientIDs[id] {
		r.version++
		r.clientTombstones = append(r.clientTombstones, orgapi.RemovedClientDTO{
			ClientID: cid, OrganizationID: id, Version: r.version, RemovedOn: r.now(),
		})
	}
	delete(r.clientIDs, id)
	return true
}

// SetClientIDs sets the live phonelog IDs of an organization (its rows in the
// client list). The organization is touched (its version moves) and every id
// that disappears gets a client tombstone. Panics if the organization does not
// exist — that is a test bug, not a registry state.
func (r *Registry) SetClientIDs(orgID int64, ids ...int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orgs[orgID]
	if !ok {
		panic(fmt.Sprintf("orgapitest: SetClientIDs(%d): no such organization", orgID))
	}
	keep := map[int64]bool{}
	for _, id := range ids {
		keep[id] = true
	}
	r.version++
	o.Version = r.version
	r.orgs[orgID] = o
	for _, old := range r.clientIDs[orgID] {
		if !keep[old] {
			r.version++
			r.clientTombstones = append(r.clientTombstones, orgapi.RemovedClientDTO{
				ClientID: old, OrganizationID: orgID, Version: r.version, RemovedOn: r.now(),
			})
		}
	}
	sorted := append([]int64(nil), ids...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	r.clientIDs[orgID] = sorted
}

// Get returns one organization.
func (r *Registry) Get(id int64) (orgapi.OrganizationDTO, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orgs[id]
	return o, ok
}

// All returns every organization, ordered by id.
func (r *Registry) All() []orgapi.OrganizationDTO {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sortedOrgs(0, "")
}

// ResetTo simulates the registry being restored from a dump taken at an
// earlier version: the counter drops to version, rows stamped later are
// clamped to it, and tombstones written after it vanish. A consumer holding a
// higher version then gets 409 on its next delta.
func (r *Registry) ResetTo(version int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version = version
	for id, o := range r.orgs {
		if o.Version > version {
			o.Version = version
			r.orgs[id] = o
		}
	}
	r.orgTombstones = filterOrgTombstones(r.orgTombstones, version)
	r.clientTombstones = filterClientTombstones(r.clientTombstones, version)
}

// SetDown makes every endpoint answer 503 (an outage) until called with false.
func (r *Registry) SetDown(down bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.down = down
}

// Requests returns every call answered so far, oldest first.
func (r *Registry) Requests() []Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Request(nil), r.requests...)
}

// --- handlers -------------------------------------------------------------

type problemBody struct {
	Schema string `json:"$schema"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func (r *Registry) record(req *http.Request) {
	r.requests = append(r.requests, Request{
		Method:      req.Method,
		Path:        req.URL.Path,
		RawQuery:    req.URL.RawQuery,
		IfNoneMatch: req.Header.Get("If-None-Match"),
		APIKey:      req.Header.Get(orgapi.HeaderAPIKey),
	})
}

// gate runs the checks every endpoint shares; false means an error was written.
func (r *Registry) gate(w http.ResponseWriter, req *http.Request) bool {
	if r.down {
		problem(w, req, http.StatusServiceUnavailable, "registry unavailable (orgapitest.SetDown)")
		return false
	}
	key := req.Header.Get(orgapi.HeaderAPIKey)
	switch {
	case key == "":
		problem(w, req, http.StatusUnauthorized, "missing "+orgapi.HeaderAPIKey+" header")
		return false
	case r.key != "" && key != r.key:
		problem(w, req, http.StatusUnauthorized, "invalid or revoked API key")
		return false
	}
	return true
}

func (r *Registry) handleVersion(w http.ResponseWriter, req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record(req)
	if !r.gate(w, req) {
		return
	}
	if r.notModified(w, req) {
		return
	}
	body := struct {
		Schema string `json:"$schema"`
		orgapi.ListVersionDTO
	}{schemaLink(req, "ListVersionDTO"), orgapi.ListVersionDTO{SchemaVersion: r.schema, Version: r.version}}
	r.writeList(w, body)
}

func (r *Registry) handleList(w http.ResponseWriter, req *http.Request, clients bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.record(req)
	if !r.gate(w, req) {
		return
	}
	q := req.URL.Query()
	var since int64
	if s := q.Get("since"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n < 0 {
			problem(w, req, http.StatusUnprocessableEntity, "validation failed: since must be an integer >= 0")
			return
		}
		since = n
	}
	status := q.Get("status")
	if status != "" && status != "active" && status != "retired" {
		problem(w, req, http.StatusUnprocessableEntity, "validation failed: status must be active or retired")
		return
	}
	if since > 0 && status != "" {
		problem(w, req, http.StatusUnprocessableEntity, "since cannot be combined with status: a delta describes the whole list")
		return
	}
	if r.notModified(w, req) {
		return
	}
	if since > r.version {
		problem(w, req, http.StatusConflict, fmt.Sprintf(
			"since=%d is ahead of the current version %d: the registry was restored or reset — drop the mirror and load the full list",
			since, r.version))
		return
	}

	meta := orgapi.ListMeta{SchemaVersion: r.schema, Version: r.version}
	if since > 0 {
		meta.Since = since
	}
	if clients {
		body := struct {
			Schema string `json:"$schema"`
			orgapi.ClientList
		}{schemaLink(req, "ClientsListOutputBody"), orgapi.ClientList{
			ListMeta: meta,
			Clients:  r.clientRows(since, status),
			Removed:  []orgapi.RemovedClientDTO{},
		}}
		if since > 0 {
			body.Removed = filterClientTombstonesAfter(r.clientTombstones, since)
		}
		r.writeList(w, body)
		return
	}
	body := struct {
		Schema string `json:"$schema"`
		orgapi.OrganizationList
	}{schemaLink(req, "OrganizationsListOutputBody"), orgapi.OrganizationList{
		ListMeta:      meta,
		Organizations: r.sortedOrgs(since, status),
		Removed:       []orgapi.RemovedOrganizationDTO{},
	}}
	if since > 0 {
		body.Removed = filterOrgTombstonesAfter(r.orgTombstones, since)
	}
	r.writeList(w, body)
}

// notModified answers 304 when If-None-Match names the current version.
func (r *Registry) notModified(w http.ResponseWriter, req *http.Request) bool {
	inm := req.Header.Get("If-None-Match")
	if inm == "" || !orgapi.ETagMatches(inm, r.version) {
		return false
	}
	w.Header().Set("ETag", orgapi.ETag(r.version))
	w.Header().Set("Cache-Control", orgapi.CacheControl)
	w.WriteHeader(http.StatusNotModified)
	return true
}

func (r *Registry) writeList(w http.ResponseWriter, body any) {
	data, err := json.Marshal(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", orgapi.ETag(r.version))
	w.Header().Set("Cache-Control", orgapi.CacheControl)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (r *Registry) sortedOrgs(since int64, status string) []orgapi.OrganizationDTO {
	out := make([]orgapi.OrganizationDTO, 0, len(r.orgs))
	for _, o := range r.orgs {
		if o.Version > since && (status == "" || o.Status == status) {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OrganizationID < out[j].OrganizationID })
	return out
}

func (r *Registry) clientRows(since int64, status string) []orgapi.ClientDTO {
	out := []orgapi.ClientDTO{}
	for _, o := range r.sortedOrgs(since, status) {
		for _, cid := range r.clientIDs[o.OrganizationID] {
			out = append(out, orgapi.ClientDTO{ClientID: cid, OrganizationDTO: o})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ClientID != out[j].ClientID {
			return out[i].ClientID < out[j].ClientID
		}
		return out[i].OrganizationID < out[j].OrganizationID
	})
	return out
}

func problem(w http.ResponseWriter, req *http.Request, status int, detail string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(problemBody{
		Schema: schemaLink(req, "ErrorModel"),
		Title:  http.StatusText(status),
		Status: status,
		Detail: detail,
	})
}

// schemaLink imitates huma's "$schema" property so consumers prove they
// ignore fields they do not know.
func schemaLink(req *http.Request, name string) string {
	scheme := "http"
	if req.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + req.Host + "/schemas/" + name + ".json"
}

func filterOrgTombstones(in []orgapi.RemovedOrganizationDTO, upTo int64) []orgapi.RemovedOrganizationDTO {
	out := make([]orgapi.RemovedOrganizationDTO, 0, len(in))
	for _, t := range in {
		if t.Version <= upTo {
			out = append(out, t)
		}
	}
	return out
}

func filterOrgTombstonesAfter(in []orgapi.RemovedOrganizationDTO, since int64) []orgapi.RemovedOrganizationDTO {
	out := make([]orgapi.RemovedOrganizationDTO, 0)
	for _, t := range in {
		if t.Version > since {
			out = append(out, t)
		}
	}
	return out
}

func filterClientTombstones(in []orgapi.RemovedClientDTO, upTo int64) []orgapi.RemovedClientDTO {
	out := make([]orgapi.RemovedClientDTO, 0, len(in))
	for _, t := range in {
		if t.Version <= upTo {
			out = append(out, t)
		}
	}
	return out
}

func filterClientTombstonesAfter(in []orgapi.RemovedClientDTO, since int64) []orgapi.RemovedClientDTO {
	out := make([]orgapi.RemovedClientDTO, 0)
	for _, t := range in {
		if t.Version > since {
			out = append(out, t)
		}
	}
	return out
}
