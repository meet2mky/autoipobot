#!/usr/bin/env python3
"""Runs the Chrome extension test harness in headless Chrome.

Chrome is driven through --remote-debugging-pipe (no networking needed). The
harness loads the real service-worker.js inside a Worker under a chrome.* stub
plus the real side-panel and options pages, and reports PASS/FAIL per check.

Usage: tests/extension/run.py [deadline-seconds]
Set CHROME=/path/to/chrome to override binary discovery. Exits 0 when every
check passes, 1 on failures, and 2 when no Chrome binary is available.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import time

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.join(HERE, "root")
PAGE = f"file://{ROOT}/h/index.html"
DEADLINE_S = float(sys.argv[1]) if len(sys.argv) > 1 else 90.0


def find_chrome():
    candidates = [os.environ.get("CHROME", "")]
    candidates += ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"]
    candidates += ["/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"]
    for candidate in candidates:
        if not candidate:
            continue
        if os.path.isabs(candidate) and os.access(candidate, os.X_OK):
            return candidate
        found = shutil.which(candidate)
        if found:
            return found
    return None


CHROME = find_chrome()


class Pipe:
    def __init__(self, write_fd, read_fd):
        self.write_fd = write_fd
        self.read_fd = read_fd
        self.buffer = b""
        self.next_id = 1
        self.events = []

    def send(self, method, params=None, session_id=None):
        message_id = self.next_id
        self.next_id += 1
        message = {"id": message_id, "method": method, "params": params or {}}
        if session_id:
            message["sessionId"] = session_id
        os.write(self.write_fd, json.dumps(message).encode() + b"\0")
        while True:
            response = self.read_message()
            if response.get("id") == message_id:
                if "error" in response:
                    raise RuntimeError(f"{method}: {response['error']}")
                return response.get("result", {})
            self.events.append(response)

    def read_message(self):
        while b"\0" not in self.buffer:
            chunk = os.read(self.read_fd, 65536)
            if not chunk:
                raise RuntimeError("Chrome closed the debugging pipe")
            self.buffer += chunk
        raw, _, self.buffer = self.buffer.partition(b"\0")
        return json.loads(raw.decode("utf-8", "replace"))


def main():
    if not CHROME:
        print("no Chrome/Chromium binary found; set CHROME=/path/to/chrome to run the extension harness")
        sys.exit(2)
    first, second = os.pipe(), os.pipe()
    cmd_read, cmd_write = [os.dup(fd) for fd in first]
    resp_read, resp_write = [os.dup(fd) for fd in second]
    for fd in first + second:
        os.close(fd)
    os.dup2(cmd_read, 3)
    os.dup2(resp_write, 4)

    with tempfile.TemporaryDirectory() as profile:
        proc = subprocess.Popen(
            [
                CHROME, "--headless=new", "--no-sandbox", "--disable-gpu", "--no-first-run",
                "--disable-extensions", "--allow-file-access-from-files", "--remote-debugging-pipe",
                f"--user-data-dir={profile}", "--window-size=1000,900", "about:blank",
            ],
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            pass_fds=(3, 4),
        )
        os.close(3)
        os.close(4)
        os.close(cmd_read)
        os.close(resp_write)
        pipe = Pipe(cmd_write, resp_read)
        try:
            targets = pipe.send("Target.getTargets")["targetInfos"]
            page = next(target for target in targets if target["type"] == "page")
            session = pipe.send("Target.attachToTarget", {"targetId": page["targetId"], "flatten": True})["sessionId"]
            pipe.send("Runtime.enable", session_id=session)
            pipe.send("Page.navigate", {"url": PAGE}, session_id=session)
            started = time.time()
            title = ""
            while time.time() - started < DEADLINE_S:
                result = pipe.send("Runtime.evaluate", {"expression": "document.title", "returnByValue": True}, session_id=session)
                title = result.get("result", {}).get("value", "")
                if title == "HARNESS DONE":
                    break
                time.sleep(0.5)
            results = pipe.send(
                "Runtime.evaluate",
                {"expression": "document.querySelector('#results') ? document.querySelector('#results').textContent : '(no #results)'", "returnByValue": True},
                session_id=session,
            )["result"].get("value", "")
            console = [
                event for event in pipe.events
                if event.get("method") in ("Runtime.exceptionThrown", "Runtime.consoleAPICalled")
            ]
        finally:
            try:
                pipe.send("Browser.close")
            except Exception:
                pass
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
    print(results)
    print("\nharness finished:", title == "HARNESS DONE")
    if console:
        print("\n--- runtime events ---")
        for event in console[:20]:
            print(json.dumps(event.get("params", {}))[:400])
    sys.exit(0 if title == "HARNESS DONE" and ", 0 failed/other" in results else 1)


main()
