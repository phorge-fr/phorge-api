package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/phorge-fr/phorge-api/internal/compute"
	"github.com/phorge-fr/phorge-api/internal/status"
)

func readyNetwork(name string) compute.Network {
	return compute.Network{Name: name, CreatedAt: now, Status: status.Object{Ready: true, NotReadySince: now}}
}

func TestNetworksWhoMayDoWhat(t *testing.T) {
	const coll, item = "/v1/tenants/demo/networks", "/v1/tenants/demo/networks/lan"
	create := `{"name":"new"}`
	tests := []struct {
		who                       string
		list, get, create, delete int
	}{
		{"viewer", 200, 200, 403, 403},
		{"user", 200, 200, 403, 403},
		{"operator", 200, 200, 202, 202},
		{"admin", 200, 200, 202, 202},
		// A key has no scope for networks yet, whatever else it holds (phorge-fr/phorge-api#74).
		{"svc", 403, 403, 403, 403},
		{"svc_mgr", 403, 403, 403, 403},
		{"mallory", 404, 404, 404, 404},
		{"", 401, 401, 401, 401},
	}
	for _, tt := range tests {
		t.Run(tt.who, func(t *testing.T) {
			e := newEnv(t)
			e.networks.put("demo", readyNetwork("lan"))
			for _, c := range []struct {
				method, path, body string
				want               int
			}{
				{"GET", coll, "", tt.list}, {"GET", item, "", tt.get}, {"POST", coll, create, tt.create}, {"DELETE", item, "", tt.delete},
			} {
				if got := call(t, e, c.method, c.path, tt.who, c.body); got != c.want {
					t.Errorf("%s %s: status %d, want %d", c.method, c.path, got, c.want)
				}
			}
		})
	}
}

func TestNetworksOfAnotherTenantAreNotReachable(t *testing.T) {
	e := newEnv(t)
	e.networks.put("other", readyNetwork("secret"))
	// alice reads demo and nothing else: another tenant's networks are not shown to exist.
	if code := call(t, e, "GET", "/v1/tenants/other/networks/secret", "alice", ""); code != http.StatusNotFound {
		t.Errorf("status %d, want 404", code)
	}
	if body := e.do(t, "GET", "/v1/tenants/demo/networks", "alice", "").Body.String(); strings.Contains(body, "secret") {
		t.Errorf("a network of another tenant is listed: %s", body)
	}
}

func TestCreateNetwork(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"lan","description":"the office"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := rec.Header().Get("Location"); got != "/v1/tenants/demo/networks/lan" {
		t.Errorf("Location = %q", got)
	}
	var body networkBody
	mustDecode(t, rec, &body)
	if body.Name != "lan" || body.Description != "the office" || body.Phase != status.Provisioning || body.CreatedBy != "user:operator@example.org" {
		t.Errorf("body = %+v", body)
	}
	if len(e.networks.created) != 1 || e.networks.created[0].Name != "lan" {
		t.Errorf("the store received %+v", e.networks.created)
	}
	if code := call(t, e, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"lan"}`); code != http.StatusConflict {
		t.Errorf("the same name twice: status %d, want 409", code)
	}
}

func TestCreateNetworkRefusesABadRequest(t *testing.T) {
	e := newEnv(t)
	for name, body := range map[string]string{
		"no name":                `{}`,
		"uppercase":              `{"name":"Lan"}`,
		"a digit first":          `{"name":"1lan"}`,
		"16 characters":          `{"name":"abcdefghijklmnop"}`,
		"ends with a hyphen":     `{"name":"lan-"}`,
		"an underscore":          `{"name":"my_lan"}`,
		"a description too long": `{"name":"lan","description":"` + strings.Repeat("x", 201) + `"}`,
	} {
		if code := call(t, e, "POST", "/v1/tenants/demo/networks", "operator", body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, code)
		}
	}
	for name, body := range map[string]string{
		"a field the kind does not have: nat":    `{"name":"lan","nat":false}`,
		"a field the kind does not have: subnet": `{"name":"lan","subnet":"10.0.0.0/24"}`,
		"not json":                               `nope`,
	} {
		if code := call(t, e, "POST", "/v1/tenants/demo/networks", "operator", body); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	if len(e.networks.created) != 0 {
		t.Errorf("something reached the store: %+v", e.networks.created)
	}
	if code := call(t, e, "POST", "/v1/tenants/ghost/networks", "admin", `{"name":"lan"}`); code != http.StatusNotFound {
		t.Errorf("a tenant that does not exist: status %d, want 404", code)
	}
}

func TestDeleteNetwork(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", readyNetwork("lan"))
	rec := e.do(t, "DELETE", "/v1/tenants/demo/networks/lan", "operator", "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body networkBody
	mustDecode(t, rec, &body)
	if body.Phase != status.Deleting {
		t.Errorf("phase = %s, want Deleting", body.Phase)
	}
	if code := call(t, e, "DELETE", "/v1/tenants/demo/networks/ghost", "operator", ""); code != http.StatusNotFound {
		t.Errorf("a network that does not exist: status %d, want 404", code)
	}
}

func TestDeleteNetworkInUseNamesTheInstances(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", readyNetwork("lan"))
	e.instances.put("demo", compute.Instance{Name: "web", Network: "lan"})
	e.instances.put("demo", compute.Instance{Name: "db", Network: "lan"})
	e.instances.put("demo", compute.Instance{Name: "elsewhere", Network: "net"})
	rec := e.do(t, "DELETE", "/v1/tenants/demo/networks/lan", "operator", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"in_use"`) || !strings.Contains(body, "db, web") || strings.Contains(body, "elsewhere") {
		t.Errorf("the refusal must name the instances on that network, and only them: %s", body)
	}
	if len(e.networks.deleted) != 0 {
		t.Errorf("a network in use was deleted: %v", e.networks.deleted)
	}
}

