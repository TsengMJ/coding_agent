# Syntax-checks Python files in memory (no __pycache__) and prints the
# summary codetools parses. Usage: python3 -c <script> [path...]
import json
import os
import sys

SKIP_DIRS = {".git", ".hg", ".svn", "node_modules", "venv", ".venv", "env", "__pycache__",
             ".tox", ".nox", ".mypy_cache", ".pytest_cache", "build", "dist", "site-packages"}
MAX_DIAGNOSTICS = 200
MAX_FILES = 20000


def python_files(paths):
    for root in paths:
        if os.path.isfile(root):
            yield root
            continue
        for dirpath, dirnames, filenames in os.walk(root):
            dirnames[:] = [d for d in dirnames if d not in SKIP_DIRS and not d.endswith(".egg-info")]
            for name in filenames:
                if name.endswith(".py"):
                    yield os.path.normpath(os.path.join(dirpath, name))


def main():
    diags = []
    for i, name in enumerate(python_files(sys.argv[1:] or ["."])):
        if i >= MAX_FILES or len(diags) >= MAX_DIAGNOSTICS:
            break
        try:
            with open(name, "rb") as f:
                compile(f.read(), name, "exec", dont_inherit=True)
        except SyntaxError as e:
            diags.append({"file": name, "line": e.lineno or 0, "column": e.offset or 0,
                          "severity": "error", "source": "python", "message": f"{type(e).__name__}: {e.msg}"})
        except (OSError, ValueError) as e:
            diags.append({"file": name, "line": 0, "severity": "error", "source": "python", "message": str(e)})
    print(json.dumps({"diagnostics": diags}))


main()
