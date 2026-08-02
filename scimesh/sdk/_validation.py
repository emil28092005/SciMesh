"""Internal validation helpers for strict, JSON-safe SDK value objects."""

from __future__ import annotations

import json
import math
import re
from enum import Enum
from types import MappingProxyType
from typing import Any, Mapping
from urllib.parse import unquote
from uuid import UUID


WORKLOAD_NAME_PATTERN = re.compile(r"^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$")
IDENTIFIER_PATTERN = re.compile(r"^[a-z][a-z0-9]*(?:[-_.][a-z0-9]+)*$")
ENTRY_POINT_PATTERN = re.compile(
    r"^[A-Za-z_][A-Za-z0-9_.]*:[A-Za-z_][A-Za-z0-9_.]*(?:@v[1-9][0-9]*)?$"
)
SEMVER_PATTERN = re.compile(
    r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?"
    r"(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$"
)
_VERSION_PATTERN = re.compile(r"^(0|[1-9][0-9]*)(?:\.(0|[1-9][0-9]*))?(?:\.(0|[1-9][0-9]*))?$")
_VERSION_CLAUSE_PATTERN = re.compile(r"^(==|>=|<=|>|<)\s*(.+)$")
_FORBIDDEN_LOCATOR_PREFIXES = (
    "file://",
    "worker://",
    "http://",
    "https://",
    "s3://",
    "/",
)
_URI_SCHEME_PATTERN = re.compile(r"^[A-Za-z][A-Za-z0-9+.-]*:")
_WINDOWS_PATH_PATTERN = re.compile(r"^[A-Za-z]:(?:[\\/]|[^\s]*[\\/])")
_SECRET_ASSIGNMENT_PATTERN = re.compile(
    r"(?i)(?:^|[^A-Za-z0-9_])"
    r"(?:authorization|bearer|token|secret|password|api[-_]?key)\s*[:=]"
)
_PATH_ASSIGNMENT_PATTERN = re.compile(
    r"(?i)(?:^|[^A-Za-z0-9_])"
    r"(?:path|file|directory|dir|workspace|cwd|upload|download)\s*[:=]"
)
_PATH_SEGMENT_PATTERN = re.compile(r"^[A-Za-z0-9_.-]+$")
_FILE_NAME_PATTERN = re.compile(r"^[A-Za-z0-9_.-]+\.[A-Za-z0-9]{1,16}$")
_TASK_KEY_COMPONENT_PATTERN = re.compile(r"^[a-z0-9][a-z0-9_.-]*$")


def require_exact_keys(
    value: Mapping[str, object],
    expected: set[str],
    label: str,
    *,
    optional: set[str] | None = None,
) -> None:
    """Reject unknown fields and report missing required fields."""
    if any(not isinstance(key, str) for key in value):
        raise ValueError(f"{label} must use string field names")
    optional = optional or set()
    actual = set(value)
    missing = expected - actual
    unknown = actual - expected - optional
    if not missing and not unknown:
        return
    details: list[str] = []
    if missing:
        details.append("missing " + ", ".join(sorted(missing)))
    if unknown:
        details.append("unknown " + ", ".join(sorted(unknown)))
    raise ValueError(f"{label} has invalid fields: {'; '.join(details)}")


def require_mapping(value: object, field: str) -> Mapping[str, object]:
    if not isinstance(value, Mapping) or any(not isinstance(key, str) for key in value):
        raise ValueError(f"{field} must be an object with string keys")
    return value


def require_string(value: object, field: str, *, max_length: int = 256) -> str:
    if not isinstance(value, str) or not value.strip() or len(value) > max_length:
        raise ValueError(f"{field} must be a non-empty string of at most {max_length} characters")
    if any(ord(character) < 32 for character in value):
        raise ValueError(f"{field} must not contain control characters")
    return value


def require_identifier(value: object, field: str) -> str:
    text = require_string(value, field, max_length=128)
    if not IDENTIFIER_PATTERN.fullmatch(text):
        raise ValueError(f"{field} must be a canonical identifier")
    return text


def require_workload_name(value: object, field: str = "workload.name") -> str:
    text = require_string(value, field, max_length=128)
    if not WORKLOAD_NAME_PATTERN.fullmatch(text):
        raise ValueError(f"{field} must be a canonical hyphenated workload name")
    return text


def require_entry_point(value: object, field: str) -> str:
    text = require_string(value, field, max_length=256)
    if not ENTRY_POINT_PATTERN.fullmatch(text):
        raise ValueError(f"{field} must be a package-owned module:object entry point")
    return text


