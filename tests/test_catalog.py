from __future__ import annotations

from unittest.mock import Mock

import pytest

from loki_mcp.catalog import CatalogTools
from loki_mcp.policy import PolicyError


def test_git_facade_routes_inspection_and_staging() -> None:
    tools = Mock()
    tools.git_diff.return_value = {"diff": True}
    tools.git_commit_context.return_value = {"configured": True}
    tools.git_stage_patch.return_value = {"staged": True}
    catalog = CatalogTools(tools)

    assert catalog.git_inspect("diff", "repo", True, "src") == {"diff": True}
    tools.git_diff.assert_called_once_with(True, "src", "repo")

    assert catalog.git_inspect("commit_context", "repo") == {"configured": True}
    tools.git_commit_context.assert_called_once_with("repo")

    assert catalog.git_stage("patch", "repo", patch="diff", reverse=True) == {"staged": True}
    tools.git_stage_patch.assert_called_once_with("diff", "repo", True, None)


def test_browser_facade_keeps_read_and_write_operations_separate() -> None:
    tools = Mock()
    tools.browser_network.return_value = {"requests": []}
    tools.browser_click.return_value = {"clicked": True}
    catalog = CatalogTools(tools)

    assert catalog.browser_observe("network", status_min=400, failed_only=True) == {
        "requests": [],
    }
    tools.browser_network.assert_called_once_with(400, True, None, 0, 100)

    assert catalog.browser_interact("click", index=7, new_tab=True) == {"clicked": True}
    tools.browser_click.assert_called_once_with(7, None, None, True)


def test_facade_rejects_unknown_actions_and_missing_action_inputs() -> None:
    catalog = CatalogTools(Mock())

    with pytest.raises(PolicyError, match="git_inspect action"):
        catalog.git_inspect("commit")
    with pytest.raises(PolicyError, match="query is required"):
        catalog.workspace_read("search")
    with pytest.raises(PolicyError, match="executable is required"):
        catalog.command_run("exec")


def test_secret_facade_routes_opaque_imports_and_public_values() -> None:
    tools = Mock()
    tools.import_secret_env.return_value = {"count": 3}
    tools.set_public_secret_value.return_value = {
        "profile": "profile", "secret": "FEATURE_ENABLED", "stored": True,
    }
    catalog = CatalogTools(tools)

    assert catalog.secret_write("import_env", "profile", import_id="opaque") == {"count": 3}
    tools.import_secret_env.assert_called_once_with("profile", "opaque")

    result = catalog.secret_write(
        "set", "profile", secret="FEATURE_ENABLED", value="true",
    )
    assert result == {"profile": "profile", "secret": "FEATURE_ENABLED", "stored": True}
    tools.set_public_secret_value.assert_called_once_with(
        "profile", "FEATURE_ENABLED", "true",
    )
    assert "true" not in repr(result)


def test_project_facade_registers_central_workflows() -> None:
    tools = Mock()
    tools.project_configuration.return_value = {"stored": True}
    catalog = CatalogTools(tools)

    result = catalog.project(
        "set_workflow", cwd="sample", workflow="development",
        steps=["sample-local/bootstrap", "sample-local/check"],
        required_secrets=["sample-local/DATABASE_URL"], timeout_seconds=900,
    )

    assert result == {"stored": True}
    tools.project_configuration.assert_called_once_with(
        "set_workflow", "sample", None, "development",
        ["sample-local/bootstrap", "sample-local/check"],
        ["sample-local/DATABASE_URL"], 900,
    )


def test_action_facade_registers_policy_without_adding_a_tool() -> None:
    tools = Mock()
    tools.set_action_policy.return_value = {"stored": True}
    catalog = CatalogTools(tools)

    result = catalog.action(
        "set", profile="sample-local", action_name="dev", cwd="sample",
        command=["node", "server.js"], all_secrets=True,
        preferred_port=43000, port_environment="PORT",
    )

    assert result == {"stored": True}
    call = tools.set_action_policy.call_args
    assert call.args[:4] == ("sample-local", "dev", "sample", ["node", "server.js"])
    assert call.args[5] is True
    assert call.args[12:14] == (43000, "PORT")
