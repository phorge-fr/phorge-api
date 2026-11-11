package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/phorge-fr/phorge-api/internal/compute"
)

// A network nobody gave a range is read from Incus, with the token that Authentik gave for the person.
func TestTheRangeIncusChoseIsReadFromIncusWithTheTokenOfThePerson(t *testing.T) {
	e := newEnv(t)
	e.networks.put("demo", readyNetwork("auto"))
	chosen := readyNetwork("mine")
	chosen.IPv4 = "10.77.1.0/24"
	e.networks.put("demo", chosen)
	e.incus.set(func(f *fakeIncus) {
		f.networks = map[string]string{"auto": "10.88.2.1/24", "mine": "10.99.9.1/24"}
		f.networks6 = map[string]string{"auto": "fd42:8:8:8::1/64", "mine": "fd42:9:9:9::1/64"}
	})

	var body networkBody
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/networks/auto", "tadmin", ""), &body)
	if body.IPv4 != "10.88.2.0/24" || body.IPv6 != "fd42:8:8:8::/64" {
		t.Errorf("ranges = %q, %q, want the ranges of the gateways Incus chose", body.IPv4, body.IPv6)
	}
	var list struct {
		Items []networkBody `json:"items"`
	}
	mustDecode(t, e.do(t, "GET", "/v1/tenants/demo/networks", "tadmin", ""), &list)
	got := map[string]string{}
	for _, nw := range list.Items {
		got[nw.Name] = nw.IPv4 + " " + nw.IPv6
	}
	if got["auto"] != "10.88.2.0/24 fd42:8:8:8::/64" || got["mine"] != "10.77.1.0/24 fd42:9:9:9::/64" {
		t.Errorf("ranges = %v: what the user chose is not replaced by what Incus says", got)
	}
	e.incus.mu.Lock()
	defer e.incus.mu.Unlock()
	for _, tok := range e.incus.netTokens {
		if tok != "Bearer incus-tadmin" {
			t.Errorf("Incus was shown %q: the token of the person, not the exchanged one, or somebody else's", tok)
		}
	}
	if len(e.incus.netTokens) == 0 {
		t.Error("Incus was never asked")
	}
}

func TestNothingIsAskedOfIncusWhenEveryRangeIsKnown(t *testing.T) {
	e := newEnv(t)
	known := readyNetwork("mine")
	known.IPv4, known.IPv6 = "10.77.1.0/24", "fd42:1:2:3::/64"
	e.networks.put("demo", known)
	// the network every test tenant starts with has its range too
	net := readyNetwork("net")
	net.IPv4, net.IPv6 = "10.66.6.0/24", "fd42:6:6:6::/64"
	e.networks.put("demo", net)
	if code := call(t, e, "GET", "/v1/tenants/demo/networks", "tadmin", ""); code != http.StatusOK {
		t.Fatal(code)
	}
	if e.exchange.calls() != 0 {
		t.Error("a token was exchanged although every range was known")
	}
}

func TestAKeyAndAFailureLeaveTheRangeAbsentAndNothingBroken(t *testing.T) {
	for name, tt := range map[string]struct {
		who     string
		arrange func(e *env)
	}{
		"a key":                  {"svc_net", func(e *env) {}},
		"incus has no right for": {"tadmin", func(e *env) { e.incus.set(func(f *fakeIncus) { f.netStatus = http.StatusForbidden }) }},
		"incus fails":            {"tadmin", func(e *env) { e.incus.set(func(f *fakeIncus) { f.netStatus = http.StatusInternalServerError }) }},
		"the exchange fails":     {"tadmin", func(e *env) { e.exchange.err = errTest }},
	} {
		e := newEnv(t)
		e.networks.put("demo", readyNetwork("auto"))
		e.incus.set(func(f *fakeIncus) { f.networks = map[string]string{"auto": "10.88.2.1/24"} })
		tt.arrange(e)
		rec := e.do(t, "GET", "/v1/tenants/demo/networks/auto", tt.who, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200: what Incus knows is an addition, not a condition", name, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "ipv4") {
			t.Errorf("%s: a range is shown: %s", name, rec.Body)
		}
		for _, leak := range []string{"private detail", "incus-"} {
			if strings.Contains(rec.Body.String(), leak) {
				t.Errorf("%s: the answer carries %q", name, leak)
			}
		}
	}
	if e := newEnv(t); true {
		e.networks.put("demo", compute.Network{Name: "auto", Status: readyNetwork("auto").Status})
		e.do(t, "GET", "/v1/tenants/demo/networks/auto", "svc_net", "")
		if e.exchange.calls() != 0 {
			t.Error("the token of a key was exchanged")
		}
	}
}
