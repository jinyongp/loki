package releases

import (
	"bytes"
	"testing"
	"time"

	slsav1 "github.com/in-toto/attestation/go/predicates/provenance/v1"
	intotov1 "github.com/in-toto/attestation/go/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func provenanceBuildFixture() ProvenanceBuild {
	return ProvenanceBuild{
		BuildType:    "https://github.com/jinyongp/loki/.github/workflows/release.yml",
		BuilderID:    "https://github.com/actions/runner",
		InvocationID: "run-12345",
		StartedAt:    time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC),
		FinishedAt:   time.Date(2026, 9, 21, 8, 3, 0, 0, time.UTC),
		ExternalParameters: map[string]string{
			"ref":     "refs/tags/v1.2.3",
			"release": "1.2.3",
		},
		ResolvedInputs: []ProvenanceSubject{
			{Name: "source", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
		Subjects: []ProvenanceSubject{
			{Name: "loki-linux-arm64", SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},
			{Name: "loki-linux-amd64", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		},
	}
}

func TestBuildSLSAProvenanceStatementUsesStandardV1Contract(t *testing.T) {
	build := provenanceBuildFixture()
	raw, err := BuildSLSAProvenanceStatement(build)
	if err != nil {
		t.Fatal(err)
	}
	reordered := build
	reordered.Subjects = []ProvenanceSubject{build.Subjects[1], build.Subjects[0]}
	reordered.ExternalParameters = map[string]string{
		"release": "1.2.3",
		"ref":     "refs/tags/v1.2.3",
	}
	second, err := BuildSLSAProvenanceStatement(reordered)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, second) {
		t.Fatalf("provenance statement is not deterministic:\n%s\n%s", raw, second)
	}

	var statement intotov1.Statement
	if err = protojson.Unmarshal(raw, &statement); err != nil {
		t.Fatal(err)
	}
	if err = statement.Validate(); err != nil {
		t.Fatal(err)
	}
	if statement.GetType() != intotov1.StatementTypeUri || statement.GetPredicateType() != slsaProvenanceV1 {
		t.Fatalf("statement contract type=%q predicate=%q", statement.GetType(), statement.GetPredicateType())
	}
	if len(statement.GetSubject()) != 2 ||
		statement.GetSubject()[0].GetName() != "loki-linux-amd64" ||
		statement.GetSubject()[1].GetName() != "loki-linux-arm64" {
		t.Fatalf("statement subjects = %#v", statement.GetSubject())
	}
	predicateRaw, err := protojson.Marshal(statement.GetPredicate())
	if err != nil {
		t.Fatal(err)
	}
	var provenance slsav1.Provenance
	if err = protojson.Unmarshal(predicateRaw, &provenance); err != nil {
		t.Fatal(err)
	}
	if err = provenance.Validate(); err != nil {
		t.Fatal(err)
	}
	if provenance.GetRunDetails().GetBuilder().GetId() != build.BuilderID ||
		provenance.GetBuildDefinition().GetBuildType() != build.BuildType {
		t.Fatalf("SLSA predicate builder=%q build_type=%q", provenance.GetRunDetails().GetBuilder().GetId(), provenance.GetBuildDefinition().GetBuildType())
	}
}

func TestBuildSLSAProvenanceStatementRejectsIncompleteInputs(t *testing.T) {
	tests := map[string]func(*ProvenanceBuild){
		"no-subject": func(build *ProvenanceBuild) {
			build.Subjects = nil
		},
		"duplicate-subject": func(build *ProvenanceBuild) {
			build.Subjects = append(build.Subjects, build.Subjects[0])
		},
		"bad-digest": func(build *ProvenanceBuild) {
			build.Subjects[0].SHA256 = "sha256:bad"
		},
		"no-external-parameters": func(build *ProvenanceBuild) {
			build.ExternalParameters = nil
		},
		"bad-build-type": func(build *ProvenanceBuild) {
			build.BuildType = "file:///tmp/build"
		},
		"bad-builder": func(build *ProvenanceBuild) {
			build.BuilderID = "runner"
		},
		"reverse-time": func(build *ProvenanceBuild) {
			build.FinishedAt = build.StartedAt.Add(-time.Second)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			build := provenanceBuildFixture()
			mutate(&build)
			if _, err := BuildSLSAProvenanceStatement(build); err == nil {
				t.Fatalf("%s provenance input was accepted", name)
			}
		})
	}
}
