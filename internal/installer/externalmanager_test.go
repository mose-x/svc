package installer

import (
	"strings"
	"testing"

	"svc/internal/apperr"
	"svc/internal/sdk"
)

// stubProbe returns a Service whose NodeManagerProbe answers manager and counts
// how often it was asked. The count is the point of half these cases: an SDK
// with no external manager must not pay for a probe that reads the login shell's
// PATH and stats two home directories.
func stubProbe(manager string) (*Service, *int) {
	calls := 0
	s := &Service{NodeManagerProbe: func() string {
		calls++
		return manager
	}}
	return s, &calls
}

// TestExternalManagerRefusalIsAboutNodejsOnly pins the scope of the rule. Go,
// the JDK and Python have no external version manager svc knows about, so the
// refusal must never fire for them -- and must never even look, because the
// probe is not free.
func TestExternalManagerRefusalIsAboutNodejsOnly(t *testing.T) {
	for _, sdkType := range []string{
		string(sdk.Golang), string(sdk.JDK), string(sdk.Python), string(sdk.Rust),
		"not-an-sdk-at-all",
	} {
		t.Run(sdkType, func(t *testing.T) {
			// A machine drowning in nvm: the answer still has to be nil for an
			// SDK nvm cannot own.
			s, calls := stubProbe("nvm-rust")
			if err := s.ExternalManagerRefusal(sdkType); err != nil {
				t.Errorf("ExternalManagerRefusal(%q) = %v, want nil", sdkType, err)
			}
			if *calls != 0 {
				t.Errorf("the probe ran %d time(s) for %q, want 0", *calls, sdkType)
			}
		})
	}
}

// TestExternalManagerRefusalAnswersPerMachine covers both machine states for
// Node.js: a free machine proceeds, a managed one is refused.
func TestExternalManagerRefusalAnswersPerMachine(t *testing.T) {
	t.Run("a machine with no manager proceeds", func(t *testing.T) {
		s, calls := stubProbe("")
		if err := s.ExternalManagerRefusal(string(sdk.NodeJS)); err != nil {
			t.Errorf("ExternalManagerRefusal(nodejs) = %v, want nil", err)
		}
		if *calls != 1 {
			t.Errorf("the probe ran %d time(s), want exactly 1", *calls)
		}
	})
	for _, manager := range []string{"nvm-rust", "nvm"} {
		t.Run("a machine owned by "+manager+" is refused", func(t *testing.T) {
			s, _ := stubProbe(manager)
			err := s.ExternalManagerRefusal(string(sdk.NodeJS))
			if err == nil {
				t.Fatal("ExternalManagerRefusal(nodejs) = nil, want a refusal")
			}
			// The marker is the contract: the frontend turns it into a sentence
			// and the CLI turns it into exit 2, so the code and both params have
			// to survive the trip. The params are the words a user reads -- the
			// display name and the label, not the machine identifiers -- because
			// they are interpolated into errors.managed-sdk verbatim and the GUI
			// renders the same key from its own short-circuit.
			want := string(apperr.New(apperr.ManagedSdk, map[string]string{
				"sdk": sdk.SdkDisplayName(sdk.NodeJS), "manager": sdk.ExternalManagerLabel(manager),
			}).Error())
			if got := err.Error(); got != want {
				t.Errorf("the refusal is %q, want %q", got, want)
			}
			for _, fragment := range []string{
				"[svc:managed-sdk]",
				`"manager":"` + sdk.ExternalManagerLabel(manager) + `"`,
				`"sdk":"Node.js"`,
			} {
				if !strings.Contains(err.Error(), fragment) {
					t.Errorf("the refusal %q does not carry %q", err, fragment)
				}
			}
		})
	}
}

// TestExternalManagerRefusalWithoutNewStillEnforces covers a Service built as a
// literal rather than through New: the probe is nil, and the rule must fall back
// to the real machine instead of panicking on it. Which answer the host gives is
// not the point -- that it answers at all is.
func TestExternalManagerRefusalWithoutNewStillEnforces(t *testing.T) {
	s := &Service{}
	err := s.ExternalManagerRefusal(string(sdk.NodeJS))
	switch manager := sdk.NodeManagerOnMachine(); {
	case manager == "" && err != nil:
		t.Errorf("this machine has no manager but the refusal fired: %v", err)
	case manager != "" && err == nil:
		t.Errorf("this machine is owned by %s but the refusal did not fire", manager)
	}
}

// TestInstallSdkRefusesAManagedNodejsBeforeDownloading pins the placement of the
// check inside InstallSdk and SwitchVersion. The shells call
// ExternalManagerRefusal themselves, so a binding reached directly -- a Wails
// call from the GUI, or a shell that forgot the check -- must still not get as
// far as the download. The Service is a real one (registry wired) so the refusal
// is reached rather than short-circuited by the app-not-initialized guard in
// front of it.
func TestInstallSdkRefusesAManagedNodejsBeforeDownloading(t *testing.T) {
	s := newScanTestService(t, nil)
	s.NodeManagerProbe = func() string { return "nvm-rust" }

	err := s.InstallSdk(string(sdk.NodeJS), "22.11.0")
	if err == nil {
		t.Fatal("InstallSdk = nil on a managed machine, want a refusal")
	}
	if !strings.Contains(err.Error(), "[svc:managed-sdk]") {
		t.Errorf("InstallSdk = %v, want the managed-sdk refusal", err)
	}
	// Switching re-points the `node` shim at svc's own copy, which is the same
	// fight, so it is refused for the same reason.
	switchErr := s.SwitchVersion(string(sdk.NodeJS), "22.11.0")
	if switchErr == nil {
		t.Fatal("SwitchVersion = nil on a managed machine, want a refusal")
	}
	if !strings.Contains(switchErr.Error(), "[svc:managed-sdk]") {
		t.Errorf("SwitchVersion = %v, want the managed-sdk refusal", switchErr)
	}
	// Uninstalling is deliberately NOT refused -- removing svc's own copy is how
	// a user gets out of the situation, and locking that too would trap them.
	// It cannot regress here because it never reaches this Service at all:
	// `svc uninstall` and the GUI's delete button both call
	// storage.UninstallVersion. The CLI test for the managed machine pins the
	// user-visible half of that.
}
