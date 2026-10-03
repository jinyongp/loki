# Declared targets and acceptance

These are source distribution contracts. A prepared archive, successful
cross-build or upstream download does not establish accepted native support.
All native product checks run after source implementation and candidate
preparation, as defined in [the delivery plan](plan.md).

| Execution target | Management command | Project-host browser | Selected full tools |
| --- | --- | --- | --- |
| Linux amd64 | Declared | Declared | Declared |
| Linux arm64 | Declared | Declared | Declared |
| Windows amd64 | Declared | Declared | Select an existing Linux WSL/SSH host |
| Windows arm64 | Declared | Select a supported execution host | Select an existing Linux WSL/SSH host |
| macOS amd64 | Declared | Declared | Select an existing Linux SSH host |
| macOS arm64 | Declared | Declared | Select an existing Linux SSH host |

The pinned Chrome for Testing contract has no native Windows arm64 input.
Management and execution-host selection remain independent. Emulated browser
execution is not claimed as a tested native artifact.

The native manager workflow covers six OS/architecture combinations; the
native project-browser workflow covers five; full candidate preparation covers
the two Linux architectures. Runner labels are explicitly selected from
[GitHub's official native runner reference](https://docs.github.com/en/actions/reference/runners/github-hosted-runners).
Native preparation runs and retained inputs are recorded in
[candidate-preparation.md](candidate-preparation.md). Management artifacts have
been prepared on six targets and project-browser artifacts on Windows amd64
and both Linux targets. macOS browser preparation is blocked by the pinned
upstream app's resource-seal failure. Final acceptance reports remain pending.
Windows Codex desktop-to-SSH-to-WSL rendering and execution location
remain an additional required check beyond native CI.

| Tool group | Public responsibility | Full private prerequisites |
| --- | --- | --- |
| workspace | Scoped files and developer guidance | runtime-core |
| git | Repository operations and optional signing | runtime-core, execution |
| execution | Managed jobs and toolchains | runtime-core |
| browser | Official Playwright/DevTools engines and owned files | runtime-core; project-host uses its standalone module |
| github | Provider API and repository-linked Projects | runtime-core |
| secrets | Application secret profiles and delivery | runtime-core |
| sharing | Owned managed endpoints | runtime-core, execution |
| coordination | Pinned devtools command adapter | runtime-core |

Installing a private prerequisite does not enable its public tools. Capability
selection, observed readiness and fresh call authorization remain separate.
Git signing is optional within Git. Personal Projects is optional within
GitHub. Official unsafe browser code execution requires its explicit capability.
Other browser capability names are declared by the exact module manifest and
enabled engine; final feature acceptance determines usable support.

Project-host browsers run with the authority of the selected host account and
an explicit project workspace. Full browsers have a separate non-root service,
private workspace/network, peer-checked adapters and an owned loopback endpoint
route. Their tabs, profiles and login state are separate from the desktop app's
in-app browser and from each other.
