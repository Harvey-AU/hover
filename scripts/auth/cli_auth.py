#!/usr/bin/env python3
"""Utility to manage Supabase CLI authentication for load-testing scripts."""
from __future__ import annotations

import argparse
import base64
import hashlib
import http.server
import json
import os
import secrets
import sys
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import webbrowser
from pathlib import Path

try:
    from . import config
except ImportError:
    # Fall back when running as a standalone script
    import config

DEFAULT_AUTH_URL = os.environ.get("SUPABASE_AUTH_URL", config.SUPABASE_URL)
DEFAULT_ANON_KEY = os.environ.get("SUPABASE_ANON_KEY", config.DEFAULT_SUPABASE_ANON_KEY)
DEFAULT_PROVIDER = os.environ.get("BBB_AUTH_PROVIDER", "google")
DEFAULT_CALLBACK_PORT = int(os.environ.get("BBB_AUTH_CALLBACK_PORT", "8765"))
LOGIN_PAGE_URL = os.environ.get("BBB_LOGIN_URL", "https://hover.app.goodnative.co/cli-login.html")
TOKEN_SKEW_SECONDS = 90


def _config_dir() -> Path:
    override = os.environ.get("BBB_AUTH_DIR")
    if override:
        return Path(override).expanduser()

    if sys.platform.startswith("win"):
        base = os.environ.get("APPDATA")
        if not base:
            base = Path.home() / "AppData" / "Roaming"
        else:
            base = Path(base)
        return Path(base) / "Hover" / "auth"

    xdg = os.environ.get("XDG_CONFIG_HOME")
    base_path = Path(xdg) if xdg else Path.home() / ".config"
    return base_path / "hover" / "auth"


CONFIG_DIR = _config_dir()
SESSION_FILE = Path(os.environ.get("BBB_SESSION_FILE", CONFIG_DIR / "session.json"))


def _debug(msg: str) -> None:
    print(msg, file=sys.stderr)


def _base64url(data: bytes) -> str:
    return base64.urlsafe_b64encode(data).rstrip(b"=").decode("ascii")


def _generate_code_verifier() -> str:
    # between 43 and 128 chars per RFC 7636
    raw = _base64url(secrets.token_bytes(64))
    while len(raw) < 43:
        raw += _base64url(secrets.token_bytes(16))
    return raw[:128]


def _generate_code_challenge(verifier: str) -> str:
    digest = hashlib.sha256(verifier.encode("utf-8")).digest()
    return _base64url(digest)


def _load_session() -> dict | None:
    try:
        with SESSION_FILE.open("r", encoding="utf-8") as fh:
            return json.load(fh)
    except FileNotFoundError:
        return None
    except json.JSONDecodeError:
        _debug(f"Warning: could not parse session file at {SESSION_FILE}, ignoring")
        return None


def _save_session(data: dict) -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    tmp_path = SESSION_FILE.with_suffix(".tmp")
    with tmp_path.open("w", encoding="utf-8") as fh:
        json.dump(data, fh, indent=2)
    tmp_path.replace(SESSION_FILE)
    try:
        os.chmod(SESSION_FILE, 0o600)
    except PermissionError:
        pass


def _is_token_valid(session: dict) -> bool:
    expires_at = session.get("expires_at")
    now = time.time()
    if expires_at is None and session.get("expires_in") and session.get("fetched_at"):
        expires_at = session["fetched_at"] + session["expires_in"]
    if expires_at is None:
        return False
    return float(expires_at) - TOKEN_SKEW_SECONDS > now


