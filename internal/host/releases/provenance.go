package releases

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	slsav1 "github.com/in-toto/attestation/go/predicates/provenance/v1"
	intotov1 "github.com/in-toto/attestation/go/v1"
	sigbundle "github.com/sigstore/sigstore-go/pkg/bundle"
	sigroot "github.com/sigstore/sigstore-go/pkg/root"
	sigverify "github.com/sigstore/sigstore-go/pkg/verify"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	slsaProvenanceV1       = "https://slsa.dev/provenance/v1"
	maxProvenanceBundleLen = 16 << 20
)

type ProvenanceSubject struct {
	Name   string
	SHA256 string
}

type ProvenanceBuild struct {
	BuildType          string
	BuilderID          string
	InvocationID       string
	StartedAt          time.Time
	FinishedAt         time.Time
	ExternalParameters map[string]string
	ResolvedInputs     []ProvenanceSubject
	Subjects           []ProvenanceSubject
}

type ProvenancePolicy struct {
	Issuer                 string
	SubjectAlternativeName string
}

type ProvenanceVerifier struct {
	verifier *sigverify.Verifier
	identity sigverify.CertificateIdentity
}

func BuildSLSAProvenanceStatement(build ProvenanceBuild) ([]byte, error) {
	buildType, err := validateHTTPSIdentity(build.BuildType, "SLSA build type")
	if err != nil {
		return nil, err
	}
	builderID, err := validateHTTPSIdentity(build.BuilderID, "SLSA builder identity")
	if err != nil {
		return nil, err
	}
	invocationID := strings.TrimSpace(build.InvocationID)
	if invocationID == "" || len(invocationID) > 512 || strings.ContainsAny(invocationID, "\r\n\x00") {
		return nil, errors.New("SLSA invocation identity is invalid")
	}
	started := build.StartedAt.UTC().Truncate(time.Second)
	finished := build.FinishedAt.UTC().Truncate(time.Second)
	if started.IsZero() || finished.IsZero() || finished.Before(started) {
		return nil, errors.New("SLSA build timestamps are invalid")
	}
	subjects, err := normalizeProvenanceSubjects(build.Subjects)
	if err != nil {
		return nil, fmt.Errorf("SLSA subjects: %w", err)
	}
	inputs, err := normalizeProvenanceSubjects(build.ResolvedInputs)
	if err != nil {
		return nil, fmt.Errorf("SLSA resolved inputs: %w", err)
	}
	params, err := provenanceParameters(build.ExternalParameters)
	if err != nil {
		return nil, err
	}

	provenance := &slsav1.Provenance{
		BuildDefinition: &slsav1.BuildDefinition{
			BuildType:            buildType,
			ExternalParameters:   params,
			ResolvedDependencies: resourceDescriptors(inputs),
		},
		RunDetails: &slsav1.RunDetails{
			Builder: &slsav1.Builder{Id: builderID},
			Metadata: &slsav1.BuildMetadata{
				InvocationId: invocationID,
				StartedOn:    timestamppb.New(started),
				FinishedOn:   timestamppb.New(finished),
			},
		},
	}
	if err = provenance.Validate(); err != nil {
		return nil, fmt.Errorf("SLSA provenance is invalid: %w", err)
	}
	predicateRaw, err := protojson.Marshal(provenance)
	if err != nil {
		return nil, err
	}
	var predicate structpb.Struct
	if err = protojson.Unmarshal(predicateRaw, &predicate); err != nil {
		return nil, err
	}
	statement := &intotov1.Statement{
		Type:          intotov1.StatementTypeUri,
		Subject:       resourceDescriptors(subjects),
		PredicateType: slsaProvenanceV1,
		Predicate:     &predicate,
	}
	if err = statement.Validate(); err != nil {
		return nil, fmt.Errorf("in-toto statement is invalid: %w", err)
	}
	return protojson.Marshal(statement)
}

func NewProvenanceVerifier(trusted sigroot.TrustedMaterial, policy ProvenancePolicy) (*ProvenanceVerifier, error) {
	if trusted == nil {
		return nil, errors.New("Sigstore trusted material is required")
	}
	issuer := strings.TrimSpace(policy.Issuer)
	san := strings.TrimSpace(policy.SubjectAlternativeName)
	if issuer == "" || san == "" || strings.ContainsAny(issuer+san, "\r\n\x00") {
		return nil, errors.New("Sigstore certificate identity policy is incomplete")
	}
	identity, err := sigverify.NewShortCertificateIdentity(issuer, "", san, "")
	if err != nil {
		return nil, err
	}
	verifier, err := sigverify.NewVerifier(
		trusted,
		sigverify.WithSignedCertificateTimestamps(1),
		sigverify.WithTransparencyLog(1),
		sigverify.WithObserverTimestamps(1),
		sigverify.WithoutStatementPredicate(),
	)
	if err != nil {
		return nil, err
	}
	return &ProvenanceVerifier{verifier: verifier, identity: identity}, nil
}

