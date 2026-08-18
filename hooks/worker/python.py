#!/usr/bin/env python3
"""The Python hooks worker.

vertrag is a Go program and cannot load a Python hook file into itself, so
this worker is the bridge: it runs the user's hook files in a real Python
interpreter and speaks vertrag's hook protocol over a socket. The protocol is
the same one the Node worker speaks — newline-delimited JSON objects of
{event, uuid, data} in, {uuid, data} out — so the Go side does not care which
language is on the other end.

Standard library only, deliberately. A tester should not have to create a
virtualenv or install a package to write a hook, and a released vertrag binary
carries this file inside it, so there is nothing to get out of step.

    python3 python.py --port <port> <hookfile>...
"""

import fnmatch
import json
import os
import socket
import sys
import traceback

# ---------------------------------------------------------------------------
# The hook API a hook file sees.
#
# The decorator style is the obvious Python spelling of the same idea, and
# will look familiar to anyone who has written hooks for a similar tool. The
# NAME is vertrag_hooks and nothing else: a module named after a different,
# archived project would be a puzzle for every reader of a vertrag hook file,
# and there are no Python hook files in the world to keep working.
# ---------------------------------------------------------------------------

_named = {"before": [], "after": [], "before_validation": []}
_each = {
    "before_all": [],
    "after_all": [],
    "before_each": [],
    "after_each": [],
    "before_each_validation": [],
}

#: Shared state between hooks. A hook that reads a value out of one response
#: and needs it in a later request has to put it somewhere; module-level
#: globals in the hook file work too, but a named place people can find beats
#: everyone inventing their own.
stash = {}


def _add_named(kind):
    def decorator(pattern):
        # A bare @hooks.before would be a mistake worth catching: the named
        # hooks take a transaction name, and decorating without one silently
        # registers a hook that matches nothing.
        if callable(pattern):
            raise TypeError(
                "@hooks.%s takes a transaction name, operationId, or glob: "
                "@hooks.%s('/things > List > 200'). For every transaction, "
                "use @hooks.%s_each." % (kind, kind, kind.replace("before_", "before").replace("after", "after"))
            )

        def register(function):
            _named[kind].append((pattern, function))
            return function

        return register

    return decorator


def _add_each(kind):
    def register(function):
        _each[kind].append(function)
        return function

    return register


before = _add_named("before")
after = _add_named("after")
before_validation = _add_named("before_validation")

before_all = _add_each("before_all")
after_all = _add_each("after_all")
before_each = _add_each("before_each")
after_each = _add_each("after_each")
before_each_validation = _add_each("before_each_validation")


def log(message):
    """Write a line to vertrag's error stream, where it interleaves with the
    run's own output rather than racing the report on stdout."""
    sys.stderr.write("hook: %s\n" % (message,))
    sys.stderr.flush()


def get_json(transaction, which="request"):
    """Return a request or response body parsed as JSON.

    The body travels as a string, because that is what goes on the wire and a
    hook editing bytes must be able to. Parsing it silently on the way in and
    re-serialising on the way out would reorder keys and reformat payloads
    that were being compared byte for byte, so the parse is explicit and
    round-tripping is the caller's choice.
    """
    body = transaction.get(which, {}).get("body") or ""
    if not body.strip():
        return {}
    try:
        return json.loads(body)
    except ValueError:
        return {}


def set_json(transaction, value, which="request"):
    """Serialise a value back into a request or response body.

    Compact separators, because Python's default pads with spaces where
    JavaScript's JSON.stringify does not. The same hook written in the two
    languages must put the same bytes on the wire: this is a contract tester,
    bodies are compared byte for byte, and a body that differs by whitespace
    depending on which worker ran it would be a difference nobody could see
    and everybody would have to explain.
    """
    transaction.setdefault(which, {})["body"] = json.dumps(value, separators=(",", ":"))


def matches(pattern, transaction):
    """Report whether a named hook's pattern selects this transaction.

    Three spellings, in the order a reader would try them: the transaction's
    full name, its operationId, and a glob over either. The operationId is
    there because a generated name — "/api/intent > Intent > 200 >
    application/json" — is long, and changes whenever someone edits the
    summary, while an operationId is stable.
    """
    name = transaction.get("name") or ""
    operation = transaction.get("operationId") or ""
    if pattern == name or (operation and pattern == operation):
        return True
    if any(character in pattern for character in "*?["):
        return fnmatch.fnmatchcase(name, pattern) or (
            bool(operation) and fnmatch.fnmatchcase(operation, pattern)
        )
    return False


