package api

import (
	"net/http"

	"github.com/phorge-fr/phorge-api/internal/authz"
	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/server"
)

// profileRoutes list the sizes an instance can take. Every tenant has the three profiles s, m and l, created with it; the
// profiles a user makes themselves (phorge-fr/phorge-api#73) will be listed here too.
type profileRoutes struct {
	base
}

func (p *profileRoutes) register(s *server.Server, authn func(http.Handler) http.Handler) {
	s.Handle("GET /v1/tenants/{t}/profiles", authn(p.guard(authz.ReadInstances, http.HandlerFunc(p.list))))
}

type profileBody struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	CPU         int    `json:"cpu"`
	MemoryMiB   int    `json:"memoryMiB"`
	DiskGiB     int    `json:"diskGiB"`
	// Builtin tells the profiles every tenant is created with from the ones a user made.
	Builtin bool `json:"builtin"`
}

func (p *profileRoutes) list(w http.ResponseWriter, _ *http.Request) {
	items := make([]profileBody, 0, len(compute.BuiltinProfiles))
	for _, pr := range compute.BuiltinProfiles {
		items = append(items, profileBody{Name: pr.Name, Description: pr.Description, CPU: pr.CPU, MemoryMiB: pr.MemoryMiB, DiskGiB: pr.DiskGiB, Builtin: pr.Builtin})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// limitsBody is what the API enforces on a request, so a client does not copy the numbers.
type limitsBody struct {
	Instance struct {
		CPU           compute.Limit `json:"cpu"`
		MemoryMiB     compute.Limit `json:"memoryMiB"`
		DiskGiB       compute.Limit `json:"diskGiB"`
		MaxSSHKeys    int           `json:"maxSshPublicKeys"`
		MaxNameLength int           `json:"maxNameLength"`
	} `json:"instance"`
	Network struct {
		MaxNameLength        int `json:"maxNameLength"`
		MaxDescriptionLength int `json:"maxDescriptionLength"`
	} `json:"network"`
}

// limits answers GET /v1/limits. It holds nothing about a tenant: the plan of a tenant is a limit of its own.
func limits(w http.ResponseWriter, _ *http.Request) {
	l := compute.CurrentLimits()
	var b limitsBody
	b.Instance.CPU, b.Instance.MemoryMiB, b.Instance.DiskGiB = l.CPU, l.MemoryMiB, l.DiskGiB
	b.Instance.MaxSSHKeys, b.Instance.MaxNameLength = l.MaxSSHKeys, 63
	b.Network.MaxNameLength, b.Network.MaxDescriptionLength = l.MaxNetworkName, l.MaxNetworkDescription
	writeJSON(w, http.StatusOK, b)
}
