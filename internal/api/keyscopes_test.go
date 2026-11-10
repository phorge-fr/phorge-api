package api

import (
	"net/http"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
)

// What the scopes of a key let it do on the routes, with the tuples the ApiKey composition writes for them (one per scope and tenant):
// the key passes the routes of its scopes and only those.
func TestAKeyWithTheScopesOfTheNetworksTheProfilesAndTheTenantPassesTheirRoutes(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", readyNetwork("lan"))
	e.profiles.put("demo", madeProfile("web", 1, 1024, 10))
	e.images.put("demo", readyImage("debian"))
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		// the networks
		{"GET", "/v1/tenants/demo/networks", "", http.StatusOK},
		{"GET", "/v1/tenants/demo/networks/lan", "", http.StatusOK},
		{"POST", "/v1/tenants/demo/networks", `{"name":"two"}`, http.StatusAccepted},
		{"DELETE", "/v1/tenants/demo/networks/two", "", http.StatusAccepted},
		// the profiles
		{"GET", "/v1/tenants/demo/profiles", "", http.StatusOK},
		{"POST", "/v1/tenants/demo/profiles", `{"name":"api","cpu":1,"memoryMiB":1024,"diskGiB":10}`, http.StatusAccepted},
		{"DELETE", "/v1/tenants/demo/profiles/api", "", http.StatusAccepted},
		// what the scopes do not give
		{"GET", "/v1/tenants/demo/instances", "", http.StatusForbidden},
		{"POST", "/v1/tenants/demo/instances", `{"name":"a","image":"debian","profile":"s"}`, http.StatusForbidden},
		{"GET", "/v1/tenants/demo/images", "", http.StatusForbidden},
		{"POST", "/v1/tenants/demo/images", `{"name":"i","source":{"name":"debian/13/cloud"}}`, http.StatusForbidden},
		{"GET", "/v1/tenants/demo/memberships", "", http.StatusForbidden},
		{"POST", "/v1/tenants/demo/memberships", `{"subject":{"kind":"User","name":"x@example.org"},"role":"viewer"}`, http.StatusForbidden},
		// keys are not managed by a key, and another tenant is not this key's
		{"GET", "/v1/apikeys", "", http.StatusForbidden},
		{"GET", "/v1/tenants/other/networks", "", http.StatusNotFound},
		{"POST", "/v1/tenants/other/networks", `{"name":"x"}`, http.StatusNotFound},
		// the tenant: rename it, and do not choose its plan
		{"PATCH", "/v1/tenants/demo", `{"displayName":"renamed"}`, http.StatusOK},
		{"PATCH", "/v1/tenants/demo", `{"plan":"large"}`, http.StatusForbidden},
		{"POST", "/v1/tenants", `{"name":"fresh","owner":"x@example.org"}`, http.StatusForbidden},
		{"DELETE", "/v1/tenants/demo", "", http.StatusAccepted},
	} {
		if got := call(t, e, c.method, c.path, "svc_net", c.body); got != c.want {
			t.Errorf("%s %s: status %d, want %d", c.method, c.path, got, c.want)
		}
	}
	if len(e.networks.created) != 1 || len(e.profiles.created) != 1 {
		t.Errorf("the key made %d networks and %d profiles, want one of each", len(e.networks.created), len(e.profiles.created))
	}
}

func TestAKeyThatOnlyReadsTheNetworksCannotWriteThem(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", readyNetwork("lan"))
	e.instances.put("demo", compute.Instance{Name: "vm", Networks: []string{"other"}})
	for _, c := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", "/v1/tenants/demo/networks", "", http.StatusOK},
		{"GET", "/v1/tenants/demo/networks/lan", "", http.StatusOK},
		{"POST", "/v1/tenants/demo/networks", `{"name":"two"}`, http.StatusForbidden},
		{"DELETE", "/v1/tenants/demo/networks/lan", "", http.StatusForbidden},
		{"GET", "/v1/tenants/demo/profiles", "", http.StatusForbidden},
		{"PATCH", "/v1/tenants/demo", `{"displayName":"renamed"}`, http.StatusForbidden},
		{"DELETE", "/v1/tenants/demo", "", http.StatusForbidden},
	} {
		if got := call(t, e, c.method, c.path, "svc_netro", c.body); got != c.want {
			t.Errorf("%s %s: status %d, want %d", c.method, c.path, got, c.want)
		}
	}
	if len(e.networks.created) != 0 || len(e.networks.deleted) != 0 {
		t.Errorf("a key that reads wrote: %v %v", e.networks.created, e.networks.deleted)
	}
}
