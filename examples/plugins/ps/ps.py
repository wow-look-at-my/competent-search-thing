#!/usr/bin/env python3
"""Processes plugin for competent-search-thing.

Bang-targeted only ("!ps fire"): the manifest declares no trigger, so
ordinary queries never reach this plugin. It filters the running-app
snapshot the searchbar sends because the manifest declares
"context": ["running"] -- context parts a plugin does not declare are
never sent to it.
"""

import json
import sys

MAX_RESULTS = 15


def _result(app, needle):
    name = str(app.get("name") or "")
    title = str(app.get("title") or "")
    exe = str(app.get("exe") or "")
    pid = app.get("pid") or 0
    result = {
        "title": name,
        "subtitle": title or exe,
        "icon": "app",
        "badge": "PS",
        "score": 100 if name.lower().startswith(needle) else 70,
    }
    fields = []
    if pid:
        fields.append({"label": "PID", "value": str(pid)})
    if exe:
        fields.append({"label": "Exe", "value": exe})
    if fields:
        result["fields"] = fields
    if pid:
        result["action"] = {"type": "copy_text", "value": str(pid)}
    return result


def main():
    request = json.load(sys.stdin)
    context = request.get("context") or {}
    apps = context.get("running_apps") or []
    needle = str(request.get("stripped") or "").strip().lower()
    results = []
    for app in apps:
        name = str(app.get("name") or "")
        if not name:
            continue  # a result needs a non-empty title
        title = str(app.get("title") or "")
        if needle and needle not in name.lower() and needle not in title.lower():
            continue
        results.append(_result(app, needle))
        if len(results) >= MAX_RESULTS:
            break
    return results


if __name__ == "__main__":
    try:
        results = main()
    except Exception:  # any problem means "no results", never a crash
        results = []
    json.dump({"v": 1, "results": results}, sys.stdout)