func TestANetworkShowsAPhaseAndNoCrossplaneCondition(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", compute.Network{Name: "broken", CreatedAt: now, Status: status.Object{
		NotReadySince: now.Add(-time.Hour),
		Problems:      "network Ready: not ready | network Synced: create failed: apply failed: Failed to create network \"broken\": Networks limit has been reached for project",
	}})
	var body networkBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/networks/broken", "viewer", ""), &body)
	if body.Phase != status.Failed {
		t.Errorf("phase = %s", body.Phase)
	}
	for _, leak := range []string{"Synced", "apply failed", "Failed to create", "network Ready"} {
		if strings.Contains(body.Problems, leak) {
			t.Errorf("%q reaches the client: %q", leak, body.Problems)
		}
	}
	if !strings.Contains(body.Problems, "number of networks") {
		t.Errorf("the limit of the plan is told in words: %q", body.Problems)
	}
}

func TestCreateInstanceTakesTheOnlyNetworkOfTheTenant(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", `{"name":"web","image":"debian"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(e.instances.created) != 1 || e.instances.created[0].Network != "net" {
		t.Fatalf("created %+v: the only network is used", e.instances.created)
	}
	var body instanceBody
	mustDecode(t, rec, &body)
	if body.Network != "net" {
		t.Errorf("body network = %q", body.Network)
	}
}

func TestCreateInstanceNamesItsNetwork(t *testing.T) {
	e := newEnv(t)
	e.images.put("demo", readyImage("debian"))
	e.networks.put("demo", readyNetwork("lan"))
	if code := call(t, e, "POST", "/v1/tenants/demo/instances", "user", `{"name":"web","image":"debian","network":"lan"}`); code != http.StatusAccepted {
		t.Fatalf("status %d", code)
	}
	if got := e.instances.created[0].Network; got != "lan" {
		t.Errorf("network = %q, want the one asked for", got)
	}
}

