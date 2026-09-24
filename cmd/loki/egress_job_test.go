package main

import "testing"

func TestParseEgressForwardsBoundsAndCanonicalizes(t *testing.T) {
	forwards, err := parseEgressForwards([]string{
		"5173=loki-workload:5173",
		"3000=loki-workload:3000",
	}, 18766)
	if err != nil {
		t.Fatal(err)
	}
	if len(forwards) != 2 ||
		forwards[0].port != 5173 || forwards[0].target != "loki-workload:5173" ||
		forwards[1].port != 3000 || forwards[1].target != "loki-workload:3000" {
		t.Fatalf("forwards = %#v", forwards)
	}

	for _, values := range [][]string{
		{"18766=loki-workload:18766"},
		{"80=loki-workload:80"},
		{"5173=bad"},
		{"5173=loki-workload:70000"},
		{"5173=loki-workload:5173", "5173=loki-workload:3000"},
	} {
		if _, err := parseEgressForwards(values, 18766); err == nil {
			t.Fatalf("invalid forwards accepted: %#v", values)
		}
	}
	tooMany := make([]string, maxEgressForwards+1)
	for index := range tooMany {
		tooMany[index] = "2" + string(rune('0'+index)) + "00=loki-workload:3000"
	}
	if _, err := parseEgressForwards(tooMany, 18766); err == nil {
		t.Fatal("too many forwards accepted")
	}
}

func TestLegacyEgressForwardDetectionIgnoresSharedForwardHost(t *testing.T) {
	if legacyEgressForwardRequested(0, "") {
		t.Fatal("shared forward host incorrectly enabled legacy forwarding")
	}
	if !legacyEgressForwardRequested(3000, "") || !legacyEgressForwardRequested(0, "target:3000") {
		t.Fatal("legacy forward fields did not enable legacy forwarding")
	}
}

func TestValidEnvironmentNameForProxyCredential(t *testing.T) {
	for _, value := range []string{"LOKI_JOB_PROXY_TOKEN", "_TOKEN", "A1"} {
		if !validEnvironmentName(value) {
			t.Fatalf("valid environment name rejected: %q", value)
		}
	}
	for _, value := range []string{"", "1TOKEN", "BAD-NAME", "한글"} {
		if validEnvironmentName(value) {
			t.Fatalf("invalid environment name accepted: %q", value)
		}
	}
}
