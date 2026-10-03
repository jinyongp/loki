# Native candidate preparation

The modular implementation was pushed to the user-approved `main` branch as
`0ef6cef927330dd83c8d03fb6f518e86f6a65652`. Follow-up candidate preparation fixes
normalize Windows architecture labels, decode Go JSON as UTF-8, authenticate
the disposable Ubuntu runner keyring before repairing its permissions, and
remove the owned npm workspace junction on Windows.

These are preparation results. No 0.2 product acceptance, release publication,
or existing installation transition has run.

## Prepared management artifacts

[Native manager preparation run 37143736900](https://github.com/jinyongp/loki/actions/runs/37143736900)
completed all six native jobs on source `3361550`: Linux amd64/arm64, Windows
amd64/arm64, and macOS amd64/arm64. Each uploaded artifact contains the native
management ZIP and its archive/program/dependency notice receipt. Final native
check jobs were skipped because the workflow phase was `prepare`.

## Tool inputs and candidates

[Browser input run 37143536575](https://github.com/jinyongp/loki/actions/runs/37143536575)
retains the receipt-bound Node, Chrome and FFmpeg archives for five targets.
[Full input run 37143538617](https://github.com/jinyongp/loki/actions/runs/37143538617)
retains both Linux source/package/image recipes. These runs execute no product
programs. Full runners independently authenticate Ubuntu package indices and
build their exact native closures.

Linux amd64 and arm64 project-browser candidate preparation succeeded in
[run 37143949433](https://github.com/jinyongp/loki/actions/runs/37143949433).
The workflow as a whole failed because its other platform jobs failed; the
successful Linux artifacts are preparation evidence only.

After fixing the owned Windows npm junction, Windows amd64 and both Linux
project-browser jobs succeeded in
[run 37144148472](https://github.com/jinyongp/loki/actions/runs/37144148472)
on source `70b1b0e`.

After fixing archive suffix recognition for dotted version filenames,
[full preparation run 37144659611](https://github.com/jinyongp/loki/actions/runs/37144659611)
on source `7c22963` succeeded on both native Linux targets. Its uploaded
artifacts contain the management candidate, reviewed native closures, module
ZIPs/catalogs and five owned OCI image archives. Both final check jobs were
skipped for the `prepare` phase. These are historical preparation artifacts;
fresh candidates are being retained for the corrected signature source.

## macOS input inspection and corrected signature policy

Both native macOS jobs reject the pinned Chrome for Testing 154.0.8037.92 app
before copying it into the Loki bundle. The actual native command is
`codesign --verify --deep --strict`; its error is:

```text
code has no resources but signature indicates they must be present
```

The original app inputs do not have a vendor resource seal. Native inspection in
[run 37145900978](https://github.com/jinyongp/loki/actions/runs/37145900978)
confirmed unsigned Intel code and linker ad-hoc arm64 code (flags `0x20002`,
zero special slots). Native strict arm64 code verification with
`--ignore-resources` succeeds. The earlier whole-app seal condition was an
incorrect assumption in Loki's producer, rather than a corrupt upstream input.
The corrected producer and doctor require the exact pinned Mach-O architecture
and signature kind, retain original bytes and framework aliases, and verify
arm64 code hashes with native codesign. Archive receipts and owned generation
integrity bind resources. No signature is stripped or replaced. Corrected
[project-browser preparation run 37146598113](https://github.com/jinyongp/loki/actions/runs/37146598113)
on source `f3b805e` succeeded on all five native targets, including both macOS
architectures. A follow-up fix canonicalizes macOS `/var` and `/private/var`
paths before comparing copied app containment. Final check jobs were skipped
for the prepare phase.

The refreshed [management preparation run 37146490204](https://github.com/jinyongp/loki/actions/runs/37146490204)
on source `90252cf` succeeded on all six native targets. Refreshed Linux full
[preparation run 37146492138](https://github.com/jinyongp/loki/actions/runs/37146492138)
on that source succeeded on both Linux architectures. All declared native
candidates are now prepared; none are accepted or published by these runs.

The final product validation tasks follow completed candidate preparation.
Native CI compilation, source acquisition and historical upstream
probes do not certify final product behavior or desktop SSH image rendering.
