#!/usr/bin/env python3
"""Create a disposable worktree for the Remote SSH acceptance checklist.

Run on the workspace host with Python 3. No keys, network configuration, or
system packages are modified. The printed path is the only directory created.
"""

import json
import os
from pathlib import Path
import platform
import subprocess
import tempfile


def git(root, *args):
    return subprocess.run(
        ["git", "-C", str(root), *args], check=True, capture_output=True, text=True
    ).stdout.strip()


def main():
    root = Path(tempfile.mkdtemp(prefix="cursor-byok-469-", dir=Path.home()))
    (root / "src").mkdir()
    (root / "src" / "inventory.py").write_text(
        'def available_stock(reserved, total):\n    return total - reserved\n',
        encoding="utf-8",
    )
    (root / ".gitignore").write_text("ignored/\n", encoding="utf-8")
    git(root, "init", "--quiet")
    git(root, "add", ".")
    git(root, "-c", "user.name=BYOK test", "-c", "user.email=byok-test@example.invalid",
        "commit", "--quiet", "-m", "SSH acceptance fixture")
    (root / "src" / "inventory.py").write_text(
        'def available_stock(reserved, total):\n'
        '    # REMOTE_DIRTY_STOCK_469: include uncommitted workspace content.\n'
        '    return max(0, total - reserved)\n', encoding="utf-8",
    )
    (root / "src" / "untracked.py").write_text(
        'def remote_only_probe():\n    return "REMOTE_UNTRACKED_469"\n', encoding="utf-8"
    )
    (root / "ignored").mkdir()
    (root / "ignored" / "excluded.py").write_text(
        'SHOULD_NOT_BE_INDEXED_469 = True\n', encoding="utf-8"
    )
    print(json.dumps({
        "hostname": platform.node(), "system": platform.platform(),
        "uid": os.getuid() if hasattr(os, "getuid") else None,
        "workspace": str(root), "git_status": git(root, "status", "--short"),
        "expected_dirty_marker": "REMOTE_DIRTY_STOCK_469",
        "expected_untracked_marker": "REMOTE_UNTRACKED_469",
        "excluded_marker": "SHOULD_NOT_BE_INDEXED_469",
        "verification": "fixture creation only; run the Cursor acceptance checklist separately",
    }, indent=2))


if __name__ == "__main__":
    main()
