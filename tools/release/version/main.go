package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var stableTagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type result struct {
	Version     string `json:"version"`
	Tag         string `json:"tag"`
	PreviousTag string `json:"previous_tag,omitempty"`
}

type semanticVersion struct {
	major int
	minor int
	patch int
	tag   string
}

func main() {
	bump := flag.String("bump", "patch", "release increment: patch, minor, or major")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	raw, err := exec.Command("git", "tag", "--list").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tags := strings.Fields(string(raw))
	next, err := nextVersion(tags, *bump)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err = json.NewEncoder(os.Stdout).Encode(next); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func nextVersion(tags []string, bump string) (result, error) {
	bump = strings.ToLower(strings.TrimSpace(bump))
	if bump != "patch" && bump != "minor" && bump != "major" {
		return result{}, errors.New("bump must be patch, minor, or major")
	}
	versions := make([]semanticVersion, 0, len(tags))
	for _, tag := range tags {
		match := stableTagPattern.FindStringSubmatch(strings.TrimSpace(tag))
		if match == nil {
			continue
		}
		major, _ := strconv.Atoi(match[1])
		minor, _ := strconv.Atoi(match[2])
		patch, _ := strconv.Atoi(match[3])
		versions = append(versions, semanticVersion{major: major, minor: minor, patch: patch, tag: tag})
	}
	sort.Slice(versions, func(i, j int) bool {
		if versions[i].major != versions[j].major {
			return versions[i].major > versions[j].major
		}
		if versions[i].minor != versions[j].minor {
			return versions[i].minor > versions[j].minor
		}
		return versions[i].patch > versions[j].patch
	})

	var next semanticVersion
	previous := ""
	if len(versions) == 0 {
		switch bump {
		case "major":
			next = semanticVersion{major: 1}
		default:
			next = semanticVersion{minor: 1}
		}
	} else {
		next = versions[0]
		previous = next.tag
		switch bump {
		case "patch":
			next.patch++
		case "minor":
			next.minor++
			next.patch = 0
		case "major":
			next.major++
			next.minor = 0
			next.patch = 0
		}
	}
	version := fmt.Sprintf("%d.%d.%d", next.major, next.minor, next.patch)
	return result{Version: version, Tag: "v" + version, PreviousTag: previous}, nil
}
