package releases

import (
	"bytes"
	"strings"
	"testing"
)

func noticeRequirementsFixture() []NoticeRequirement {
	return []NoticeRequirement{
		{Component: "chromium", Version: "1697308"},
		{Component: "gh", Version: "2.101.0", NoticeRequired: true},
	}
}

func noticeMaterialsFixture() []NoticeMaterial {
	return []NoticeMaterial{
		{
			Component: "gh",
			Version:   "2.101.0",
			Kind:      "notice",
			Filename:  "NOTICE",
			Data:      []byte("GitHub CLI notice\n"),
		},
		{
			Component: "chromium",
			Version:   "1697308",
			Kind:      "license",
			Filename:  "LICENSE",
			Data:      []byte("Chromium license\n"),
		},
		{
			Component: "gh",
			Version:   "2.101.0",
			Kind:      "license",
			Filename:  "LICENSE",
			Data:      []byte("GitHub CLI license\n"),
		},
	}
}

func TestNoticeBundleIsDeterministicAndVerifiable(t *testing.T) {
	requirements := noticeRequirementsFixture()
	materials := noticeMaterialsFixture()
	first, manifest, err := BuildNoticeBundle(requirements, materials)
	if err != nil {
		t.Fatal(err)
	}
	reversed := append([]NoticeMaterial(nil), materials...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	second, secondManifest, err := BuildNoticeBundle(requirements, reversed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("notice bundle changed when input order changed")
	}
	if len(manifest.Entries) != 3 || len(secondManifest.Entries) != 3 ||
		manifest.Entries[0].Path != "licenses/chromium/LICENSE" ||
		manifest.Entries[1].Path != "licenses/gh/LICENSE" ||
		manifest.Entries[2].Path != "notices/gh/NOTICE" {
		t.Fatalf("notice manifest = %#v", manifest)
	}
	verified, err := VerifyNoticeBundle(first, requirements)
	if err != nil {
		t.Fatal(err)
	}
	if len(verified.Entries) != len(manifest.Entries) {
		t.Fatalf("verified notice manifest = %#v", verified)
	}
}

func TestNoticeBundleRejectsIncompleteOrUnexpectedInventory(t *testing.T) {
	requirements := noticeRequirementsFixture()
	materials := noticeMaterialsFixture()

	t.Run("missing-license", func(t *testing.T) {
		filtered := materials[:1]
		if _, _, err := BuildNoticeBundle(requirements, filtered); err == nil {
			t.Fatal("notice bundle without required license material was accepted")
		}
	})

	t.Run("missing-required-notice", func(t *testing.T) {
		filtered := []NoticeMaterial{materials[1], materials[2]}
		if _, _, err := BuildNoticeBundle(requirements, filtered); err == nil {
			t.Fatal("notice bundle without required NOTICE material was accepted")
		}
	})

	t.Run("unexpected-component", func(t *testing.T) {
		withExtra := append(append([]NoticeMaterial(nil), materials...), NoticeMaterial{
			Component: "unknown",
			Version:   "1.0.0",
			Kind:      "license",
			Filename:  "LICENSE",
			Data:      []byte("unknown\n"),
		})
		if _, _, err := BuildNoticeBundle(requirements, withExtra); err == nil {
			t.Fatal("unrequired notice component was accepted")
		}
	})

	t.Run("version-drift", func(t *testing.T) {
		raw, _, err := BuildNoticeBundle(requirements, materials)
		if err != nil {
			t.Fatal(err)
		}
		changed := append([]NoticeRequirement(nil), requirements...)
		changed[0].Version = "1697309"
		if _, err = VerifyNoticeBundle(raw, changed); err == nil {
			t.Fatal("notice bundle for a different component version was accepted")
		}
	})
}

func TestNoticeBundleRejectsTamperingAndUnsafeMaterialNames(t *testing.T) {
	requirements := noticeRequirementsFixture()
	materials := noticeMaterialsFixture()
	raw, _, err := BuildNoticeBundle(requirements, materials)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([]byte(nil), raw...)
	tampered[len(tampered)/2] ^= 0x40
	if _, err = VerifyNoticeBundle(tampered, requirements); err == nil {
		t.Fatal("tampered notice bundle was accepted")
	}

	unsafe := append([]NoticeMaterial(nil), materials...)
	unsafe[0].Filename = "../NOTICE"
	if _, _, err = BuildNoticeBundle(requirements, unsafe); err == nil {
		t.Fatal("unsafe notice material path was accepted")
	}
	empty := append([]NoticeMaterial(nil), materials...)
	empty[1].Data = nil
	if _, _, err = BuildNoticeBundle(requirements, empty); err == nil {
		t.Fatal("empty license material was accepted")
	}

	duplicate := append(append([]NoticeMaterial(nil), materials...), materials[2])
	if _, _, err = BuildNoticeBundle(requirements, duplicate); err == nil ||
		!strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate notice material error = %v", err)
	}
}
