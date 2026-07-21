package tpm

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tpm.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if s.Pinned("vm1") {
		t.Error("empty store reports vm1 as pinned")
	}
	if err := s.Pin("vm1"); err != nil {
		t.Fatalf("pin: %v", err)
	}

	// A fresh store over the same file must see it.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !s2.Pinned("vm1") {
		t.Error("pin did not survive a reopen")
	}

	if err := s2.Unpin("vm1"); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	s3, _ := NewStore(path)
	if s3.Pinned("vm1") {
		t.Error("unpin did not survive a reopen")
	}
}

// fakeCtl records what the reconciler did.
type fakeCtl struct {
	state   string
	present bool
	sets    int
	stateEr error
}

func (f *fakeCtl) State(string) (string, error) {
	if f.stateEr != nil {
		return "", f.stateEr
	}
	return f.state, nil
}
func (f *fakeCtl) TPMInfo(string) (bool, string, string, error) {
	return f.present, "tpm-crb", "2.0", nil
}
func (f *fakeCtl) SetTPM(_ string, enabled bool) error {
	if enabled {
		f.sets++
		f.present = true
	}
	return nil
}

func TestReconcileRestoresStrippedTPM(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "tpm.json"))
	if err := s.Pin("vm1"); err != nil {
		t.Fatalf("pin: %v", err)
	}
	// ZVM re-saved the domain and dropped the device.
	ctl := &fakeCtl{state: "running", present: false}
	s.Reconcile(ctl, nil)
	if ctl.sets != 1 {
		t.Errorf("SetTPM called %d times, want 1", ctl.sets)
	}

	// A second pass must be a no-op — a reconciler that keeps acting on an
	// unchanged domain is signalling a broken read path.
	s.Reconcile(ctl, nil)
	if ctl.sets != 1 {
		t.Errorf("SetTPM called %d times over two passes, want 1 — the "+
			"reconciler is not idempotent", ctl.sets)
	}
}

// A deleted VM must not make the reconciler write to a domain that is gone.
func TestReconcileSkipsUndefinedVM(t *testing.T) {
	s, _ := NewStore(filepath.Join(t.TempDir(), "tpm.json"))
	_ = s.Pin("ghost")
	ctl := &fakeCtl{stateEr: errors.New("domain not found")}
	s.Reconcile(ctl, nil)
	if ctl.sets != 0 {
		t.Errorf("SetTPM called %d times for an undefined VM, want 0", ctl.sets)
	}
}