func (v *ProvenanceVerifier) VerifyBundle(raw []byte, expected []ProvenanceSubject) error {
	if v == nil || v.verifier == nil {
		return errors.New("Sigstore provenance verifier is not configured")
	}
	if len(raw) == 0 || len(raw) > maxProvenanceBundleLen {
		return errors.New("Sigstore provenance bundle exceeds size policy")
	}
	subjects, err := normalizeProvenanceSubjects(expected)
	if err != nil {
		return err
	}
	var signed sigbundle.Bundle
	if err = signed.UnmarshalJSON(raw); err != nil {
		return fmt.Errorf("decode Sigstore bundle: %w", err)
	}
	digests := make([]sigverify.ArtifactDigest, 0, len(subjects))
	for _, subject := range subjects {
		value, decodeErr := hex.DecodeString(subject.SHA256)
		if decodeErr != nil {
			return decodeErr
		}
		digests = append(digests, sigverify.ArtifactDigest{Algorithm: "sha256", Digest: value})
	}
	result, err := v.verifier.Verify(
		&signed,
		sigverify.NewPolicy(
			sigverify.WithArtifactDigests(digests),
			sigverify.WithCertificateIdentity(v.identity),
		),
	)
	if err != nil {
		return fmt.Errorf("verify Sigstore provenance bundle: %w", err)
	}
	return validateVerifiedProvenance(result, subjects)
}

func validateVerifiedProvenance(result *sigverify.VerificationResult, expected []ProvenanceSubject) error {
	if result == nil || result.Statement == nil {
		return errors.New("verified Sigstore bundle has no in-toto statement")
	}
	if result.Statement.GetType() != intotov1.StatementTypeUri ||
		result.Statement.GetPredicateType() != slsaProvenanceV1 {
		return errors.New("verified Sigstore bundle is not SLSA provenance v1")
	}
	if result.VerifiedIdentity == nil {
		return errors.New("verified Sigstore bundle has no verified certificate identity")
	}
	actual := make([]ProvenanceSubject, 0, len(result.Statement.GetSubject()))
	for _, subject := range result.Statement.GetSubject() {
		if subject == nil {
			return errors.New("verified provenance contains an empty subject")
		}
		digest := subject.GetDigest()["sha256"]
		actual = append(actual, ProvenanceSubject{Name: subject.GetName(), SHA256: digest})
	}
	normalized, err := normalizeProvenanceSubjects(actual)
	if err != nil {
		return fmt.Errorf("verified provenance subjects: %w", err)
	}
	if !slices.Equal(normalized, expected) {
		return errors.New("verified provenance subjects do not match the release evidence set")
	}
	return nil
}

func normalizeProvenanceSubjects(subjects []ProvenanceSubject) ([]ProvenanceSubject, error) {
	if len(subjects) == 0 {
		return nil, errors.New("at least one provenance subject is required")
	}
	result := append([]ProvenanceSubject(nil), subjects...)
	for index := range result {
		result[index].Name = strings.TrimSpace(result[index].Name)
		result[index].SHA256 = strings.ToLower(strings.TrimSpace(result[index].SHA256))
		if result[index].Name == "" || len(result[index].Name) > 1024 ||
			strings.ContainsAny(result[index].Name, "\r\n\x00") ||
			!sha256Pattern.MatchString(result[index].SHA256) {
			return nil, errors.New("provenance subject identity is invalid")
		}
	}
	slices.SortFunc(result, func(left, right ProvenanceSubject) int {
		if value := strings.Compare(left.Name, right.Name); value != 0 {
			return value
		}
		return strings.Compare(left.SHA256, right.SHA256)
	})
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] || result[index].Name == result[index-1].Name {
			return nil, errors.New("provenance subjects must have unique names")
		}
	}
	return result, nil
}

func resourceDescriptors(subjects []ProvenanceSubject) []*intotov1.ResourceDescriptor {
	result := make([]*intotov1.ResourceDescriptor, 0, len(subjects))
	for _, subject := range subjects {
		result = append(result, &intotov1.ResourceDescriptor{
			Name: subject.Name,
			Digest: map[string]string{
				"sha256": subject.SHA256,
			},
		})
	}
	return result
}

func provenanceParameters(values map[string]string) (*structpb.Struct, error) {
	if len(values) == 0 {
		return nil, errors.New("SLSA external parameters are required")
	}
	params := make(map[string]any, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" || len(key) > 128 || len(value) > 2048 ||
			strings.ContainsAny(key+value, "\r\n\x00") {
			return nil, errors.New("SLSA external parameters are invalid")
		}
		params[key] = value
	}
	return structpb.NewStruct(params)
}

func validateHTTPSIdentity(value, name string) (string, error) {
	value = strings.TrimSpace(value)
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("%s must be an HTTPS URI", name)
	}
	return value, nil
}
