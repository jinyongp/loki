---
name: selected-tools
description: Use when the user selects the Loki Tools connection for the enabled workspace, Git, execution, GitHub, secrets, sharing, coordination or browser tools on its execution host.
---

# Selected tools

Discover the currently enabled tools and use their actual schemas. Installing a
private prerequisite does not expose its tool group. Disabled or unready groups
may be absent from discovery. A rejected cached call requires refreshed discovery
or reconnecting after host configuration changes.

Workspace paths refer to the host's managed `/workspace`. Git signing belongs
to Git and its protected integration setup. Provider credentials belong to the
GitHub integration; application secrets belong to the separate secrets tools.
Repository-linked Projects use installation authorization. Personal Projects
requires an explicitly selected optional capability.

When browser tools are selected, identify them as the Loki project browser on
this host. Its tabs and login state belong to this connection. Use it when the
user selects this browser connection. A request for the desktop in-app browser
requires that app's integration or an explicit user choice of another connection.

Read the returned setup guidance if a group is unready. Use `loki doctor`,
`loki tools start`, and the matching `loki integrations` command on the selected
execution host when authorized by the task. Keep credentials in protected input
files or stdin. Public status and command arguments contain no secret values.
