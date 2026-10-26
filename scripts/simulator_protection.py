"""Load private, checkout-local simulator protections without external dependencies."""
import json
from pathlib import Path
import uuid


def load_protected_simulator_ids(configuration_path, error_type):
    configuration_path = Path(configuration_path)
    try:
        try:
            configured_simulator_ids = json.loads(configuration_path.read_text())
        except FileNotFoundError:
            if configuration_path.is_symlink():
                raise ValueError("Broken protection configuration symlink")
            return set()
        if not isinstance(configured_simulator_ids, list):
            raise ValueError("Protection configuration must be a JSON list")
        protected_simulator_ids = set()
        for simulator_id in configured_simulator_ids:
            if not isinstance(simulator_id, str) or str(uuid.UUID(simulator_id)).upper() != simulator_id.upper():
                raise ValueError("Protection configuration must contain simulator UUIDs")
            protected_simulator_ids.add(simulator_id.upper())
        return protected_simulator_ids
    except (OSError, ValueError) as exc:
        raise error_type("Cannot read protected simulator configuration; expected a JSON list of UUIDs") from exc
