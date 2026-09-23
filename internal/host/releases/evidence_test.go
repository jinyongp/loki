package releases

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"testing"
	"time"
)

type fakeProvenanceVerifier struct {
	wantRaw      []byte
	wantSubjects []ProvenanceSubject
	err          error
	called       bool
}

func (f *fakeProvenanceVerifier) VerifyBundle(raw []byte, expected []ProvenanceSubject) error {
	f.called = true
	if f.err != nil {
		return f.err
	}
	if !slices.Equal(raw, f.wantRaw) || !slices.Equal(expected, f.wantSubjects) {
		return errors.New("unexpected provenance verification input")
	}
	return nil
}

func descriptorForEvidence(targetPath string, raw []byte) TargetDescriptor {
	sum := sha256.Sum256(raw)
	return TargetDescriptor{
		Path:   targetPath,
		Length: int64(len(raw)),
		SHA256: hex.EncodeToString(sum[:]),
	}
}

func releaseEvidenceFixture(t *testing.T) (ReleaseManifest, []byte, []byte, ReleaseEvidenceRequirements) {
	t.Helper()
	now := time.Date(2026, 9, 21, 9, 0, 0, 0, time.UTC)
	manifest := releaseManifestFixture(t, "1.2.3", now)
	provenanceRaw := []byte("self-contained-provenance-bundle")
	noticesRaw, _, err := BuildNoticeBundle(noticeRequirementsFixture(), noticeMaterialsFixture())
	if err != nil {
		t.Fatal(err)
	}
	manifest.Provenance = descriptorForEvidence("releases/provenance/1.2.3.bundle.json", provenanceRaw)
	manifest.Notices = descriptorForEvidence("releases/notices/1.2.3.tar.gz", noticesRaw)
	requirements := ReleaseEvidenceRequirements{
		ProvenanceSubjects: []ProvenanceSubject{
			{Name: "loki-linux-amd64", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
			{Name: "loki-linux-arm64", SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
		},
		Notices: noticeRequirementsFixture(),
	}
	return manifest, provenanceRaw, noticesRaw, requirements
}

func TestVerifyReleaseEvidenceBindsShippedEvidence(t *testing.T) {
	manifest, provenanceRaw, noticesRaw, requirements := releaseEvidenceFixture(t)
	expectedSubjects, err := normalizeProvenanceSubjects(requirements.ProvenanceSubjects)
	if err != nil {
		t.Fatal(err)
	}
	verifier := &fakeProvenanceVerifier{
		wantRaw:      provenanceRaw,
		wantSubjects: expectedSubjects,
	}
	notices, err := VerifyReleaseEvidence(manifest, provenanceRaw, noticesRaw, requirements, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if !verifier.called || len(notices.Entries) != len(noticeMaterialsFixture()) {
		t.Fatalf("release evidence verification incomplete: called=%v notices=%#v", verifier.called, notices)
	}
}

func TestVerifyReleaseEvidenceRejectsIncompleteOrMismatchedInputs(t *testing.T) {
	manifest, provenanceRaw, noticesRaw, requirements := releaseEvidenceFixture(t)
	expectedSubjects, err := normalizeProvenanceSubjects(requirements.ProvenanceSubjects)
	if err != nil {
		t.Fatal(err)
	}
	validVerifier := func() *fakeProvenanceVerifier {
		return &fakeProvenanceVerifier{wantRaw: provenanceRaw, wantSubjects: expectedSubjects}
	}

	t.Run("missing-verifier", func(t *testing.T) {
		if _, err := VerifyReleaseEvidence(manifest, provenanceRaw, noticesRaw, requirements, nil); err == nil {
			t.Fatal("release evidence without provenance verifier was accepted")
		}
	})

	t.Run("missing-provenance-subjects", func(t *testing.T) {
		changed := requirements
		changed.ProvenanceSubjects = nil
		if _, err := VerifyReleaseEvidence(manifest, provenanceRaw, noticesRaw, changed, validVerifier()); err == nil {
			t.Fatal("release evidence without provenance subjects was accepted")
		}
	})

	t.Run("provenance-target-mismatch", func(t *testing.T) {
		tampered := append([]byte(nil), provenanceRaw...)
		tampered[0] ^= 0x20
		if _, err := VerifyReleaseEvidence(manifest, tampered, noticesRaw, requirements, validVerifier()); err == nil {
			t.Fatal("release evidence with tampered provenance target was accepted")
		}
	})

	t.Run("notice-target-mismatch", func(t *testing.T) {
		tampered := append([]byte(nil), noticesRaw...)
		tampered[len(tampered)/2] ^= 0x20
		if _, err := VerifyReleaseEvidence(manifest, provenanceRaw, tampered, requirements, validVerifier()); err == nil {
			t.Fatal("release evidence with tampered notice target was accepted")
		}
	})

	t.Run("notice-inventory-drift", func(t *testing.T) {
		changed := requirements
		changed.Notices = append([]NoticeRequirement(nil), requirements.Notices...)
		changed.Notices[0].Version = "different"
		if _, err := VerifyReleaseEvidence(manifest, provenanceRaw, noticesRaw, changed, validVerifier()); err == nil {
			t.Fatal("release evidence with stale notice inventory was accepted")
		}
	})

	t.Run("provenance-policy-failure", func(t *testing.T) {
		verifier := validVerifier()
		verifier.err = errors.New("identity mismatch")
		if _, err := VerifyReleaseEvidence(manifest, provenanceRaw, noticesRaw, requirements, verifier); err == nil {
			t.Fatal("release evidence with failed provenance policy was accepted")
		}
	})
}
