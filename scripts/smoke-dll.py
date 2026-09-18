"""Exercise the real Windows DLL ABI with synthetic host auth and state only."""
import base64
import ctypes as c
import json
from pathlib import Path
import struct
import sys
import time


class Buffer(c.Structure):
    _fields_ = [("ptr", c.c_void_p), ("length", c.c_size_t)]


HostCall = c.CFUNCTYPE(c.c_int, c.c_void_p, c.c_char_p, c.c_void_p, c.c_size_t, c.POINTER(Buffer))
Free = c.CFUNCTYPE(None, c.c_void_p, c.c_size_t)
PluginCall = c.CFUNCTYPE(c.c_int, c.c_char_p, c.c_void_p, c.c_size_t, c.POINTER(Buffer))
Shutdown = c.CFUNCTYPE(None)


class Host(c.Structure):
    _fields_ = [("version", c.c_uint32), ("ctx", c.c_void_p), ("call", HostCall), ("free", Free)]


class Plugin(c.Structure):
    _fields_ = [("version", c.c_uint32), ("call", PluginCall), ("free", Free), ("shutdown", Shutdown)]


allocations = {}
callbacks = []


@HostCall
def host_call(ctx, method, payload, size, response):
    method = method.decode()
    callbacks.append(method)
    if method == "host.auth.list":
        result = {"files": [{"id": "synthetic-auth", "auth_index": "synthetic-index", "provider": "codex"}]}
    elif method == "host.auth.get":
        assert json.loads(c.string_at(payload, size))["auth_index"] == "synthetic-index"
        result = {"json": {"type": "codex", "access_token": "synthetic-token", "account_id": "synthetic-account"}}
    else:
        return 1
    raw = json.dumps({"ok": True, "result": result}).encode()
    buf = c.create_string_buffer(raw)
    address = c.addressof(buf)
    allocations[address] = buf
    response[0] = Buffer(address, len(raw))
    return 0


@Free
def host_free(ptr, length):
    allocations.pop(ptr, None)


def main():
    path = Path(sys.argv[1] if len(sys.argv) > 1 else "dist/windows-amd64/cpa-codex-turn-state.dll").resolve()
    dll = c.CDLL(str(path))
    dll.cliproxy_plugin_init.argtypes = [c.POINTER(Host), c.POINTER(Plugin)]
    dll.cliproxy_plugin_init.restype = c.c_int
    host = Host(1, None, host_call, host_free)
    plugin = Plugin()
    assert dll.cliproxy_plugin_init(c.byref(host), c.byref(plugin)) == 0

    def call(method, request):
        raw = json.dumps(request).encode()
        payload = c.create_string_buffer(raw)
        response = Buffer()
        code = plugin.call(method.encode(), payload, len(raw), c.byref(response))
        try:
            result = json.loads(c.string_at(response.ptr, response.length))
        finally:
            plugin.free(response.ptr, response.length)
        assert code == 0 and result["ok"], (method, result.get("error"))
        return result["result"]

    config = """enabled: true
defaults:
  accepted_blocks: [10, 12]
probe:
  enabled: true
  background_refresh: false
  timeout_seconds: 1
  proxy_pool:
    - url: http://127.0.0.1:1
"""
    registration = call("plugin.register", {"schema_version": 4, "config_yaml": base64.b64encode(config.encode()).decode()})
    assert registration["metadata"]["Version"] == "0.3.0"
    req = {"RequestID": "smoke", "ToFormat": "codex", "Model": "model-a", "Metadata": {"selected_auth_id": "synthetic-auth"}}
    call("request.intercept_after", req)
    assert callbacks == ["host.auth.list", "host.auth.get"]
    assert not allocations, "host-owned buffers leaked"
    token = base64.urlsafe_b64encode(b"\x80" + struct.pack("!Q", int(time.time())) + bytes(16 + 160 + 32)).decode()
    call("response.intercept_after", {"RequestID": "smoke", "ResponseHeaders": {"X-Codex-Turn-State": [token]}})
    call("request.complete", {"RequestID": "smoke", "Outcome": "succeeded"})
    result = call("request.intercept_after", req)
    assert result["Headers"]["X-Codex-Turn-State"] == [token]
    req["Model"] = "model-b"
    result = call("request.intercept_after", req)
    assert token not in json.dumps(result), "cross-model injection"
    status = call("management.handle", {"Method": "GET", "Path": "/codex-turn-state/status"})
    body = base64.b64decode(status["Body"])
    assert token.encode() not in body and b"synthetic-token" not in body
    assert json.loads(body)["version"] == "0.3.0"
    # A distinct model with an old synthetic seed must refresh from a background
    # C-to-host callback, without an intercept/request to trigger it.
    old = base64.urlsafe_b64encode(b"\x80" + struct.pack("!Q", int(time.time()) - 56 * 60) + bytes(16 + 160 + 32)).decode()
    background_config = config.replace("background_refresh: false", "background_refresh: true")
    background_config += "\ncredentials:\n  synthetic-auth:\n    plan: plus\n    state_model: background-model\n    state: " + old + "\n"
    call("plugin.reconfigure", {"schema_version": 4, "config_yaml": base64.b64encode(background_config.encode()).decode()})
    deadline = time.monotonic() + 4
    while True:
        status = call("management.handle", {"Method": "GET", "Path": "/codex-turn-state/status"})
        entries = json.loads(base64.b64decode(status["Body"]))["entries"]
        if any(e["model"] == "background-model" and e["last_probe"] == "network_error" and e["last_probe_reason"] == "before_expiry" for e in entries):
            break
        assert time.monotonic() < deadline, "no background refresh through native host callbacks"
        time.sleep(0.02)
    call("plugin.quiesce", {})
    plugin.shutdown()
    assert not allocations, "background callback buffer leak"
    print("DLL ABI smoke passed: callbacks, injection, isolation, proactive refresh, quiesce")


if __name__ == "__main__":
    main()