def _run(functions, argument):
    for function in functions:
        function(argument)


def _run_named(kind, transaction):
    for pattern, function in _named[kind]:
        if matches(pattern, transaction):
            function(transaction)


def handle(event, data):
    """Dispatch one event to the hooks registered for it."""
    if event in ("beforeAll", "afterAll"):
        _run(_each["before_all" if event == "beforeAll" else "after_all"], data)
        return data

    if event == "beforeEach":
        _run(_each["before_each"], data)
        _run_named("before", data)
        return data

    if event == "beforeEachValidation":
        _run(_each["before_each_validation"], data)
        _run_named("before_validation", data)
        return data

    if event == "afterEach":
        # Named hooks run before the "each" ones on the way out, mirroring the
        # way they run after them on the way in.
        _run_named("after", data)
        _run(_each["after_each"], data)
        return data

    return data


# ---------------------------------------------------------------------------
# Loading hook files, and the module they import.
# ---------------------------------------------------------------------------


def install_module():
    """Make this module importable as `vertrag_hooks`.

    A hook file says `import vertrag_hooks as hooks`, and nothing on disk
    provides that, so it is installed into the module table before the hook
    files load.
    """
    sys.modules["vertrag_hooks"] = sys.modules[__name__]


def load(paths):
    for path in paths:
        absolute = os.path.abspath(path)
        directory = os.path.dirname(absolute)
        # The hook file's own directory goes on the path, so a hook file can
        # import its project's modules the way a script beside them would.
        if directory not in sys.path:
            sys.path.insert(0, directory)

        source = open(absolute, "r", encoding="utf-8").read()
        code = compile(source, absolute, "exec")
        # Executed in a namespace of its own, named for the file, so two hook
        # files defining the same function name do not collide.
        namespace = {"__name__": os.path.splitext(os.path.basename(absolute))[0], "__file__": absolute}
        exec(code, namespace)


# ---------------------------------------------------------------------------
# The socket protocol.
# ---------------------------------------------------------------------------


def serve(port):
    listener = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    listener.bind(("127.0.0.1", port))
    listener.listen(1)

    # vertrag waits for this line before connecting.
    sys.stdout.write("vertrag-hooks-ready\n")
    sys.stdout.flush()

    while True:
        connection, _ = listener.accept()
        try:
            handle_connection(connection)
        finally:
            connection.close()


def handle_connection(connection):
    buffer = b""
    while True:
        chunk = connection.recv(65536)
        if not chunk:
            return
        buffer += chunk

        # Messages are newline-delimited; the trailing fragment is whatever
        # has not arrived in full yet.
        while b"\n" in buffer:
            line, buffer = buffer.split(b"\n", 1)
            if not line.strip():
                continue
            reply(connection, line)


def reply(connection, line):
    try:
        message = json.loads(line.decode("utf-8"))
    except ValueError:
        return

    uuid = message.get("uuid")
    data = message.get("data")
    try:
        data = handle(message.get("event"), data)
        response = {"uuid": uuid, "data": data}
    except Exception as error:  # noqa: BLE001 — a hook may raise anything
        # A throwing hook fails its transaction rather than the run: one bad
        # hook should not stop the rest of the suite from reporting. The
        # traceback goes to stderr, where a person can read it, while the
        # short form travels back as the failure's reason.
        traceback.print_exc(file=sys.stderr)
        sys.stderr.flush()
        response = {"uuid": uuid, "data": message.get("data"), "error": "%s: %s" % (type(error).__name__, error)}

    connection.sendall((json.dumps(response) + "\n").encode("utf-8"))


def main(argv):
    port = 61321
    hookfiles = []

    index = 0
    while index < len(argv):
        if argv[index] == "--port":
            index += 1
            port = int(argv[index])
        else:
            hookfiles.append(argv[index])
        index += 1

    install_module()
    load(hookfiles)
    serve(port)


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except KeyboardInterrupt:
        pass
    except Exception:  # noqa: BLE001 — report and exit non-zero
        traceback.print_exc(file=sys.stderr)
        sys.exit(1)
