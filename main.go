package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

static int invoke_host(cliproxy_host_api* h, char* m, void* p, size_t n, cliproxy_buffer* r) {
    if (!h || !h->call || !h->free_buffer) return 1;
    return ((int (*)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*))h->call)(h->host_ctx, m, p, n, r);
}
static void free_host(cliproxy_host_api* h, cliproxy_buffer r) {
    if (r.ptr && h && h->free_buffer) ((void (*)(void*, size_t))h->free_buffer)(r.ptr, r.len);
}

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"encoding/json"
	"errors"
	"unsafe"
)

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = C.uint32_t(pluginABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	if host != nil && host.abi_version == C.uint32_t(pluginABIVersion) {
		// Copy the ABI table; the host owns callback addresses for this plugin's lifetime.
		api := *host
		runtime.hostCall = func(method string, request any, result any) error {
			raw, err := json.Marshal(request)
			if err != nil {
				return errors.New("encode host callback")
			}
			name := C.CString(method)
			payload := C.CBytes(raw)
			defer C.free(unsafe.Pointer(name))
			defer C.free(payload)
			var response C.cliproxy_buffer
			code := C.invoke_host(&api, name, payload, C.size_t(len(raw)), &response)
			defer C.free_host(&api, response)
			if code != 0 || response.ptr == nil || response.len > 16<<20 {
				return errors.New("host callback unavailable")
			}
			var wrapped envelope
			if json.Unmarshal(C.GoBytes(response.ptr, C.int(response.len)), &wrapped) != nil || !wrapped.OK {
				return errors.New("host callback failed")
			}
			if json.Unmarshal(wrapped.Result, result) != nil {
				return errors.New("decode host callback")
			}
			return nil
		}
	}
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	if method == nil {
		_ = writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		const maxCGoBytes = C.size_t(1<<31 - 1)
		if requestLen > maxCGoBytes {
			_ = writeResponse(response, errorEnvelope("invalid_request", "request is too large"))
			return 1
		}
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		_ = writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	if !writeResponse(response, raw) {
		return 1
	}
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	runtime.shutdown()
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) bool {
	if response == nil || len(raw) == 0 {
		return false
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return false
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
	return true
}
