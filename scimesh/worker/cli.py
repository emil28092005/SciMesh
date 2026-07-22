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
    parser = argparse.ArgumentParser(prog="scimesh-worker")
    parser.add_argument("--coordinator-url")
    parser.add_argument("--worker-id")
    parser.add_argument("--work-dir")
    parser.add_argument("--poll-interval", type=float)
    parser.add_argument("--request-timeout", type=float)
    parser.add_argument("--heartbeat-interval", type=float)
    args = parser.parse_args(argv)
    config = WorkerConfig.from_environment()
    overrides = {key: value for key, value in vars(args).items() if value is not None}
    if "work_dir" in overrides:
        overrides["work_dir"] = Path(overrides["work_dir"])
    config = WorkerConfig(**{**config.__dict__, **overrides})
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    client = HttpCoordinatorClient(config.coordinator_url, config.request_timeout, config.bearer_token)
    WorkerDaemon(config, client, HttpArtifactClient(config.coordinator_url, config.request_timeout, config.bearer_token), SciMeshRunner()).run_forever()
    return 0
