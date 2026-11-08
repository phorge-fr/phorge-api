package compute

import (
	"net/netip"
	"testing"
)

func TestParseRangeGatewayAndReserved(t *testing.T) {
	p, err := ParseRange("10.77.1.0/24")
	if err != nil {
		t.Fatal(err)
	}
	if g := Gateway(p); g != "10.77.1.1/24" {
		t.Errorf("Gateway = %s", g)
	}
	if r, ok := RangeOfGateway("10.77.1.1/24"); !ok || r != "10.77.1.0/24" {
		t.Errorf("RangeOfGateway = %s, %v", r, ok)
	}
	if _, ok := RangeOfGateway(""); ok {
		t.Error("an empty gateway has a range")
	}
	if _, ok := ReservedOverlap(netip.MustParsePrefix("10.2.0.0/16")); !ok {
		t.Error("10.2.0.0/16 holds a platform range")
	}
	if _, ok := ReservedOverlap(p); ok {
		t.Error("10.77.1.0/24 is not reserved")
	}
}