func TestCreateInstanceNeedsAnUnambiguousReadyNetwork(t *testing.T) {
	const create = `{"name":"web","image":"debian"}`
	t.Run("none", func(t *testing.T) {
		e := newEnv(t)
		e.images.put("demo", readyImage("debian"))
		delete(e.networks.data["demo"], "net")
		rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", create)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "no network: create one first") {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
	t.Run("several and none named", func(t *testing.T) {
		e := newEnv(t)
		e.images.put("demo", readyImage("debian"))
		e.networks.put("demo", readyNetwork("lan"))
		rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", create)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "lan, net") {
			t.Errorf("status %d: %s: the choice is theirs, and the networks are listed", rec.Code, rec.Body)
		}
	})
	t.Run("named and unknown", func(t *testing.T) {
		e := newEnv(t)
		e.images.put("demo", readyImage("debian"))
		rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", `{"name":"web","image":"debian","network":"ghost"}`)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "network ghost does not exist") {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
	t.Run("named and not ready", func(t *testing.T) {
		e := newEnv(t)
		e.images.put("demo", readyImage("debian"))
		e.networks.put("demo", compute.Network{Name: "lan", CreatedAt: now, Status: status.Object{NotReadySince: now}})
		rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", `{"name":"web","image":"debian","network":"lan"}`)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "network_not_ready") {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
	t.Run("the only one and not ready", func(t *testing.T) {
		e := newEnv(t)
		e.images.put("demo", readyImage("debian"))
		e.networks.put("demo", compute.Network{Name: "net", CreatedAt: now, Status: status.Object{NotReadySince: now}})
		rec := e.do(t, "POST", "/v1/tenants/demo/instances", "user", create)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "network_not_ready") {
			t.Errorf("status %d: %s", rec.Code, rec.Body)
		}
	})
	t.Run("a bad name", func(t *testing.T) {
		e := newEnv(t)
		e.images.put("demo", readyImage("debian"))
		if code := call(t, e, "POST", "/v1/tenants/demo/instances", "user", `{"name":"web","image":"debian","network":"Bad_Name"}`); code != http.StatusUnprocessableEntity {
			t.Errorf("status %d, want 422", code)
		}
	})
}

// The XRD has no default size: the API completes what the request left out, so a client that sends no size keeps getting the
// instance it always got.
func TestCreateInstanceCompletesItsSize(t *testing.T) {
	for name, tt := range map[string]struct {
		body          string
		cpu, mem, dsk int
	}{
		"no size":     {`{"name":"a","image":"debian"}`, 1, 1024, 10},
		"only a cpu":  {`{"name":"b","image":"debian","cpu":4}`, 4, 1024, 10},
		"only a disk": {`{"name":"c","image":"debian","diskGiB":50}`, 1, 1024, 50},
		"all three":   {`{"name":"d","image":"debian","cpu":2,"memoryMiB":2048,"diskGiB":20}`, 2, 2048, 20},
	} {
		// a tenant of its own for each case: a plan holds two instances
		e := newEnv(t)
		e.images.put("demo", readyImage("debian"))
		if code := call(t, e, "POST", "/v1/tenants/demo/instances", "user", tt.body); code != http.StatusAccepted {
			t.Fatalf("%s: status %d", name, code)
		}
		got := e.instances.created[len(e.instances.created)-1]
		if *got.CPU != tt.cpu || *got.MemoryMiB != tt.mem || *got.DiskGiB != tt.dsk {
			t.Errorf("%s: sent %d/%d/%d, want %d/%d/%d", name, *got.CPU, *got.MemoryMiB, *got.DiskGiB, tt.cpu, tt.mem, tt.dsk)
		}
	}
}

func TestCreateNetworkAcceptsTheRangeTheUserChooses(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"lan","ipv4":"10.77.1.0/24"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body networkBody
	mustDecode(t, rec, &body)
	if body.IPv4 != "10.77.1.0/24" {
		t.Errorf("ipv4 = %q, want the range back", body.IPv4)
	}
	if got := e.networks.created[0].IPv4; got != "10.77.1.0/24" {
		t.Errorf("the store received %q", got)
	}
	// Without one, Incus chooses and nothing is shown until that choice is reported back.
	rec = e.do(t, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"auto"}`)
	raw := rec.Body.String()
	body = networkBody{}
	mustDecode(t, rec, &body)
	if body.IPv4 != "" || strings.Contains(raw, "ipv4") {
		t.Errorf("an ipv4 is shown that nobody chose: %s", raw)
	}
}

func TestCreateNetworkRefusesABadRange(t *testing.T) {
	e := newEnv(t)
	for name, ipv4 := range map[string]string{
		"not an address":          "lan",
		"an address, no prefix":   "10.77.1.0",
		"a host, not the network": "10.77.1.5/24",
		"the gateway form":        "10.77.1.1/24",
		"IPv6":                    "fd00::/64",
		"a public range":          "8.8.8.0/24",
		"too big":                 "10.0.0.0/8",
		"too small":               "10.77.1.0/30",
	} {
		body := `{"name":"lan","ipv4":"` + ipv4 + `"}`
		if code := call(t, e, "POST", "/v1/tenants/demo/networks", "operator", body); code != http.StatusUnprocessableEntity {
			t.Errorf("%s: status %d, want 422", name, code)
		}
	}
	if len(e.networks.created) != 0 {
		t.Errorf("something reached the store: %+v", e.networks.created)
	}
}

func TestCreateNetworkKeepsTenantsAwayFromThePlatformRanges(t *testing.T) {
	e := newEnv(t)
	for _, ipv4 := range []string{"10.1.0.0/24", "10.10.0.0/16", "192.168.1.0/24", "10.4.0.0/22"} {
		rec := e.do(t, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"lan","ipv4":"`+ipv4+`"}`)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "range_reserved") {
			t.Errorf("%s: status %d: %s, want 409 range_reserved", ipv4, rec.Code, rec.Body)
		}
	}
	if len(e.networks.created) != 0 {
		t.Errorf("something reached the store: %+v", e.networks.created)
	}
}

func TestCreateNetworkRefusesARangeAnotherTenantHas(t *testing.T) {
	e := newEnv(t)
	taken := readyNetwork("theirs")
	taken.IPv4 = "10.77.0.0/16"
	e.networks.put("other", taken)
	rec := e.do(t, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"lan","ipv4":"10.77.5.0/24"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "range_in_use") {
		t.Fatalf("status %d: %s, want 409 range_in_use", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "theirs") || strings.Contains(rec.Body.String(), "t-other") || strings.Contains(rec.Body.String(), "10.77.0.0/16") {
		t.Errorf("the answer names the other tenant's network: %s", rec.Body)
	}
	if code := call(t, e, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"lan","ipv4":"10.78.5.0/24"}`); code != http.StatusAccepted {
		t.Errorf("a free range: status %d, want 202", code)
	}
	// Without a range there is nothing to compare: Incus chooses.
	if code := call(t, e, "POST", "/v1/tenants/demo/networks", "operator", `{"name":"auto"}`); code != http.StatusAccepted {
		t.Errorf("no range: status %d, want 202", code)
	}
}
