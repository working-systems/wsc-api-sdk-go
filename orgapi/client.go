package orgapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTimeout   = 30 * time.Second
	defaultMaxBody   = 64 << 20 // a full list of a few thousand organizations is well under 10 MiB
	defaultUserAgent = "wsc-sdk-go/orgapi"
	maxProblemBody   = 64 << 10
)

// Client calls the registry's sync endpoints. It is safe for concurrent use
// and performs no retries: a consumer that polls already has a retry cadence.
type Client struct {
	base    *url.URL
	key     string
	http    *http.Client
	ua      string
	maxBody int64
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default http.Client (30 s timeout). Contexts
// still bound each request.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// WithUserAgent replaces the default User-Agent (wsc-sdk-go/orgapi); name the
// consuming service so the registry's access log says who is polling.
func WithUserAgent(ua string) Option { return func(c *Client) { c.ua = ua } }

// WithMaxBodyBytes caps how much of a 200 response is read (default 64 MiB).
func WithMaxBodyBytes(n int64) Option { return func(c *Client) { c.maxBody = n } }

// NewClient returns a client for the registry at baseURL — the service root,
// scheme://host[:port][/prefix], with no /api/v1 — presenting apiKey (a key
// with the read scope is enough for every method here).
func NewClient(baseURL, apiKey string, opts ...Option) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("orgapi: parsing base URL: %w", err)
	}
	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("orgapi: base URL %q must be http(s)://host[:port][/prefix]", baseURL)
	}
	if apiKey == "" {
		return nil, errors.New("orgapi: an API key is required")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath, u.RawQuery, u.Fragment = "", "", ""
	c := &Client{
		base:    u,
		key:     apiKey,
		http:    &http.Client{Timeout: defaultTimeout},
		ua:      defaultUserAgent,
		maxBody: defaultMaxBody,
	}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// BaseURL is the service root the client was built with, normalized.
func (c *Client) BaseURL() string { return c.base.String() }

// ListQuery is the query surface of the two list endpoints.
type ListQuery struct {
	// Since asks for a delta: only organizations changed after this registry
	// version, plus removals since it. 0 means the full list.
	Since int64
	// Status filters the full list to active or retired organizations. The
	// registry refuses it together with Since (a delta describes the whole list).
	Status string
	// IfNoneMatch is the version the caller holds (ETag(v), or a bare integer);
	// the registry answers ErrNotModified while it is still current.
	IfNoneMatch string
}

func (q ListQuery) values() url.Values {
	v := url.Values{}
	if q.Since != 0 {
		v.Set("since", strconv.FormatInt(q.Since, 10))
	}
	if q.Status != "" {
		v.Set("status", q.Status)
	}
	return v
}

// Version reads the registry version behind the organization list. With
// ifNoneMatch set to the version held, it returns ErrNotModified while that
// version is current.
func (c *Client) Version(ctx context.Context, ifNoneMatch string) (ListVersionDTO, error) {
	return c.version(ctx, PathOrganizationsVersion, ifNoneMatch)
}

// ClientsVersion is Version for the client list. Both lists share the one
// registry version; the endpoint exists for symmetry with the list it precedes.
func (c *Client) ClientsVersion(ctx context.Context, ifNoneMatch string) (ListVersionDTO, error) {
	return c.version(ctx, PathClientsVersion, ifNoneMatch)
}

// ListOrganizations fetches the organization list in full or as a delta. In
// delta mode every returned organization is in its complete current state
// (replace your copy of each) and Removed lists the tombstones since. Slices
// are never nil.
func (c *Client) ListOrganizations(ctx context.Context, q ListQuery) (*OrganizationList, error) {
	var list OrganizationList
	etag, err := c.get(ctx, PathOrganizations, q.values(), q.IfNoneMatch, &list)
	if err != nil {
		return nil, err
	}
	if err := checkPayload(PathOrganizations, list.SchemaVersion, list.Version, etag); err != nil {
		return nil, err
	}
	if list.Organizations == nil {
		list.Organizations = []OrganizationDTO{}
	}
	if list.Removed == nil {
		list.Removed = []RemovedOrganizationDTO{}
	}
	return &list, nil
}

// ListClients fetches the client list (organizations seen through their live
// phonelog IDs) in full or as a delta. Slices are never nil.
func (c *Client) ListClients(ctx context.Context, q ListQuery) (*ClientList, error) {
	var list ClientList
	etag, err := c.get(ctx, PathClients, q.values(), q.IfNoneMatch, &list)
	if err != nil {
		return nil, err
	}
	if err := checkPayload(PathClients, list.SchemaVersion, list.Version, etag); err != nil {
		return nil, err
	}
	if list.Clients == nil {
		list.Clients = []ClientDTO{}
	}
	if list.Removed == nil {
		list.Removed = []RemovedClientDTO{}
	}
	return &list, nil
}

func (c *Client) version(ctx context.Context, path, ifNoneMatch string) (ListVersionDTO, error) {
	var v ListVersionDTO
	etag, err := c.get(ctx, path, nil, ifNoneMatch, &v)
	if err != nil {
		return ListVersionDTO{}, err
	}
	if err := checkPayload(path, v.SchemaVersion, v.Version, etag); err != nil {
		return ListVersionDTO{}, err
	}
	return v, nil
}

// checkPayload refuses a payload this client cannot vouch for: a schema tag it
// does not speak, or an ETag that disagrees with the body's version (a proxy
// that mixed two responses must not be applied to a mirror).
func checkPayload(path, schema string, version int64, etag string) error {
	if schema != ListSchema {
		return fmt.Errorf("%w: %s answered schema %q, this client speaks %q", ErrSchema, path, schema, ListSchema)
	}
	if etag != "" {
		if n, ok := ParseETag(etag); !ok || n != version {
			return fmt.Errorf("orgapi: GET %s: ETag %s disagrees with body version %d", path, etag, version)
		}
	}
	return nil
}

// get performs one authenticated GET and decodes a 200 into out. It returns
// ErrNotModified for a 304 and a *StatusError for anything else; the ETag
// header is returned whenever the registry sent one.
func (c *Client) get(ctx context.Context, path string, query url.Values, ifNoneMatch string, out any) (etag string, err error) {
	u := *c.base
	u.Path = c.base.Path + path
	u.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("orgapi: building request: %w", err)
	}
	req.Header.Set(HeaderAPIKey, c.key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.ua)
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("orgapi: GET %s: %w", u.String(), err)
	}
	defer resp.Body.Close()
	etag = resp.Header.Get("ETag")

	switch resp.StatusCode {
	case http.StatusNotModified:
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxProblemBody))
		return etag, ErrNotModified
	case http.StatusOK:
		// Unknown fields are ignored on purpose: huma adds "$schema" to every
		// body, and the registry may add fields without bumping the schema tag.
		dec := json.NewDecoder(io.LimitReader(resp.Body, c.maxBody))
		if err := dec.Decode(out); err != nil {
			return etag, fmt.Errorf("orgapi: GET %s: decoding response: %w", u.String(), err)
		}
		return etag, nil
	default:
		return etag, c.statusError(req, resp)
	}
}

// statusError shapes a non-2xx answer, reading the RFC-7807 body when the
// registry sent one (huma's ErrorModel: title, status, detail).
func (c *Client) statusError(req *http.Request, resp *http.Response) error {
	se := &StatusError{Status: resp.StatusCode, Method: req.Method, URL: req.URL.String()}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxProblemBody))
	var problem struct {
		Title  string `json:"title"`
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &problem) == nil {
		se.Title = problem.Title
		se.Detail = problem.Detail
	}
	return se
}
