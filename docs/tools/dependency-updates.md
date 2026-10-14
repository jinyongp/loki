# Dependency update proposals

A Loki release uses reviewed, pinned inputs. A newer upstream version does not
invalidate those inputs and does not block a source check or publication.
`verify_release_tools.py` checks local pin consistency without network access.
Acquisition still verifies the accepted archive sizes, checksums, source commits
and native runtime behavior. Those integrity and product gates remain required.

The **Dependency update proposals** workflow runs each Monday at approximately
10:17 Asia/Seoul (01:17 UTC), or on manual dispatch. GitHub schedules can be
delayed. The workflow only queries primary upstream metadata and manages one
tracking issue; it never changes a pin, creates a PR, merges or publishes Loki.
It uses the repository `GITHUB_TOKEN` with contents read and issues write; no
personal token or application installation is needed.

## Coverage and acceptance

The inventory reads the real manifests and receipts: direct Go/npm dependencies,
Go, Node, Chrome, Playwright's coupled FFmpeg revision, gh, devtools, ripgrep,
Git from the accepted Ubuntu series, every pinned workflow Action, Buildx,
BuildKit, and native image digests for existing base/build tags. Go/npm transitive
updates and OS library closures are reviewed with their owning dependency PR;
this investigation is not a vulnerability scanner. New major versions require
compatibility review. A release being labelled stable or having aged does not
establish that it works with Loki.

The issue uses dependency sections rather than tables, with accepted → proposed
versions, official release links and publication dates. Long references and multiple
pin locations are collapsed; shared review instructions appear once. Image proposals stay on the accepted
OS/tool tag. A new Ubuntu release does not silently change the distribution.
FFmpeg follows the revision required by the latest stable Playwright MCP, not
an unrelated FFmpeg release stream. Node's channel is explicit in the policy:
`current` (default, existing policy) or `lts`. Candidates older than an accepted
version are not proposed for versioned tools.

Adopting a proposal is a separate, reviewed PR. Update all linked manifests,
lockfiles, commits, receipts and notices together; run the affected native build
and acceptance checks before merging. Publishing remains a separate action.

## Holds and one issue

Edit `packaging/tools/dependency-policy.json` to hold a specific candidate or all
candidates through a UTC calendar date. Each hold needs a reason and either
`version` or `until`. Exact image versions include `sha256:`; exact Action versions
are commit SHAs, as shown in the report. For example:

```json
{
  "schema": 1,
  "channels": {"node": "current"},
  "holds": {
    "chrome": {
      "version": "155.0.8059.79",
      "reason": "Wait for native regression investigation"
    },
    "npm:@playwright/mcp": {
      "until": "2026-11-01",
      "reason": "Keep the accepted browser API during the current release cycle"
    }
  }
}
```

Only currently used inventory IDs are accepted. Each ID has one hold entry, so
holding multiple historical releases does not produce a growing version list.
A version hold does not suppress the next different candidate; date holds expire
automatically. Remove obsolete holds when reviewing dependency updates.

The workflow finds its bot-owned `dependency-watch` issue by a fixed managed
marker. It updates the same issue, closes it when every proposal is handled and
reopens it when an actionable candidate appears. No actionable candidates on the
first run means no issue is created. Held candidates stay visible in an existing
issue. An unchanged report produces no body update and no comment. New candidates
replace old rows. Human notes outside the managed section are retained. Existing
owned duplicates are closed only after their human notes have been copied once
to the canonical issue. Runs are serialized to prevent duplicate creation.

A failed lookup is **unknown**, not current. It preserves the previous candidate
when the accepted pin has not changed and keeps an open issue open. An entirely
failed first investigation creates no empty issue. Other sources are still
investigated; the workflow reports failure with its partial JSON artifact and
summary. Release CI has no dependency on this workflow. Report artifacts expire
after 14 days; results are not appended as issue comments.

## Local investigation

```sh
python3 scripts/maintainer/tools/verify_release_tools.py
python3 scripts/maintainer/tools/dependency_watch.py --output /tmp/loki-dependency-report.json
```

The second command performs read-only upstream discovery. Set `GH_TOKEN` for
GitHub API authentication. Publishing is explicit (`--publish`) and normally runs
only in the dedicated workflow. The report contains public metadata, never API
credentials. Failed API calls have bounded timeouts and retries.
