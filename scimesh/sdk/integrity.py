"""Independent installed-distribution content measurement for SDK allowlists."""

from __future__ import annotations

import hashlib
import importlib.util
import os
from importlib import metadata
from pathlib import Path


def installed_distribution_digest(
    distribution: metadata.Distribution | str,
    *,
    allow_editable: bool = False,
) -> str:
    """Hash installed package payload files using a stable path/length framing.

    Distribution metadata is deliberately excluded: editable/non-editable
    installers generate different RECORD and entry-point files for identical
    package code. All source/native modules and package data below declared
    top-level packages are included. Interpreter-generated ``__pycache__``
    files are excluded because they are neither stable wheel payloads nor used
    by the registry's cache-isolated discovery import.
    """
    installed = (
        metadata.distribution(distribution)
        if isinstance(distribution, str)
        else distribution
    )
    raw_top_level = installed.read_text("top_level.txt")
    if raw_top_level is None:
        raise ValueError("installed distribution does not declare top-level packages")
    declared_top_levels = [
        line.strip() for line in raw_top_level.splitlines() if line.strip()
    ]
    if any(not value.isidentifier() for value in declared_top_levels):
        raise ValueError("installed distribution declares an invalid top-level package")
    top_levels = set(declared_top_levels)
    if not top_levels:
        raise ValueError("installed distribution has no measurable top-level package")
    declared_files = tuple(installed.files or ())
    editable_bootstrap = any(
        Path(str(item)).name.startswith("__editable__")
        and Path(str(item)).suffix == ".pth"
        for item in declared_files
    )
    if editable_bootstrap and not allow_editable:
        raise ValueError(
            "editable workload installations are not accepted for secure discovery"
        )
    for item in declared_files:
        relative = Path(str(item))
        suffix = relative.suffix.lower()
        if suffix == ".pth" and not allow_editable:
            raise ValueError(
                "installed workload distribution declares a .pth bootstrap"
            )
        if suffix in {".pyc", ".pyo"} and "__pycache__" not in relative.parts:
            raise ValueError(
                "installed workload distribution declares sourceless bytecode"
            )

    selected: list[tuple[str, Path]] = []
    for top_level in sorted(top_levels):
        root = Path(str(installed.locate_file(top_level)))
        if not root.exists():
            # PEP 660 editable distributions may expose source packages through
            # a meta-path finder rather than a physical site-packages path.
            spec = importlib.util.find_spec(top_level)
            locations = (
                tuple(spec.submodule_search_locations or ()) if spec is not None else ()
            )
            if len(locations) > 1:
                raise ValueError(
                    "shared namespace packages are not supported for workload integrity"
                )
            if locations:
                root = Path(locations[0])
        if root.is_symlink():
            raise ValueError(
                "installed workload package root must not be a symbolic link"
            )
        if root.is_dir():
            candidates = root.rglob("*")
            for path in candidates:
                if path.is_symlink():
                    raise ValueError(
                        "installed workload package contains a symbolic-link payload"
                    )
                if not path.is_file():
                    continue
                relative_parts = path.relative_to(root).parts
                if "__pycache__" in relative_parts:
                    continue
                if path.suffix.lower() in {".pyc", ".pyo"}:
                    raise ValueError(
                        "installed workload package contains sourceless bytecode"
                    )
                relative = f"{top_level}/{path.relative_to(root).as_posix()}"
                selected.append((relative, path))
            continue
        module = Path(str(installed.locate_file(top_level + ".py")))
        if not module.exists():
            spec = importlib.util.find_spec(top_level)
            if spec is not None and spec.origin is not None:
                module = Path(spec.origin)
        if module.is_symlink() or not module.is_file():
            raise ValueError(
                "installed workload package contains a missing top-level payload"
            )
        selected.append((top_level + ".py", module))
    # Include declared package data outside top-level import trees. Generated
    # console wrappers and installer metadata are excluded; executable .pth and
    # sourceless bytecode payloads were rejected above. Generated pycache
    # entries are deliberately ignored and discovery imports from an empty
    # cache prefix.
    selected_names = {relative for relative, _ in selected}
    metadata_root_names = {
        Path(str(item)).parts[0]
        for item in declared_files
        if Path(str(item)).parts
        and Path(str(item)).parts[0].endswith((".dist-info", ".egg-info"))
    }
    for item in declared_files:
        relative = Path(str(item))
        text = relative.as_posix()
        if (
            not relative.parts
            or relative.parts[0] in metadata_root_names
            or text.startswith("../../../bin/")
            or "__pycache__" in relative.parts
        ):
            continue
        path = Path(str(installed.locate_file(item)))
        if path.is_symlink():
            raise ValueError(
                "installed workload distribution contains a symbolic-link payload"
            )
        if not path.is_file() or text in selected_names:
            continue
        selected.append((text, path))
        selected_names.add(text)

    entry_point_payloads = [
        (
            f".entry-points/{entry_point.group}/{entry_point.name}",
            entry_point.value.encode("utf-8"),
        )
        for entry_point in installed.entry_points
    ]
    if not selected:
        raise ValueError("installed distribution has no measurable package payload")
    digest = hashlib.sha256()
    for relative, path in sorted(selected):
        name = relative.encode("utf-8")
        payload = path.read_bytes()
        digest.update(len(name).to_bytes(4, "big"))
        digest.update(name)
        digest.update(len(payload).to_bytes(8, "big"))
        digest.update(payload)
    for relative, payload in sorted(entry_point_payloads):
        name = relative.encode("utf-8")
        digest.update(len(name).to_bytes(4, "big"))
        digest.update(name)
        digest.update(len(payload).to_bytes(8, "big"))
        digest.update(payload)
    return "sha256:" + digest.hexdigest()
