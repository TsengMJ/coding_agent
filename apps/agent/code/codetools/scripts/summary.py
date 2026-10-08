# Compacts a pytest junit XML report or ruff JSON output into the summary
# codetools parses. Usage: python3 -c <script> <kind> <report> <output>
import json
import os
import sys
import xml.etree.ElementTree as ET

MAX_FAILURES = 30
MAX_DIAGNOSTICS = 200
# ruff codes that mean the file cannot run: syntax errors and undefined names.
RUFF_ERRORS = ("E9", "F63", "F7", "F82")


def read(name):
    try:
        with open(name, encoding="utf-8", errors="replace") as f:
            return f.read()
    except OSError:
        return ""


def clip(s, n=1500):
    s = s or ""
    return s[:n] + "..." if len(s) > n else s


def rel(name):
    return os.path.relpath(name) if name and os.path.isabs(name) else name or ""


def pytest(text, out):
    root = ET.fromstring(text)
    for case in root.iter("testcase"):
        bad = case.find("failure")
        if bad is None:
            bad = case.find("error")
        if case.find("skipped") is not None:
            out["skipped"] += 1
        elif bad is None:
            out["passed"] += 1
        else:
            out["failed"] += 1
            message = bad.get("message", "")
            if bad.tag == "error":
                # Errors such as "collection failure" keep the cause in the "E" lines.
                cause = [l for l in (bad.text or "").splitlines() if l.startswith("E ")][-10:]
                message = "\n".join([message] + cause)
            if len(out["failures"]) < MAX_FAILURES:
                out["failures"].append({
                    "package": case.get("classname", ""),
                    "test": case.get("name", ""),
                    "file": rel(case.get("file", "")),
                    "message": clip(message),
                    "stack": clip(bad.text or "", 3000)[-3000:],
                })


def ruff(text, out):
    out["diagnostics"] = []
    for d in json.loads(text)[:MAX_DIAGNOSTICS]:
        code = d.get("code") or ""
        loc = d.get("location") or {}
        error = not code or code.startswith(RUFF_ERRORS)
        out["diagnostics"].append({
            "file": rel(d.get("filename", "")),
            "line": loc.get("row", 0),
            "column": loc.get("column", 0),
            "severity": "error" if error else "warning",
            "source": "ruff",
            "message": f"{d.get('message', '')} ({code})" if code else d.get("message", ""),
        })


def main():
    kind, report, output = sys.argv[1:4]
    out = {"passed": 0, "failed": 0, "skipped": 0, "cwd": os.getcwd(), "failures": [], "output": ""}
    try:
        text = read(report)
        if not text.strip():
            raise ValueError(f"the {kind} report was not written")
        {"pytest": pytest, "ruff": ruff}[kind](text, out)
    except Exception as e:  # reported to the model as status "error"
        out["error"] = str(e)
    raw = read(output).strip()
    out["output"] = "..." + raw[-4000:] if len(raw) > 4000 else raw
    print(json.dumps(out))


main()
