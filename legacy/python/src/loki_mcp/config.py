from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path, PurePosixPath
from urllib.parse import urlsplit
import re
import tomllib


NAME_PATTERN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$")
ENV_PATTERN = re.compile(r"^[A-Z_][A-Z0-9_]*$")
PUBLIC_HOST_PATTERN = re.compile(
    r"^(?=.{1,253}$)(?:[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?\.)*"
    r"[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$"
)
CLOUDFLARE_AUDIENCE_PATTERN = re.compile(r"^[0-9a-f]{64}$")


@dataclass(frozen=True)
class CommandSpec:
    command: tuple[str, ...]
    cwd: str
    timeout_seconds: int
    max_output_bytes: int
    environment: dict[str, str]


@dataclass(frozen=True)
class ServerConfig:
    root: Path
    audit_log: Path
    host: str
    port: int
    public_hosts: tuple[str, ...]
    cloudflare_access_team_domain: str | None
    cloudflare_access_audience: str | None
    max_file_bytes: int
    max_write_bytes: int
    max_output_bytes: int
    max_list_entries: int
    max_search_results: int
    max_read_lines: int
    max_patch_bytes: int
    max_patch_files: int
    max_processes: int
    process_retention_seconds: int
    checks: dict[str, CommandSpec]
    processes: dict[str, CommandSpec]
    executables: dict[str, str] | None = None
    artifact_base_url: str | None = None
    preview_base_domain: str | None = None
    preview_access_audience: str | None = None


def load_config(path: Path) -> ServerConfig:
    with path.open("rb") as handle:
        raw = tomllib.load(handle)

    host = str(raw.get("host", "127.0.0.1"))
    if host not in {"127.0.0.1", "::1"}:
        raise ValueError("host must be a loopback address")

    port = _bounded_int(raw, "port", 8765, 1024, 65535)
    max_output_bytes = _bounded_int(raw, "max_output_bytes", 262_144, 4_096, 16_777_216)
    public_hosts = _load_public_hosts(raw.get("public_hosts", []))
    cloudflare_access_team_domain, cloudflare_access_audience = _load_cloudflare_access(raw)
    artifact_base_url = _load_artifact_base_url(raw.get("artifact_base_url"), public_hosts)
    preview_base_domain, preview_access_audience = _load_preview_settings(
        raw.get("preview_base_domain"),
        raw.get("preview_access_audience"),
        cloudflare_access_team_domain,
    )

    return ServerConfig(
        root=Path(str(raw.get("root", "/workspace"))),
        audit_log=Path(str(raw.get("audit_log", "/var/log/loki/mcp/audit.jsonl"))),
        host=host,
        port=port,
        public_hosts=public_hosts,
        cloudflare_access_team_domain=cloudflare_access_team_domain,
        cloudflare_access_audience=cloudflare_access_audience,
        max_file_bytes=_bounded_int(raw, "max_file_bytes", 16_777_216, 4_096, 268_435_456),
        max_write_bytes=_bounded_int(raw, "max_write_bytes", 2_097_152, 4_096, 67_108_864),
        max_output_bytes=max_output_bytes,
        max_list_entries=_bounded_int(raw, "max_list_entries", 2_000, 10, 100_000),
        max_search_results=_bounded_int(raw, "max_search_results", 500, 1, 10_000),
        max_read_lines=_bounded_int(raw, "max_read_lines", 2_000, 1, 20_000),
        max_patch_bytes=_bounded_int(raw, "max_patch_bytes", 524_288, 1_024, 16_777_216),
        max_patch_files=_bounded_int(raw, "max_patch_files", 50, 1, 1_000),
        max_processes=_bounded_int(raw, "max_processes", 3, 1, 32),
        process_retention_seconds=_bounded_int(raw, "process_retention_seconds", 3_600, 30, 86_400),
        checks=_load_commands(raw.get("checks", {}), default_timeout=900, default_output=max_output_bytes),
        processes=_load_commands(raw.get("processes", {}), default_timeout=14_400, default_output=10_485_760),
        executables=_load_executables(raw.get("executables", {})),
        artifact_base_url=artifact_base_url,
        preview_base_domain=preview_base_domain,
        preview_access_audience=preview_access_audience,
    )


def _load_public_hosts(raw: object) -> tuple[str, ...]:
    if not isinstance(raw, list) or not all(isinstance(item, str) for item in raw):
        raise ValueError("public_hosts must be an array of hostnames")

    result: list[str] = []
    for item in raw:
        hostname = item.strip().lower()
        if PUBLIC_HOST_PATTERN.fullmatch(hostname) is None:
            raise ValueError(f"invalid public hostname: {item!r}")
        if hostname not in result:
            result.append(hostname)
    return tuple(result)


def _load_cloudflare_access(raw: dict) -> tuple[str | None, str | None]:
    team_domain_raw = raw.get("cloudflare_access_team_domain")
    audience_raw = raw.get("cloudflare_access_audience")
    if team_domain_raw is None and audience_raw is None:
        return None, None
    if not isinstance(team_domain_raw, str) or not isinstance(audience_raw, str):
        raise ValueError("Cloudflare Access team domain and audience must be configured together")

    team_domain = team_domain_raw.strip().lower()
    audience = audience_raw.strip().lower()
    if (
        PUBLIC_HOST_PATTERN.fullmatch(team_domain) is None
        or not team_domain.endswith(".cloudflareaccess.com")
    ):
        raise ValueError("invalid Cloudflare Access team domain")
    if CLOUDFLARE_AUDIENCE_PATTERN.fullmatch(audience) is None:
        raise ValueError("invalid Cloudflare Access audience")
    return team_domain, audience


