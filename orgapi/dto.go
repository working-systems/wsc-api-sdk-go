// Package orgapi is the client-side view of the WSC Organization API
// (wsc-api-organization): the wire types its list endpoints publish, the
// ETag conventions of its sync protocol, and an HTTP client for the four
// sync endpoints. It depends on the standard library only.
//
// The DTOs here ARE the server's. wsc-api-organization imports them for its
// own handlers, so a field or tag change here is a wire-contract change that
// surfaces in the server's committed OpenAPI spec (its treaty test fails until
// the spec is regenerated). Two rules follow from how huma builds that spec:
// never rename an exported type (schema components are named after Go type
// names), and never add a Go field named Schema (huma injects "$schema" into
// every response body itself and refuses a type that already has one — the
// payload schema tag is therefore SchemaVersion with json:"schema").
package orgapi

import "time"

const (
	// ListSchema names the shape of the list payloads. It changes only when
	// the list payloads change incompatibly; additive fields do not bump it.
	ListSchema = "v1"

	// HeaderAPIKey is the registry's sole authentication mechanism: one static
	// header, which Delphi 10.2's THTTPClient sets in one line.
	HeaderAPIKey = "X-API-Key"

	// CacheControl is what the registry sends on every list response: per-key
	// and always revalidated — the version check it revalidates against is one
	// indexed single-row read on the server.
	CacheControl = "private, no-cache"
)

// Paths of the sync endpoints, relative to the service root
// (scheme://host[:port]).
const (
	PathOrganizations        = "/api/v1/organizations"
	PathOrganizationsVersion = "/api/v1/organizations/version"
	PathClients              = "/api/v1/clients"
	PathClientsVersion       = "/api/v1/clients/version"
)

// OrganizationDTO is one organization as the registry publishes it.
type OrganizationDTO struct {
	OrganizationID int64      `json:"organizationId" doc:"Minted by this registry — the identity everything else hangs off"`
	UnionCode      string     `json:"unionCode"`
	LocalNumber    string     `json:"localNumber"`
	DisplayName    string     `json:"displayName"`
	Timezone       string     `json:"timezone" doc:"IANA time zone name (e.g. America/New_York); empty when unknown"`
	Status         string     `json:"status" enum:"active,retired"`
	RetiredOn      *time.Time `json:"retiredOn,omitempty"`
	CreatedOn      time.Time  `json:"createdOn"`
	Version        int64      `json:"version" doc:"Registry version at which this organization — or any of its org types, aliases, or external IDs — last changed"`
	OrgTypes       []string   `json:"orgTypes" doc:"Live classification codes (registry: /org-types); an organization can be several things at once"`
	Aliases        []AliasDTO `json:"aliases" doc:"Live alternate names; the displayName search filter matches these too"`
	// ExternalIDs is disclosure-gated, not just data: null means the key lacks
	// read_external_id; an empty array means the organization has none.
	ExternalIDs []ExternalIDDTO `json:"externalIds" doc:"Live external IDs. null = not disclosed (key lacks read_external_id); empty array = none."`
}

// AliasDTO is an alternate name of an organization. display_name stays the
// one primary name; aliases catch the expanded/contracted spellings, former
// names, and DBAs.
type AliasDTO struct {
	AliasID   int64      `json:"aliasId" doc:"Addresses this alias in DELETE"`
	Alias     string     `json:"alias" doc:"Stored verbatim; matched through the same normalization as displayName"`
	CreatedOn time.Time  `json:"createdOn"`
	RetiredOn *time.Time `json:"retiredOn,omitempty"`
}

// ExternalIDDTO is one identifier another system uses for an organization
// (PHONELOG's CLIENT_ID lives in the seeded "phonelog" system).
type ExternalIDDTO struct {
	MappingID      int64      `json:"mappingId" doc:"Addresses this mapping in PATCH/DELETE"`
	OrganizationID int64      `json:"organizationId"`
	SystemCode     string     `json:"systemCode"`
	ExternalID     string     `json:"externalId" doc:"Normalized per the system's rules — what search compares"`
	ExternalIDRaw  string     `json:"externalIdRaw" doc:"Exactly as received"`
	IsPrimary      bool       `json:"isPrimary"`
	CreatedOn      time.Time  `json:"createdOn"`
	RetiredOn      *time.Time `json:"retiredOn,omitempty"`
}

// ClientDTO is an organization seen through one of its phonelog IDs. It
// embeds the organization shape wholesale — the lens adds clientId, nothing
// else. Only organizations carrying a live phonelog ID appear in the client
// list, one row per ID.
type ClientDTO struct {
	ClientID int64 `json:"clientId" doc:"PHONELOG CLIENT_ID — an external ID in the seeded phonelog system"`
	OrganizationDTO
}

// ListVersionDTO is the sync handshake: "what version are you at?"
type ListVersionDTO struct {
	SchemaVersion string `json:"schema" doc:"Payload schema version (currently v1); changes only when the list shape changes incompatibly"`
	Version       int64  `json:"version" doc:"Registry version: increases on every change to any organization, its types, aliases, or external IDs. Repeated as the ETag."`
}

// ListMeta heads every list payload.
type ListMeta struct {
	SchemaVersion string `json:"schema" doc:"Payload schema version (currently v1)"`
	Version       int64  `json:"version" doc:"Registry version this payload describes, read in the same database snapshot as the rows. Send it back as If-None-Match or since."`
	Since         int64  `json:"since,omitempty" doc:"Delta mode only: the version the changes are relative to"`
}

// RemovedOrganizationDTO is a tombstone in the organizations list: the row
// itself is gone (an owner-side hard delete — the API only ever retires).
type RemovedOrganizationDTO struct {
	OrganizationID int64     `json:"organizationId"`
	Version        int64     `json:"version" doc:"Registry version at which it was removed"`
	RemovedOn      time.Time `json:"removedOn"`
}

// RemovedClientDTO is a tombstone in the clients list: this CLIENT_ID stopped
// being a live phonelog ID of this organization (retired, re-valued, moved, or
// deleted), so the lens row vanished. The organization itself may well still
// exist.
type RemovedClientDTO struct {
	ClientID       int64     `json:"clientId"`
	OrganizationID int64     `json:"organizationId"`
	Version        int64     `json:"version" doc:"Registry version at which it was removed"`
	RemovedOn      time.Time `json:"removedOn"`
}

// OrganizationList is the body of GET /api/v1/organizations, in full or delta
// mode. The server embeds it in an anonymous body struct so its schema keeps
// the component name huma derived before this type existed.
type OrganizationList struct {
	ListMeta
	Organizations []OrganizationDTO        `json:"organizations" doc:"Full mode: every organization. Delta mode: the current state of each organization changed since — replace your copy of each."`
	Removed       []RemovedOrganizationDTO `json:"removed" doc:"Organizations removed since (delta mode; always empty in full mode)"`
}

// ClientList is the body of GET /api/v1/clients, in full or delta mode.
type ClientList struct {
	ListMeta
	Clients []ClientDTO        `json:"clients" doc:"Full mode: every client. Delta mode: the current lens rows of each organization changed since — replace your copy of each."`
	Removed []RemovedClientDTO `json:"removed" doc:"Client rows that vanished since (delta mode; always empty in full mode)"`
}
