package status

import (
	"strings"
	"testing"
	"time"
)

func TestAnObjectWhoseCompositionCannotRunFailsAfterAWhile(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	notReady := now.Add(-time.Hour)
	tests := []struct {
		name string
		o    Object
		want Phase
	}{
		{"just failed to sync", Object{NotReadySince: notReady, SyncFailedSince: now.Add(-30 * time.Second)}, Provisioning},
		{"failing to sync for FailedAfter", Object{NotReadySince: notReady, SyncFailedSince: now.Add(-FailedAfter)}, Failed},
		{"failing for a long time", Object{NotReadySince: notReady, SyncFailedSince: now.Add(-time.Hour)}, Failed},
		{"synced", Object{NotReadySince: notReady}, Provisioning},
		{"ready wins", Object{Ready: true, NotReadySince: notReady, SyncFailedSince: now.Add(-time.Hour)}, Ready},
		{"deleting wins", Object{NotReadySince: notReady, SyncFailedSince: now.Add(-time.Hour), Deleting: true}, Deleting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.o.At(now); got != tt.want {
				t.Errorf("phase = %s, want %s", got, tt.want)
			}
		})
	}
	if m := (Object{SyncFailedSince: now.Add(-time.Hour)}).Message(Failed); m == "" || strings.Contains(strings.ToLower(m), "crossplane") {
		t.Errorf("message = %q: it must say something, without naming Crossplane", m)
	}
}
