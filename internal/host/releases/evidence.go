package releases

import (
	"errors"
	"fmt"
)

type ProvenanceBundleVerifier interface {
	VerifyBundle(raw []byte, expected []ProvenanceSubject) error
}

type ReleaseEvidenceRequirements struct {
	ProvenanceSubjects []ProvenanceSubject
	Notices            []NoticeRequirement
}

func VerifyReleaseEvidence(
	manifest ReleaseManifest,
	provenanceRaw []byte,
	noticesRaw []byte,
	requirements ReleaseEvidenceRequirements,
	verifier ProvenanceBundleVerifier,
) (NoticeManifest, error) {
	normalized, err := NewReleaseManifest(manifest)
	if err != nil {
		return NoticeManifest{}, err
	}
	if verifier == nil {
		return NoticeManifest{}, errors.New("release provenance verifier is required")
	}
	subjects, err := normalizeProvenanceSubjects(requirements.ProvenanceSubjects)
	if err != nil {
		return NoticeManifest{}, fmt.Errorf("release provenance requirements: %w", err)
	}
	if _, err = normalizeNoticeRequirements(requirements.Notices); err != nil {
		return NoticeManifest{}, fmt.Errorf("release notice requirements: %w", err)
	}
	if err = normalized.Provenance.VerifyBytes(provenanceRaw); err != nil {
		return NoticeManifest{}, fmt.Errorf("release provenance target verification failed: %w", err)
	}
	if err = normalized.Notices.VerifyBytes(noticesRaw); err != nil {
		return NoticeManifest{}, fmt.Errorf("release notices target verification failed: %w", err)
	}
	if err = verifier.VerifyBundle(provenanceRaw, subjects); err != nil {
		return NoticeManifest{}, err
	}
	notices, err := VerifyNoticeBundle(noticesRaw, requirements.Notices)
	if err != nil {
		return NoticeManifest{}, err
	}
	return notices, nil
}