def _request_json(url: str, payload: dict) -> dict:
    data = json.dumps(payload).encode("utf-8")
    headers = {
        "Content-Type": "application/json",
        "Accept": "application/json",
        "apikey": DEFAULT_ANON_KEY,
        "Authorization": f"Bearer {DEFAULT_ANON_KEY}",
    }
    req = urllib.request.Request(url, data=data, headers=headers, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            charset = resp.headers.get_content_charset() or "utf-8"
            return json.loads(resp.read().decode(charset))
    except urllib.error.HTTPError as exc:
        body = exc.read().decode("utf-8", errors="replace")
        raise RuntimeError(f"HTTP {exc.code}: {body}") from exc


class _AuthCallbackHandler(http.server.BaseHTTPRequestHandler):
    expected_state: str = ""
    event: threading.Event | None = None
    result: dict | None = None

    def log_message(self, format: str, *args) -> None:
        return  # Silence default logging

    def _allowed_origin(self) -> str | None:
        origin = self.headers.get("Origin")
        if not origin:
            return None
        try:
            parsed = urllib.parse.urlparse(origin)
        except ValueError:
            return None
        host = (parsed.hostname or "").lower()
        if host in {"127.0.0.1", "localhost"}:
            return origin
        if host == "goodnative.co" or host.endswith(".goodnative.co"):
            return origin
        if host == "fly.dev" or host.endswith(".fly.dev"):
            return origin
        return None

    def _send_cors_headers(self, origin: str | None) -> None:
        if origin:
            self.send_header("Access-Control-Allow-Origin", origin)
            self.send_header("Vary", "Origin")
        self.send_header("Access-Control-Allow-Methods", "POST, OPTIONS")
        self.send_header("Access-Control-Allow-Headers", "Content-Type")
        self.send_header("Access-Control-Max-Age", "600")

    def do_OPTIONS(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path != "/callback":
            self.send_response(404)
            self._send_cors_headers(self._allowed_origin())
            self.end_headers()
            return

        self.send_response(204)
        self._send_cors_headers(self._allowed_origin())
        self.end_headers()

    def _finish(self, status: int, message: str) -> None:
        body = (
            f"<html><body><h2>{message}</h2>"
            "<p>You can close this tab now.</p></body></html>"
        )
        self.send_response(status)
        origin = self._allowed_origin()
        if origin:
            self.send_header("Access-Control-Allow-Origin", origin)
            self.send_header("Vary", "Origin")
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(body.encode("utf-8"))))
        self.end_headers()
        self.wfile.write(body.encode("utf-8"))

    def do_GET(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path != "/callback":
            self._finish(404, "Callback path not found")
            return

        if self.result is not None and "session" in self.result:
            self._finish(200, "Session already received")
            return

        self._finish(200, "Waiting for CLI login...")

    def do_POST(self) -> None:
        parsed = urllib.parse.urlparse(self.path)
        if parsed.path != "/callback":
            self._finish(404, "Callback path not found")
            return

        query = urllib.parse.parse_qs(parsed.query)
        state = query.get("state", [""])[0]
        if state != self.expected_state:
            self._finish(400, "Invalid state; login aborted")
            if self.result is not None:
                self.result.clear()
                self.result["error"] = "state_mismatch"
            if self.event:
                self.event.set()
            return

        length_header = self.headers.get("Content-Length", "0")
        try:
            length = int(length_header)
        except ValueError:
            length = 0
        body = self.rfile.read(length)
        try:
            payload = json.loads(body.decode("utf-8"))
        except json.JSONDecodeError:
            self._finish(400, "Invalid JSON payload")
            return

        session = payload.get("session") if isinstance(payload, dict) else None
        if session is None and isinstance(payload, dict):
            session = payload

        if not isinstance(session, dict) or not session.get("access_token"):
            self._finish(400, "Session payload missing access_token")
            return

        if self.result is not None:
            self.result.clear()
            self.result["session"] = session
        self._finish(200, "Authentication complete")
        if self.event:
            self.event.set()


def _start_callback_server(state: str, port: int):
    event = threading.Event()
    result: dict[str, object] = {}

    class Handler(_AuthCallbackHandler):  # type: ignore[misc, valid-type]
        pass

    Handler.expected_state = state  # type: ignore[assignment]
    Handler.event = event  # type: ignore[assignment]
    Handler.result = result  # type: ignore[assignment]

    try:
        httpd = http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler)
    except OSError as exc:
        raise RuntimeError(
            f"Failed to bind to 127.0.0.1:{port}. Set BBB_AUTH_CALLBACK_PORT to a free port."
        ) from exc

    thread = threading.Thread(target=httpd.serve_forever, daemon=True)
    thread.start()
    return httpd, event, result


