from pathlib import Path

import pytest

from loki_mcp.config import load_config


def test_loads_compact_and_extended_commands(tmp_path: Path) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text("""
root = "."

[checks]
lint = ["pnpm", "lint"]

[processes.dev]
command = ["pnpm", "dev", "--host", "127.0.0.1"]
cwd = "web"
timeout_seconds = 120
max_output_bytes = 8192

[processes.dev.environment]
NODE_ENV = "development"

[executables]
cargo = "/opt/cargo/bin/cargo"
""", encoding="utf-8")
    config = load_config(config_path)
    assert config.public_hosts == ()
    assert config.cloudflare_access_team_domain is None
    assert config.cloudflare_access_audience is None
    assert config.artifact_base_url is None
    assert config.preview_base_domain is None
    assert config.preview_access_audience is None
    assert config.checks["lint"].command == ("pnpm", "lint")
    assert config.processes["dev"].cwd == "web"
    assert config.processes["dev"].environment == {"NODE_ENV": "development"}
    assert config.executables == {"cargo": "/opt/cargo/bin/cargo"}


def test_rejects_relative_executable_path(tmp_path: Path) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text('[executables]\ncargo = "bin/cargo"\n', encoding="utf-8")
    with pytest.raises(ValueError):
        load_config(config_path)


def test_rejects_command_cwd_escape(tmp_path: Path) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text("""
[processes.dev]
command = ["pnpm", "dev"]
cwd = "../outside"
""", encoding="utf-8")
    with pytest.raises(ValueError):
        load_config(config_path)


def test_loads_cloudflare_access_settings(tmp_path: Path) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text('''
public_hosts = ["MCP.Example.com", "mcp.example.com"]
artifact_base_url = "https://mcp.example.com/artifacts/"
cloudflare_access_team_domain = "Example.cloudflareaccess.com"
cloudflare_access_audience = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
''', encoding="utf-8")
    config = load_config(config_path)
    assert config.public_hosts == ("mcp.example.com",)
    assert config.cloudflare_access_team_domain == "example.cloudflareaccess.com"
    assert config.cloudflare_access_audience == "a" * 64
    assert config.artifact_base_url == "https://mcp.example.com/artifacts"


def test_loads_preview_settings(tmp_path: Path) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text('''
cloudflare_access_team_domain = "example.cloudflareaccess.com"
cloudflare_access_audience = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
preview_base_domain = "Preview.Example.com."
preview_access_audience = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
''', encoding="utf-8")
    config = load_config(config_path)
    assert config.preview_base_domain == "preview.example.com"
    assert config.preview_access_audience == "b" * 64


def test_loads_capability_only_preview_domain(tmp_path: Path) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text('preview_base_domain = "example.com"\n', encoding="utf-8")
    config = load_config(config_path)
    assert config.preview_base_domain == "example.com"
    assert config.preview_access_audience is None


@pytest.mark.parametrize("config_text", [
    'preview_access_audience = "' + "b" * 64 + '"\n',
    'preview_base_domain = "localhost"\npreview_access_audience = "' + "b" * 64 + '"\n',
    'preview_base_domain = "preview.example.com"\npreview_access_audience = "short"\n',
])
def test_rejects_invalid_preview_settings(tmp_path: Path, config_text: str) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text(config_text, encoding="utf-8")
    with pytest.raises(ValueError):
        load_config(config_path)


@pytest.mark.parametrize("config_text", [
    'cloudflare_access_team_domain = "example.cloudflareaccess.com"\n',
    'cloudflare_access_audience = "' + "a" * 64 + '"\n',
    'cloudflare_access_team_domain = "example.com"\ncloudflare_access_audience = "' + "a" * 64 + '"\n',
    'cloudflare_access_team_domain = "example.cloudflareaccess.com"\ncloudflare_access_audience = "short"\n',
])
def test_rejects_invalid_cloudflare_access_settings(tmp_path: Path, config_text: str) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text(config_text, encoding="utf-8")
    with pytest.raises(ValueError):
        load_config(config_path)


@pytest.mark.parametrize("public_hosts", ['"mcp.example.com"', '["https://mcp.example.com"]'])
def test_rejects_invalid_public_hosts(tmp_path: Path, public_hosts: str) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text(f"public_hosts = {public_hosts}\n", encoding="utf-8")
    with pytest.raises(ValueError):
        load_config(config_path)


@pytest.mark.parametrize("artifact_url", [
    "http://mcp.example.com/artifacts",
    "https://other.example.com/artifacts",
    "https://mcp.example.com/downloads",
    "https://mcp.example.com/artifacts?token=value",
])
def test_rejects_invalid_artifact_base_url(tmp_path: Path, artifact_url: str) -> None:
    config_path = tmp_path / "config.toml"
    config_path.write_text(
        f'public_hosts = ["mcp.example.com"]\nartifact_base_url = "{artifact_url}"\n',
        encoding="utf-8",
    )
    with pytest.raises(ValueError):
        load_config(config_path)