def _load_artifact_base_url(raw: object, public_hosts: tuple[str, ...]) -> str | None:
    if raw is None:
        return None
    if not isinstance(raw, str):
        raise ValueError("artifact_base_url must be an HTTPS URL")
    parsed = urlsplit(raw.strip())
    if (
        parsed.scheme != "https"
        or parsed.hostname not in public_hosts
        or parsed.port is not None
        or parsed.username is not None
        or parsed.password is not None
        or parsed.path.rstrip("/") != "/artifacts"
        or parsed.query
        or parsed.fragment
    ):
        raise ValueError("artifact_base_url must use a configured public host and /artifacts path")
    return f"https://{parsed.hostname}/artifacts"


def _load_preview_settings(
    domain_raw: object,
    audience_raw: object,
    team_domain: str | None,
) -> tuple[str | None, str | None]:
    if domain_raw is None and audience_raw is None:
        return None, None
    if not isinstance(domain_raw, str):
        raise ValueError("preview base domain must be configured before its Access audience")
    domain = domain_raw.strip().rstrip(".").lower()
    if PUBLIC_HOST_PATTERN.fullmatch(domain) is None or "." not in domain:
        raise ValueError("invalid preview base domain")
    if audience_raw is None:
        return domain, None
    if not isinstance(audience_raw, str):
        raise ValueError("preview Cloudflare Access audience must be a string")
    audience = audience_raw.strip().lower()
    if CLOUDFLARE_AUDIENCE_PATTERN.fullmatch(audience) is None:
        raise ValueError("invalid preview Cloudflare Access audience")
    if team_domain is None:
        raise ValueError("preview Access verification requires Cloudflare Access team settings")
    return domain, audience


def _load_commands(raw: object, *, default_timeout: int, default_output: int) -> dict[str, CommandSpec]:
    if not isinstance(raw, dict):
        raise ValueError("command configuration must be a table")

    result: dict[str, CommandSpec] = {}
    for name, value in raw.items():
        if not isinstance(name, str) or NAME_PATTERN.fullmatch(name) is None:
            raise ValueError(f"invalid command name: {name!r}")

        if isinstance(value, list):
            command = value
            cwd = "."
            timeout = default_timeout
            max_output = default_output
            environment: object = {}
        elif isinstance(value, dict):
            command = value.get("command")
            cwd = str(value.get("cwd", "."))
            timeout = int(value.get("timeout_seconds", default_timeout))
            max_output = int(value.get("max_output_bytes", default_output))
            environment = value.get("environment", {})
        else:
            raise ValueError(f"command {name!r} must be an argv array or table")

        if not isinstance(command, list) or not command or not all(isinstance(item, str) and item for item in command):
            raise ValueError(f"command {name!r} must contain a non-empty string argv array")
        _validate_cwd(cwd)
        if not 1 <= timeout <= 86_400:
            raise ValueError(f"command {name!r} timeout is out of range")
        if not 4_096 <= max_output <= 67_108_864:
            raise ValueError(f"command {name!r} output limit is out of range")
        if not isinstance(environment, dict):
            raise ValueError(f"command {name!r} environment must be a table")

        clean_environment: dict[str, str] = {}
        for key, environment_value in environment.items():
            if not isinstance(key, str) or ENV_PATTERN.fullmatch(key) is None:
                raise ValueError(f"command {name!r} has invalid environment key")
            if key in {"HOME", "PATH"} or key.startswith(("LD_", "PYTHON")):
                raise ValueError(f"command {name!r} cannot override {key}")
            if not isinstance(environment_value, str) or len(environment_value) > 4_096:
                raise ValueError(f"command {name!r} has invalid environment value")
            clean_environment[key] = environment_value

        result[name] = CommandSpec(
            command=tuple(command),
            cwd=cwd,
            timeout_seconds=timeout,
            max_output_bytes=max_output,
            environment=clean_environment,
        )
    return result


def _load_executables(raw: object) -> dict[str, str]:
    if not isinstance(raw, dict):
        raise ValueError("executables must be a table")
    result: dict[str, str] = {}
    for name, value in raw.items():
        if not isinstance(name, str) or NAME_PATTERN.fullmatch(name) is None:
            raise ValueError(f"invalid executable name: {name!r}")
        if not isinstance(value, str) or not value.startswith("/") or "\x00" in value:
            raise ValueError(f"executable {name!r} must use an absolute path")
        parsed = PurePosixPath(value)
        if ".." in parsed.parts:
            raise ValueError(f"executable {name!r} path is invalid")
        result[name] = value
    return result


def _validate_cwd(cwd: str) -> None:
    parsed = PurePosixPath(cwd)
    if parsed.is_absolute() or ".." in parsed.parts or "\\" in cwd or "\x00" in cwd:
        raise ValueError("command cwd must stay inside the workspace")


def _bounded_int(raw: dict, key: str, default: int, minimum: int, maximum: int) -> int:
    value = int(raw.get(key, default))
    if not minimum <= value <= maximum:
        raise ValueError(f"{key} must be between {minimum} and {maximum}")
    return value
