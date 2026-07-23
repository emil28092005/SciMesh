"""Console entry point for ``scimesh-worker``."""

from __future__ import annotations

import argparse
import logging
from pathlib import Path

from .artifacts import HttpArtifactClient
from .config import WorkerConfig
from .coordinator import HttpCoordinatorClient
from .daemon import WorkerDaemon
from .runners import SciMeshRunner


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        prog="scimesh-worker",
        epilog=(
            "Environment: SCIMESH_COORDINATOR_URL, SCIMESH_WORK_DIR, "
            "SCIMESH_WORKER_NAME, SCIMESH_CPU_COUNT, SCIMESH_MEMORY_MB, "
            "SCIMESH_POLL_INTERVAL, SCIMESH_REQUEST_TIMEOUT, "
            "SCIMESH_HEARTBEAT_INTERVAL, SCIMESH_CLEANUP_AFTER_SECONDS, and "
            "SCIMESH_BEARER_TOKEN. SCIMESH_WORKER_ID is a legacy/test override."
        ),
    )
    parser.add_argument("--coordinator-url")
    parser.add_argument("--worker-id")
    parser.add_argument("--work-dir")
    parser.add_argument("--worker-name")
    parser.add_argument("--cpu-count", type=int)
    parser.add_argument("--memory-mb", type=int)
    parser.add_argument("--poll-interval", type=float)
    parser.add_argument("--request-timeout", type=float)
    parser.add_argument("--heartbeat-interval", type=float)
    parser.add_argument("--cleanup-after-seconds", type=float)
    args = parser.parse_args(argv)
    overrides = {key: value for key, value in vars(args).items() if value is not None}
    if "work_dir" in overrides:
        overrides["work_dir"] = Path(overrides["work_dir"])
    try:
        config = WorkerConfig.from_environment(overrides)
    except (TypeError, ValueError) as error:
        parser.error(str(error))
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    client = HttpCoordinatorClient(config.coordinator_url, config.request_timeout, config.bearer_token)
    WorkerDaemon(
        config,
        client,
        HttpArtifactClient(config.coordinator_url, config.request_timeout, config.bearer_token),
        SciMeshRunner(),
    ).run_forever()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
