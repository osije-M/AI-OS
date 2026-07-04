"""
pytest bootstrap: make `import graph` work regardless of the cwd pytest is invoked from
(repo root via `pytest pyagent` or from inside pyagent/ via `pytest`).
"""
import os
import sys

_TESTS_DIR = os.path.dirname(os.path.abspath(__file__))
_PYAGENT_DIR = os.path.dirname(_TESTS_DIR)
_GEN_DIR = os.path.join(_PYAGENT_DIR, "gen")

for _p in (_PYAGENT_DIR, _GEN_DIR):
    if _p not in sys.path:
        sys.path.insert(0, _p)
