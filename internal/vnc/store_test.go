package vnc

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestValidPassword(t *testing.T) {
	good := []string{"a", "12345678", "P@ssw0r", "abc-_.+!"}
	for _, s := range good {
		if !ValidPassword(s) {
			t.Errorf("ValidPassword(%q) = false, want true", s)
		}
	}
	// Too short/long, quote and XML-significant chars, control chars, non-ASCII.
	bad := []string{"", "123456789", "ab'cd", `a"b`, "x<y", "a>b", "a&b", "a\tb", "ümlaut"}
	for _, s := range bad {
		if ValidPassword(s) {
			t.Errorf("ValidPassword(%q) = true, want false", s)
		}
	}
}

func TestStoreRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vnc.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.All()) != 0 {
		t.Fatalf("new store not empty: %d entries", len(s.All()))
	}
	if err := s.Set(Entry{VM: "vm1", Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	e, ok := s.Get("vm1")
	if !ok || e.Password != "secret" {
		t.Fatalf("Get(vm1) = %+v, %v", e, ok)
	}

	// Reopen from disk — the entry must persist.
	s2, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if e, ok := s2.Get("vm1"); !ok || e.Password != "secret" {
		t.Fatalf("reopened Get(vm1) = %+v, %v", e, ok)
	}
	if err := s2.Remove("vm1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Get("vm1"); ok {
		t.Fatal("Remove did not delete the entry")
	}
}

// fakeController records SetVNCPassword calls and answers VNCHasPassword from
// a map, so Reconcile can be exercised without a real virsh.
type fakeController struct {
	state    map[string]string
	hasPw    map[string]bool
	setCalls []string

	listen         map[string]string
	port           map[string]int
	autoport       map[string]bool
	setListenCalls []string
}

func (f *fakeController) State(n string) (string, error) {
	st, ok := f.state[n]
	if !ok {
		return "", fmt.Errorf("no such domain %q", n)
	}
	return st, nil
}

func (f *fakeController) VNCHasPassword(n string) (bool, error) { return f.hasPw[n], nil }

func (f *fakeController) SetVNCPassword(n, pw string) error {
	f.setCalls = append(f.setCalls, n)
	f.hasPw[n] = pw != ""
	return nil
}

func (f *fakeController) VNCListenInfo(n string) (string, int, bool, error) {
	auto := true
	if f.autoport != nil {
		if a, ok := f.autoport[n]; ok {
			auto = a
		}
	}
	var p int
	if f.port != nil {
		p = f.port[n]
	}
	var l string
	if f.listen != nil {
		l = f.listen[n]
	}
	return l, p, auto, nil
}

func (f *fakeController) SetVNCListen(n, listen string, port int) error {
	f.setListenCalls = append(f.setListenCalls, n)
	if f.listen == nil {
		f.listen = map[string]string{}
	}
	if f.port == nil {
		f.port = map[string]int{}
	}
	if f.autoport == nil {
		f.autoport = map[string]bool{}
	}
	f.listen[n] = listen
	f.port[n] = port
	f.autoport[n] = port <= 0
	return nil
}

func TestReconcileRepairsMissingPassword(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "vnc.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, vm := range []string{"protected", "stripped", "gone"} {
		if err := s.Set(Entry{VM: vm, Password: "pw"}); err != nil {
			t.Fatal(err)
		}
	}
	fc := &fakeController{
		state: map[string]string{"protected": "running", "stripped": "running"},
		hasPw: map[string]bool{"protected": true, "stripped": false},
	}
	s.Reconcile(fc, nil)

	// "protected" already has a password  → left alone.
	// "stripped"  lost its password       → repaired.
	// "gone"      is an undefined domain  → skipped (State errors).
	if len(fc.setCalls) != 1 || fc.setCalls[0] != "stripped" {
		t.Fatalf("setCalls = %v, want [stripped]", fc.setCalls)
	}

	// A second pass must be a no-op: the repair already set the password.
	s.Reconcile(fc, nil)
	if len(fc.setCalls) != 1 {
		t.Fatalf("second pass made extra repairs: %v", fc.setCalls)
	}
}

// A pinned listen address must survive ZVM re-generating <graphics> with
// listen='::', and an entry that never asked for an address must be left
// alone — that is every pin written before v0.7.0.
func TestReconcileRepairsListenOnlyWhenPinned(t *testing.T) {
	s, err := NewStore(filepath.Join(t.TempDir(), "vnc.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(Entry{VM: "restricted", Password: "pw", Listen: "127.0.0.1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(Entry{VM: "pwonly", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	fc := &fakeController{
		state:    map[string]string{"restricted": "running", "pwonly": "running"},
		hasPw:    map[string]bool{"restricted": true, "pwonly": true},
		listen:   map[string]string{"restricted": "::", "pwonly": "::"},
		autoport: map[string]bool{"restricted": true, "pwonly": true},
	}
	s.Reconcile(fc, nil)

	if len(fc.setListenCalls) != 1 || fc.setListenCalls[0] != "restricted" {
		t.Fatalf("setListenCalls = %v, want [restricted]", fc.setListenCalls)
	}
	if fc.listen["pwonly"] != "::" {
		t.Fatalf("password-only entry had its listen address changed to %q", fc.listen["pwonly"])
	}

	// Idempotent: the address now matches, so a second pass repairs nothing.
	s.Reconcile(fc, nil)
	if len(fc.setListenCalls) != 1 {
		t.Fatalf("second pass repaired again: %v", fc.setListenCalls)
	}
}