def require_semver(value: object, field: str) -> str:
    text = require_string(value, field, max_length=64)
    match = SEMVER_PATTERN.fullmatch(text)
    if match is None:
        raise ValueError(f"{field} must be a semantic version such as 1.0.0")
    prerelease = match.group(4)
    if prerelease is not None and any(
        identifier.isdigit() and len(identifier) > 1 and identifier.startswith("0")
        for identifier in prerelease.split(".")
    ):
        raise ValueError(f"{field} has a non-canonical numeric prerelease identifier")
    return text


def require_uuid(value: object, field: str) -> str:
    if not isinstance(value, str):
        raise ValueError(f"{field} must be a UUID string")
    try:
        return str(UUID(value))
    except ValueError as error:
        raise ValueError(f"{field} must be a UUID string") from error


def require_sha256(value: object, field: str, *, prefixed: bool = False) -> str:
    if not isinstance(value, str):
        raise ValueError(f"{field} must be a SHA-256 digest")
    digest = value[7:] if prefixed and value.startswith("sha256:") else value
    if prefixed and not value.startswith("sha256:"):
        raise ValueError(f"{field} must use the sha256:<hex> form")
    if not re.fullmatch(r"[0-9a-f]{64}", digest):
        raise ValueError(f"{field} must be a lowercase SHA-256 digest")
    return f"sha256:{digest}" if prefixed else digest


