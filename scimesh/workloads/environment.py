"""Local development digest helpers for built-in SDK workload definitions.

This module is a leaf: it must not import other SDK workload modules, so the
workload definition packages (``search``, ``graph``, ``descriptors``) and the
``builtins`` registry wiring can import it without creating cycles.
"""

from __future__ import annotations

import hashlib
import platform
import sys

from rdkit import rdBase

from scimesh.sdk.integrity import installed_distribution_digest


def current_scimesh_package_digest() -> str:
    """Hash installed SciMesh Python sources for the built-in trusted adapters.

    This is a local immutable-code pin, not a package signature or container
    attestation. Consequently the built-in manifests are trusted only; an
    administrator must supply signed image metadata before enabling an
    untrusted quorum policy.
    """
    # Source/editable installs are allowed only for this explicit local
    # development helper. Registry discovery keeps the secure default.
    return installed_distribution_digest("scimesh", allow_editable=True)


def current_environment_digest() -> str:
    payload = "\n".join(
        (
            current_scimesh_package_digest(),
            f"python={sys.implementation.name}-{platform.python_version()}",
            f"rdkit={rdBase.rdkitVersion}",
            f"platform={sys.platform}-{platform.machine().lower()}",
        )
    )
    return "sha256:" + hashlib.sha256(payload.encode("utf-8")).hexdigest()
