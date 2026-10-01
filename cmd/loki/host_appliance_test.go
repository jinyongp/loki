package main

import (
	"bytes"
	"testing"
)

func TestApplianceRejectsRepairWithoutApprovalOrWithOfflineRoot(t *testing.T) {
	for _, args := range [][]string{{"repair"}, {"repair", "--approve", "--root", "/tmp/image"}, {"check", "--approve"}, {"check", "--root", "relative"}} {
		var stdout, stderr bytes.Buffer
		if code := runHostAppliance(args, &stdout, &stderr); code != 2 {
			t.Fatalf("args=%v code=%d stderr=%s", args, code, stderr.String())
		}
	}
}

func TestApplianceOfflineCheckRejectsIncompleteImage(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runHostAppliance([]string{"check", "--root", t.TempDir()}, &stdout, &stderr); code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}