def require_nonnegative_int(value: object, field: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value < 0:
        raise ValueError(f"{field} must be a non-negative integer")
    return value


def require_positive_int(value: object, field: str) -> int:
    result = require_nonnegative_int(value, field)
    if result == 0:
        raise ValueError(f"{field} must be a positive integer")
    return result


def require_schema_version(value: object, expected: int, field: str) -> int:
    if isinstance(value, bool) or not isinstance(value, int) or value != expected:
        raise ValueError(f"{field} must be the integer {expected}")
    return value


def require_task_key(value: object, field: str = "task_key") -> str:
    text = require_string(value, field, max_length=256)
    parts = text.split("/")
    if any(
        not part or part in {".", ".."} or not _TASK_KEY_COMPONENT_PATTERN.fullmatch(part)
        for part in parts
    ):
        raise ValueError(f"{field} must be a canonical workflow-relative key")
    return text


def contains_unsafe_location(value: str) -> bool:
    stripped = value.strip()
    variants = [stripped]
    for _ in range(2):
        decoded = unquote(variants[-1])
        if decoded == variants[-1]:
            break
        variants.append(decoded)
    for candidate in variants:
        if _SECRET_ASSIGNMENT_PATTERN.search(candidate):
            return True
        fragments = (candidate,) + tuple(
            fragment
            for fragment in re.split(r"[\s=\"'()\[\]{}<>;,]+", candidate)
            if fragment
        )
        for fragment in fragments:
            lower = fragment.lower()
            normalized = fragment.replace("\\", "/")
            segments = normalized.split("/")
            looks_relative = (
                len(segments) >= 3
                and all(_PATH_SEGMENT_PATTERN.fullmatch(segment) for segment in segments)
            ) or (
                len(segments) >= 2
                and all(_PATH_SEGMENT_PATTERN.fullmatch(segment) for segment in segments)
                and bool(_FILE_NAME_PATTERN.fullmatch(segments[-1]))
            )
            if (
                bool(_URI_SCHEME_PATTERN.match(fragment))
                or lower.startswith(tuple(prefix.lower() for prefix in _FORBIDDEN_LOCATOR_PREFIXES))
                or fragment.startswith(("./", "../", "~/", "\\\\"))
                or bool(_WINDOWS_PATH_PATTERN.match(fragment))
                or any(segment == ".." for segment in segments)
                or looks_relative
                or (
                    _PATH_ASSIGNMENT_PATTERN.search(candidate) is not None
                    and ("/" in fragment or "\\" in fragment)
                )
            ):
                return True
    return False


def require_safe_message(value: object, field: str, *, max_length: int = 512) -> str:
    text = require_string(value, field, max_length=max_length)
    tokens = (text,) + tuple(text.split())
    if any(contains_unsafe_location(token.strip("'\"()[]{}<>,;")) for token in tokens):
        raise ValueError(f"{field} must not contain a URI or local path")
    return text


def require_opaque_resource_id(value: object, field: str) -> str:
    """Validate a non-secret resource handle without treating it as a locator."""
    text = require_string(value, field, max_length=160)
    if (
        contains_unsafe_location(text)
        or "/" in text
        or "\\" in text
        or "," in text
        or any(character.isspace() for character in text)
    ):
        raise ValueError(f"{field} must be an opaque single resource identifier")
    return text


def require_finite_number(value: object, field: str) -> int | float:
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise ValueError(f"{field} must be a finite number")
    if isinstance(value, int):
        if abs(value).bit_length() > 4096:
            raise ValueError(f"{field} exceeds the 4096-bit integer bound")
        return value
    if not math.isfinite(value):
        raise ValueError(f"{field} must be a finite number")
    return value


def freeze_json(
    value: object,
    field: str,
    *,
    forbid_locations: bool = False,
    _depth: int = 0,
) -> Any:
    """Return an immutable deep copy of a JSON value.

    Scientific task parameters use ``forbid_locations`` so durable payloads
    cannot smuggle worker-local paths or transport URLs. Manifests and verifier
    evidence use ordinary JSON validation because JSON Schema keywords and
    sanitized references may legitimately contain URI-shaped strings.
    """
    if _depth > 64:
        raise ValueError(f"{field} nesting exceeds 64 levels")
    if value is None or isinstance(value, bool):
        return value
    if isinstance(value, int):
        if abs(value).bit_length() > 4096:
            raise ValueError(f"{field} contains an integer above the 4096-bit JSON bound")
        return value
    if isinstance(value, str):
        if any(ord(character) < 32 for character in value):
            raise ValueError(f"{field} must not contain control characters")
        if forbid_locations and contains_unsafe_location(value):
            raise ValueError(f"{field} must not contain a URI or local path")
        return value
    if isinstance(value, float):
        if not math.isfinite(value):
            raise ValueError(f"{field} must not contain NaN or infinity")
        return value
    if isinstance(value, Mapping):
        frozen: dict[str, Any] = {}
        for key, child in value.items():
            if not isinstance(key, str):
                raise ValueError(f"{field} must use string object keys")
            frozen[key] = freeze_json(
                child,
                f"{field}.{key}",
                forbid_locations=forbid_locations,
                _depth=_depth + 1,
            )
        return MappingProxyType(frozen)
    if isinstance(value, (list, tuple)):
        return tuple(
            freeze_json(
                child,
                f"{field}[]",
                forbid_locations=forbid_locations,
                _depth=_depth + 1,
            )
            for child in value
        )
    raise ValueError(f"{field} must contain only JSON-compatible values")


def freeze_json_mapping(
    value: object,
    field: str,
    *,
    forbid_locations: bool = False,
) -> Mapping[str, Any]:
    mapping = require_mapping(value, field)
    frozen = freeze_json(mapping, field, forbid_locations=forbid_locations)
    assert isinstance(frozen, Mapping)
    return frozen


def thaw_json(value: object) -> Any:
    if isinstance(value, Mapping):
        return {key: thaw_json(child) for key, child in value.items()}
    if isinstance(value, tuple):
        return [thaw_json(child) for child in value]
    return value


def canonical_json(value: object) -> str:
    return json.dumps(thaw_json(value), sort_keys=True, separators=(",", ":"), allow_nan=False)


def parse_release(value: object, field: str = "version") -> tuple[int, int, int]:
    text = require_string(value, field, max_length=32)
    match = _VERSION_PATTERN.fullmatch(text)
    if match is None:
        raise ValueError(f"{field} must contain one to three numeric release components")
    return tuple(int(part or 0) for part in match.groups())  # type: ignore[return-value]


def validate_version_range(expression: object, field: str) -> str:
    text = require_string(expression, field, max_length=128)
    clauses = [clause.strip() for clause in text.split(",")]
    if not clauses or any(not clause for clause in clauses):
        raise ValueError(f"{field} must be an explicit version range")
    canonical_clauses: list[str] = []
    for clause in clauses:
        match = _VERSION_CLAUSE_PATTERN.fullmatch(clause)
        if match is None:
            raise ValueError(f"{field} must use ==, >=, <=, >, or < clauses")
        bound = match.group(2).strip()
        parse_release(bound, field)
        canonical_clauses.append(match.group(1) + bound)
    return ",".join(canonical_clauses)


def version_in_range(version: object, expression: str) -> bool:
    candidate = parse_release(version)
    for clause in expression.split(","):
        match = _VERSION_CLAUSE_PATTERN.fullmatch(clause)
        assert match is not None
        operator, raw_bound = match.groups()
        bound = parse_release(raw_bound)
        if operator == "==" and candidate != bound:
            return False
        if operator == ">=" and candidate < bound:
            return False
        if operator == "<=" and candidate > bound:
            return False
        if operator == ">" and candidate <= bound:
            return False
        if operator == "<" and candidate >= bound:
            return False
    return True


def enum_value(enum_type: type[Enum], value: object, field: str) -> Any:
    try:
        return enum_type(value)
    except (TypeError, ValueError) as error:
        allowed = ", ".join(member.value for member in enum_type)
        raise ValueError(f"{field} must be one of: {allowed}") from error
