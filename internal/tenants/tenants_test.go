package tenants

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPhaseAt(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	since := func(d time.Duration) time.Time { return now.Add(-d) }

	tests := []struct {
		name string
		t    Tenant
		want Phase
	}{
		{"ready", Tenant{Ready: true, NotReadySince: since(time.Hour)}, Ready},
		{"ready with a leftover problem", Tenant{Ready: true, Problems: "old"}, Ready},
		{"just created", Tenant{NotReadySince: since(5 * time.Second)}, Provisioning},
		{"a problem at the first reconciles", Tenant{Problems: "Alias doesn't exist", NotReadySince: since(30 * time.Second)}, Provisioning},
		{"a problem for a while", Tenant{Problems: "Reached maximum", NotReadySince: since(FailedAfter)}, Failed},
		{"not ready for a long time, with no problem", Tenant{NotReadySince: since(time.Hour)}, Provisioning},
		{"deleting wins over ready", Tenant{Ready: true, Deleting: true}, Deleting},
		{"deleting wins over failed", Tenant{Problems: "x", NotReadySince: since(time.Hour), Deleting: true}, Deleting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.t.PhaseAt(now); got != tt.want {
				t.Errorf("phase = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestCreateRequestValidate(t *testing.T) {
	good := CreateRequest{Name: "demo", Owner: "alice@example.org", Plan: Medium, DisplayName: "Demo"}
	if err := good.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := (CreateRequest{Name: "demo", Owner: "alice@example.org"}).Validate(); err != nil {
		t.Errorf("plan and display name are optional: %v", err)
	}
	if err := (CreateRequest{Name: "ab", Owner: "Alice@Example.org"}).Validate(); err != nil {
		t.Errorf("the owner keeps its case, a two letters name is fine: %v", err)
	}

	for name, req := range map[string]CreateRequest{
		"no name":                     {Owner: "a@b"},
		"a one letter name":           {Name: "d", Owner: "a@b"},
		"an upper case name":          {Name: "Demo", Owner: "a@b"},
		"a name starting with 1":      {Name: "1demo", Owner: "a@b"},
		"a name with an underscore":   {Name: "de_mo", Owner: "a@b"},
		"a name of 29 characters":     {Name: strings.Repeat("a", 29), Owner: "a@b"},
		"a name that is a relation":   {Name: "demo#admin", Owner: "a@b"},
		"no owner":                    {Name: "demo"},
		"an owner that is a wildcard": {Name: "demo", Owner: "*"},
		"an owner with a relation":    {Name: "demo", Owner: "a@b#admin"},
		"an owner with a space":       {Name: "demo", Owner: "a @b"},
		"an unknown plan":             {Name: "demo", Owner: "a@b", Plan: "huge"},
		"a display name too long":     {Name: "demo", Owner: "a@b", DisplayName: strings.Repeat("é", MaxDisplayName+1)},
	} {
		t.Run(name, func(t *testing.T) {
			var invalid *InvalidError
			if err := req.Validate(); !errors.As(err, &invalid) || len(invalid.Problems) == 0 {
				t.Errorf("accepted: %v", err)
			}
		})
	}

	// Characters are counted, not bytes: 100 accents are 200 bytes.
	if err := (CreateRequest{Name: "demo", Owner: "a@b", DisplayName: strings.Repeat("é", MaxDisplayName)}).Validate(); err != nil {
		t.Errorf("100 characters were refused: %v", err)
	}

	err := CreateRequest{Name: "D", Owner: "x", Plan: "huge"}.Validate()
	var invalid *InvalidError
	if !errors.As(err, &invalid) || len(invalid.Problems) != 3 {
		t.Errorf("every problem is reported at once: %v", err)
	}
}

func TestPatchRequestValidate(t *testing.T) {
	large, bad, name := Large, Plan("huge"), "Production"
	long := strings.Repeat("a", MaxDisplayName+1)
	empty := ""

	for name, ok := range map[string]PatchRequest{
		"a plan":               {Plan: &large},
		"a display name":       {DisplayName: &name},
		"an empty name clears": {DisplayName: &empty},
		"both":                 {Plan: &large, DisplayName: &name},
	} {
		if err := ok.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, req := range map[string]PatchRequest{
		"nothing":                 {},
		"an unknown plan":         {Plan: &bad},
		"a display name too long": {DisplayName: &long},
	} {
		if err := req.Validate(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
