package status

import (
	"strings"
	"testing"
)

// What the console showed for a tenant whose project did not exist yet.
const rawTenant = "network Ready: not ready | network Synced: create failed: apply failed: Failed to create network \"default\": Failed to load project \"t-demo-2\": Project not found | " +
	"network LastAsyncOperation: apply failed: Failed to create network \"default\": Failed to load project \"t-demo-2\": Project not found | owner Ready: not ready | owner-api Ready: not ready | " +
	"profile Synced: create failed: apply failed: Failed to create profile \"phorge\": Failed loading project \"t-demo-2\": Project not found | profile Ready: not ready | " +
	"profile LastAsyncOperation: apply failed: Failed to create profile \"phorge\": Failed loading project \"t-demo-2\": Project not found | project Ready: not ready"

func TestPublicNeverShowsTheCompositionOrThePlatform(t *testing.T) {
	got := Public(rawTenant)
	if got != platformMessage {
		t.Errorf("Public = %q, want the one sentence about the platform", got)
	}
	for _, leak := range []string{"network", "Synced", "apply failed", "Project not found", "t-demo-2", "LastAsyncOperation", "profile"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q leaks into %q", leak, got)
		}
	}
}

func TestPublicSaysNothingWhileThingsAreSimplyNotReady(t *testing.T) {
	if got := Public("project Ready: not ready | network Ready: not ready | owner Ready: not ready"); got != "" {
		t.Errorf("Public = %q, want nothing: nothing is wrong yet", got)
	}
	if got := Public(""); got != "" {
		t.Errorf("Public(\"\") = %q", got)
	}
}

func TestPublicTranslatesWhatTheUserCanActOn(t *testing.T) {
	for raw, want := range map[string]string{
		"instance Synced: create failed: Failed creating instance: Reached maximum number of instances in project \"t-demo\"":      "maximum number of instances",
		"instance LastAsyncOperation: Reached maximum aggregate value \"100GiB\" for \"limits.disk\" in project \"t-x\"":           "disk limit",
		"instance Synced: Reached maximum aggregate value \"8GiB\" for \"limits.memory\" in project \"t-x\"":                       "memory limit",
		"instance Synced: Reached maximum aggregate value \"4\" for \"limits.cpu\" in project \"t-x\"":                             "CPU limit",
		"network Synced: create failed: apply failed: Failed to create network \"x\": Networks limit has been reached for project": "maximum number of networks",
		"instance Synced: start failed: the image is incompatible with secureboot":                                                 "secure boot",
		"image Synced: delete failed: in-use by 1 usage(s)":                                                                        "used by an instance",
		"instance Synced: update failed: prevent_destroy is set":                                                                   "delete it and create it again",
	} {
		got := Public(raw)
		if !strings.Contains(got, want) {
			t.Errorf("Public(%q) = %q, want it to say %q", raw, got, want)
		}
		for _, leak := range []string{"t-demo", "t-x", "failed:", "Failed creating", "LastAsyncOperation", "Synced"} {
			if strings.Contains(got, leak) {
				t.Errorf("%q leaks into %q", leak, got)
			}
		}
	}
}

func TestPublicKeepsTheIncusStatusesOutOfIt(t *testing.T) {
	// The state of a VM (Starting, Stopping, Running) and the phase are other fields: they are not problems.
	if got := Public("instance Synced: connect failed: cannot get terraform setup: cannot resolve provider config"); got != instanceMessage {
		t.Errorf("Public = %q, want the sentence about the instance", got)
	}
	if got := Public("image Synced: connect failed: cannot track ProviderConfig usage"); got != imageMessage {
		t.Errorf("Public = %q, want the sentence about the image", got)
	}
}

func TestPublicSaysEachThingOnce(t *testing.T) {
	got := Public("image Synced: x | image LastAsyncOperation: y | image Ready: not ready | instance Synced: z")
	if strings.Count(got, imageMessage) != 1 || strings.Count(got, instanceMessage) != 1 {
		t.Errorf("Public = %q", got)
	}
}

func TestAnEntryThatIsNotInTheFormatIsStillHidden(t *testing.T) {
	if got := Public("something odd with 10.1.2.3 in it"); got != platformMessage {
		t.Errorf("Public = %q", got)
	}
}

func TestAFailedObjectAlwaysSaysSomething(t *testing.T) {
	o := Object{Problems: "project Ready: not ready"}
	if got := o.Message(Failed); got != failedMessage {
		t.Errorf("Message = %q", got)
	}
	if got := o.Message(Provisioning); got != "" {
		t.Errorf("Message = %q: nothing is wrong yet", got)
	}
	if got := (Object{Problems: rawTenant}).Message(Failed); got != platformMessage {
		t.Errorf("Message = %q", got)
	}
}
