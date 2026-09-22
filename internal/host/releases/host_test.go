package releases

import "testing"

func TestDetectHostRecognizesSupportedUbuntuAndWSL(t *testing.T) {
	osRelease := []byte("ID=ubuntu\nVERSION_ID=\"24.04\"\n")
	native, err := detectHost("linux", "amd64", osRelease, []byte("6.8.0-generic"))
	if err != nil {
		t.Fatal(err)
	}
	if native != (SupportedHost{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"}) {
		t.Fatalf("native host = %#v", native)
	}
	wsl, err := detectHost("linux", "amd64", osRelease, []byte("6.6.87.2-microsoft-standard-WSL2"))
	if err != nil {
		t.Fatal(err)
	}
	if wsl.Environment != "wsl" {
		t.Fatalf("WSL host = %#v", wsl)
	}

	for name, tc := range map[string]struct {
		goos      string
		goarch    string
		osRelease []byte
	}{
		"wrong-os":     {goos: "darwin", goarch: "amd64", osRelease: osRelease},
		"wrong-arch":   {goos: "linux", goarch: "arm64", osRelease: osRelease},
		"wrong-distro": {goos: "linux", goarch: "amd64", osRelease: []byte("ID=debian\nVERSION_ID=12\n")},
		"wrong-version": {goos: "linux", goarch: "amd64",
			osRelease: []byte("ID=ubuntu\nVERSION_ID=22.04\n")},
		"missing-id": {goos: "linux", goarch: "amd64", osRelease: []byte("VERSION_ID=24.04\n")},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := detectHost(tc.goos, tc.goarch, tc.osRelease, nil); err == nil {
				t.Fatalf("%s host was accepted", name)
			}
		})
	}
}

func TestParseHostOSReleaseRejectsControlCharacters(t *testing.T) {
	if _, err := parseHostOSRelease([]byte("ID=ubuntu\nVERSION_ID=\"24.04\x00\"\n")); err == nil {
		t.Fatal("NUL host identity was accepted")
	}
	if _, err := parseHostOSRelease([]byte("ID=ubuntu\nVERSION_ID=\"24.04\rbad\"\n")); err == nil {
		t.Fatal("CR host identity was accepted")
	}
}
