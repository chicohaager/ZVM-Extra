package schedule

import (
	"testing"

	"github.com/chicohaager/zima-vm-extras/internal/virsh"
)

type fakeController struct {
	snaps   []virsh.Snapshot
	deleted []string
}

func (f *fakeController) State(string) (string, error)                          { return "shut off", nil }
func (f *fakeController) CreateSnapshot(_, _, _ string, _ bool, _ string) error { return nil }
func (f *fakeController) ListSnapshots(string) ([]virsh.Snapshot, error)        { return f.snaps, nil }
func (f *fakeController) DeleteSnapshot(_, snap string, _ bool) error {
	f.deleted = append(f.deleted, snap)
	return nil
}

func TestPruneKeepsNewestAndSparesManual(t *testing.T) {
	f := &fakeController{snaps: []virsh.Snapshot{
		{Name: "manual-keepme"},
		{Name: "auto-20260104-000000"},
		{Name: "auto-20260101-000000"},
		{Name: "auto-20260103-000000"},
		{Name: "auto-20260102-000000"},
	}}
	(&Store{}).prune(f, Entry{VM: "x", Keep: 2}, nil)

	// 4 auto snapshots, keep newest 2 → the 2 oldest deleted, manual untouched.
	if len(f.deleted) != 2 {
		t.Fatalf("deleted %v, want 2", f.deleted)
	}
	if f.deleted[0] != "auto-20260101-000000" || f.deleted[1] != "auto-20260102-000000" {
		t.Errorf("prune deleted the wrong ones / wrong order: %v", f.deleted)
	}
	for _, d := range f.deleted {
		if d == "manual-keepme" {
			t.Error("prune must never delete a manual (non-auto-) snapshot")
		}
	}
}

func TestPruneKeepZeroIsNoRetention(t *testing.T) {
	f := &fakeController{snaps: []virsh.Snapshot{{Name: "auto-1"}, {Name: "auto-2"}}}
	(&Store{}).prune(f, Entry{VM: "x", Keep: 0}, nil)
	if len(f.deleted) != 0 {
		t.Errorf("Keep=0 means unlimited — must not delete anything: %v", f.deleted)
	}
}

func TestUpsertValidation(t *testing.T) {
	s, _ := NewStore(t.TempDir() + "/schedule.json")
	if _, err := s.Upsert(Entry{VM: "x", Enabled: true, IntervalHours: 0}); err == nil {
		t.Error("an enabled schedule with interval 0 must be rejected")
	}
	if _, err := s.Upsert(Entry{VM: "x", Enabled: true, IntervalHours: 24, Keep: 7}); err != nil {
		t.Errorf("a valid schedule must be accepted: %v", err)
	}
}
