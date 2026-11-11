package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/status"
)

const lanPath = "/v1/tenants/demo/networks/lan"

func networkWithRanges(name, ipv4, ipv6 string) compute.Network {
	n := readyNetwork(name)
	n.IPv4, n.IPv6 = ipv4, ipv6
	return n
}

func TestPatchNetworkWhoMayDoWhat(t *testing.T) {
	for who, want := range map[string]int{"viewer": 403, "user": 403, "operator": 202, "admin": 202, "svc": 403, "mallory": 404, "": 401} {
		e := newEnv(t)
		e.networks.put("demo", readyNetwork("lan"))
		if got := call(t, e, "PATCH", lanPath, who, `{"description":"office"}`); got != want {
			t.Errorf("%q: status %d, want %d", who, got, want)
		}
	}
}

func TestPatchNetworkChangesTheDescriptionAndTheRanges(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", networkWithRanges("lan", "10.77.1.0/24", ""))
	rec := e.do(t, "PATCH", lanPath, "operator", `{"description":"office","ipv4":"10.77.2.0/24","ipv6":"fd42:1:2:3::/64"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body networkBody
	mustDecode(t, rec, &body)
	if body.Description != "office" || body.IPv4 != "10.77.2.0/24" || body.IPv6 != "fd42:1:2:3::/64" {
		t.Errorf("body = %+v", body)
	}
	if len(e.networks.updated) != 1 {
		t.Fatalf("the store received %+v", e.networks.updated)
	}
}

func TestPatchNetworkRefusesABadRequest(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", networkWithRanges("lan", "10.77.1.0/24", "fd42:1:2:3::/64"))
	for name, body := range map[string]string{
		"nothing to change":             `{}`,
		"a description too long":        `{"description":"` + strings.Repeat("x", 201) + `"}`,
		"an ipv4 that is not private":   `{"ipv4":"8.8.8.0/24"}`,
		"an ipv4 range that is removed": `{"ipv4":""}`,
		"an ipv4 host address":          `{"ipv4":"10.77.2.5/24"}`,
		"an ipv6 range that is removed": `{"ipv6":""}`,
		"an ipv6 that is not local":     `{"ipv6":"2001:db8:1:2::/64"}`,
		"an ipv6 that is not a /64":     `{"ipv6":"fd42:1:2::/48"}`,
		"an ipv6 host address":          `{"ipv6":"fd42:1:2:3::1/64"}`,
		"an ipv6 that is an ipv4":       `{"ipv6":"10.77.2.0/24"}`,
		"an ipv4 that is an ipv6":       `{"ipv4":"fd42:1:2:3::/64"}`,
	} {
		if code := call(t, e, "PATCH", lanPath, "operator", body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, code)
		}
	}
	for name, body := range map[string]string{
		"a field the kind does not have": `{"nat":false}`,
		"not json":                       `nope`,
	} {
		if code := call(t, e, "PATCH", lanPath, "operator", body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	if code := call(t, e, "PATCH", "/v1/tenants/demo/networks/ghost", "operator", `{"description":"x"}`); code != http.StatusNotFound {
		t.Errorf("a network that does not exist: status %d, want 404", code)
	}
	if len(e.networks.updated) != 0 {
		t.Errorf("something reached the store: %+v", e.networks.updated)
	}
}

func TestPatchNetworkRefusesARangeThatIsTakenOrReserved(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", networkWithRanges("lan", "10.77.1.0/24", "fd42:1:2:3::/64"))
	e.networks.put("demo", networkWithRanges("dmz", "10.77.9.0/24", "fd42:1:2:9::/64"))
	e.networks.put("other", networkWithRanges("lan", "10.88.1.0/24", "fd42:8:8:8::/64"))
	for name, tt := range map[string]struct{ body, code string }{
		"an ipv4 of another network of the tenant": {`{"ipv4":"10.77.9.0/25"}`, "range_in_use"},
		"an ipv4 of another tenant":                {`{"ipv4":"10.88.0.0/16"}`, "range_in_use"},
		"an ipv6 of another network of the tenant": {`{"ipv6":"fd42:1:2:9::/64"}`, "range_in_use"},
		"an ipv6 of another tenant":                {`{"ipv6":"fd42:8:8:8::/64"}`, "range_in_use"},
		"an ipv4 of the platform":                  {`{"ipv4":"10.244.0.0/24"}`, "range_reserved"},
	} {
		rec := e.do(t, "PATCH", lanPath, "operator", tt.body)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), tt.code) {
			t.Errorf("%s: status %d, %s", name, rec.Code, rec.Body)
		}
	}
	if len(e.networks.updated) != 0 {
		t.Errorf("a range that is taken reached the store: %+v", e.networks.updated)
	}
	// Its own range is not another network's: a range that grows over the old one, and the same one again, are fine.
	for name, body := range map[string]string{
		"the range it has": `{"ipv4":"10.77.1.0/24"}`,
		"a bigger range":   `{"ipv4":"10.77.0.0/21"}`,
		"the ipv6 it has":  `{"ipv6":"fd42:1:2:3::/64"}`,
	} {
		if code := call(t, e, "PATCH", lanPath, "operator", body); code != http.StatusAccepted {
			t.Errorf("%s: status %d, want 202", name, code)
		}
	}
}

func TestPatchNetworkIPv4IsRefusedWhileAnInstanceRunsOnItAndNothingIsStopped(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", networkWithRanges("lan", "10.77.1.0/24", ""))
	e.instances.put("demo", compute.Instance{Name: "web", Networks: []string{"lan"}, State: "Running", Running: true})
	e.instances.put("demo", compute.Instance{Name: "db", Networks: []string{"net", "lan"}, State: "Running", Running: true})
	e.instances.put("demo", compute.Instance{Name: "idle", Networks: []string{"lan"}, State: "Stopped"})
	e.instances.put("demo", compute.Instance{Name: "elsewhere", Networks: []string{"net"}, State: "Running", Running: true})
	rec := e.do(t, "PATCH", lanPath, "operator", `{"ipv4":"10.77.2.0/24"}`)
	body := rec.Body.String()
	if rec.Code != http.StatusConflict || !strings.Contains(body, `"running"`) || !strings.Contains(body, "db, web") ||
		strings.Contains(body, "idle") || strings.Contains(body, "elsewhere") {
		t.Fatalf("status %d: the refusal must name the running instances of that network, and only them: %s", rec.Code, body)
	}
	if len(e.networks.updated) != 0 {
		t.Errorf("the range was changed under running instances: %+v", e.networks.updated)
	}
	// Nothing is stopped for the user.
	if len(e.instances.patched) != 0 {
		t.Errorf("an instance was changed: %+v", e.instances.patched)
	}
	// What cuts nobody goes through while they run: the same range, the IPv6, the description.
	for name, body := range map[string]string{
		"the range it has": `{"ipv4":"10.77.1.0/24"}`,
		"an ipv6":          `{"ipv6":"fd42:1:2:3::/64"}`,
		"a description":    `{"description":"office"}`,
	} {
		if code := call(t, e, "PATCH", lanPath, "operator", body); code != http.StatusAccepted {
			t.Errorf("%s: status %d, want 202", name, code)
		}
	}
	// Once they are stopped, the range changes: they take it at their next start.
	for _, name := range []string{"web", "db"} {
		off := false
		if code := call(t, e, "PATCH", "/v1/tenants/demo/instances/"+name, "operator", `{"running":false}`); code != http.StatusOK {
			t.Fatalf("stop %s: status %d (%v)", name, code, off)
		}
		in, _ := e.instances.Get(t.Context(), "demo", name)
		in.State = "Stopped"
		e.instances.put("demo", in)
	}
	if code := call(t, e, "PATCH", lanPath, "operator", `{"ipv4":"10.77.2.0/24"}`); code != http.StatusAccepted {
		t.Errorf("no instance runs on it any more: status %d, want 202", code)
	}
}

func TestPatchNetworkWhoseRangeIsUnknownIsTreatedAsAChangeOfRange(t *testing.T) {
	// Incus chose the range and could not be asked: the API cannot tell that the range is the same, so a running instance still blocks.
	e := newEnv(t)
	e.networks.put("demo", readyNetwork("lan"))
	e.instances.put("demo", compute.Instance{Name: "web", Networks: []string{"lan"}, State: "Running", Running: true})
	if code := call(t, e, "PATCH", lanPath, "operator", `{"ipv4":"10.77.2.0/24"}`); code != http.StatusConflict {
		t.Errorf("status %d, want 409", code)
	}
}

func TestPatchNetworkBeingDeletedIsRefused(t *testing.T) {
	e := newEnv(t)
	n := readyNetwork("lan")
	n.Status = status.Object{Deleting: true}
	e.networks.put("demo", n)
	if code := call(t, e, "PATCH", lanPath, "operator", `{"description":"x"}`); code != http.StatusConflict {
		t.Errorf("status %d, want 409", code)
	}
}

func TestCreateNetworkTakesAnIPv6(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"lan","ipv6":"fd42:1:2:3::/64"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body networkBody
	mustDecode(t, rec, &body)
	if body.IPv6 != "fd42:1:2:3::/64" {
		t.Errorf("ipv6 = %q", body.IPv6)
	}
	if code := call(t, e, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"two","ipv6":"fd42:1:2:3::/64"}`); code != http.StatusConflict {
		t.Errorf("the same ipv6 twice: status %d, want 409", code)
	}
	if code := call(t, e, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"three","ipv6":"2001:db8::/64"}`); code != http.StatusUnprocessableEntity {
		t.Errorf("a global ipv6: status %d, want 422", code)
	}
}

func TestParseRange6AndGateway6(t *testing.T) {
	for in, want := range map[string]string{
		"fd42:1:2:3::/64":      "fd42:1:2:3::1/64",
		"fd42:0:0:1::/64":      "fd42:0:0:1::1/64",
		"fd42::/64":            "fd42:0:0:0::1/64",
		"fdab:cdef:12:34::/64": "fdab:cdef:12:34::1/64",
	} {
		p, err := compute.ParseRange6(in)
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if got := compute.Gateway6(p); got != want {
			t.Errorf("Gateway6(%s) = %s, want %s", in, got, want)
		}
	}
}
