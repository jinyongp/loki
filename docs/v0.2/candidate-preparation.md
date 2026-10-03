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
skipped for the `prepare` phase. Preparation completeness remains open because
the two macOS browser artifacts could not be produced.

## Open macOS preparation blocker

Both native macOS jobs reject the pinned Chrome for Testing 154.0.8037.92 app
before copying it into the Loki bundle. The actual native command is
`codesign --verify --deep --strict`; its error is:

```text
code has no resources but signature indicates they must be present
```

The downloaded primary-source archives contain no `_CodeSignature` resource
entries. Their byte lengths and hashes match the reviewed recipes. This failure
therefore precedes Loki repackaging; replacing framework aliases is not a fix.
The producer retains its vendor-seal requirement and does not strip or replace
the upstream signature. A usable signed upstream input or an explicit revised
macOS signing/distribution design is needed before this gate can close.

The final product validation tasks remain pending until candidate preparation
is complete. Native CI compilation, source acquisition and historical upstream
probes do not certify final product behavior or desktop SSH image rendering.
