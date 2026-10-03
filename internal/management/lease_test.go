package management

import "testing"

func TestConcurrentSessionsPinGenerationUntilLastDisconnect(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	state.Installed["browser"] = ownedFixture(t, s, "browser")
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	first, err := s.Lease(state.Installed["browser"].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Lease(state.Installed["browser"].Artifact)
	if err != nil {
		first()
		t.Fatal(err)
	}
	if err := s.Remove("browser"); err == nil {
		first()
		second()
		t.Fatal("removed leased generation")
	}
	first()
	first()
	if err := s.Remove("browser"); err == nil {
		second()
		t.Fatal("remaining session lost its lease")
	}
	second()
	if err := s.Remove("browser"); err != nil {
		t.Fatal(err)
	}
}