def _perform_login(auth_url: str, provider: str, port: int) -> dict:
    state = secrets.token_urlsafe(24)
    server, done_event, result = _start_callback_server(state, port)
    shutdown_lock = threading.Lock()
    shutdown_called = False

    def safe_shutdown() -> None:
        nonlocal shutdown_called
        with shutdown_lock:
            if shutdown_called:
                return
            shutdown_called = True
            server.shutdown()
            server.server_close()

    redirect_url = f"http://127.0.0.1:{port}/callback"
    callback_with_state = f"{redirect_url}?state={urllib.parse.quote(state, safe='')}"

    params = {
        "callback": callback_with_state,
        "state": state,
        "provider": provider,
        "auth_url": auth_url,
    }
    login_url = f"{LOGIN_PAGE_URL}?{urllib.parse.urlencode(params)}"
    _debug("Opening browser for Supabase login...")
    _debug(f"If your browser does not open, visit:\n  {login_url}\n")
    opened = webbrowser.open(login_url, new=2, autoraise=True)
    if not opened:
        _debug("Please copy the URL above into your browser.")

    if not done_event.wait(timeout=300):
        safe_shutdown()
        raise RuntimeError("Timed out waiting for authentication. Please try again.")

    safe_shutdown()

    if result.get("error"):
        raise RuntimeError(f"Authentication failed: {result['error']}")

    session = result.get("session")
    if not isinstance(session, dict):
        raise RuntimeError("Authentication failed: no session data received")

    session["fetched_at"] = time.time()
    _save_session(session)
    _debug(f"Saved Supabase session to {SESSION_FILE}")
    return session


def _refresh_session(auth_url: str, refresh_token: str) -> dict:
    token_url = f"{auth_url}/auth/v1/token?grant_type=refresh_token"
    response = _request_json(token_url, {"refresh_token": refresh_token})
    response["fetched_at"] = time.time()
    _save_session(response)
    _debug("Refreshed Supabase session using stored refresh token")
    return response


def ensure_token(*, force_login: bool = False) -> str:
    if force_login:
        session = _perform_login(DEFAULT_AUTH_URL, DEFAULT_PROVIDER, DEFAULT_CALLBACK_PORT)
        return session["access_token"]

    session = _load_session()
    if session and _is_token_valid(session):
        return session["access_token"]

    if session and session.get("refresh_token"):
        try:
            refreshed = _refresh_session(DEFAULT_AUTH_URL, session["refresh_token"])
            return refreshed["access_token"]
        except Exception as exc:  # noqa: BLE001
            _debug(f"Failed to refresh session: {exc!r}")
            if os.environ.get("BBB_AUTH_DEBUG"):
                import traceback

                _debug(traceback.format_exc())

    _debug("No valid Supabase session found. Starting login flow...")
    session = _perform_login(DEFAULT_AUTH_URL, DEFAULT_PROVIDER, DEFAULT_CALLBACK_PORT)
    return session["access_token"]


def logout() -> None:
    try:
        SESSION_FILE.unlink()
        _debug(f"Deleted session file at {SESSION_FILE}")
    except FileNotFoundError:
        _debug("No session file to delete")


def main() -> None:
    parser = argparse.ArgumentParser(description="Supabase CLI auth helper")
    sub = parser.add_subparsers(dest="command", required=True)

    ensure_parser = sub.add_parser("ensure", help="Ensure a valid access token exists")
    ensure_parser.add_argument(
        "--force-login",
        action="store_true",
        help="Ignore cached session and force a new login",
    )

    sub.add_parser("login", help="Force a browser login and cache the session")
    sub.add_parser("logout", help="Remove cached session data")
    sub.add_parser("session-path", help="Print the session file path")

    args = parser.parse_args()

    try:
        if args.command == "ensure":
            token = ensure_token(force_login=args.force_login)
            print(token, end="")
        elif args.command == "login":
            token = ensure_token(force_login=True)
            print(token, end="")
        elif args.command == "logout":
            logout()
        elif args.command == "session-path":
            print(SESSION_FILE)
    except KeyboardInterrupt:
        sys.exit(1)
    except Exception as exc:  # noqa: BLE001
        _debug(f"Error: {exc}")
        sys.exit(1)


if __name__ == "__main__":
    main()
