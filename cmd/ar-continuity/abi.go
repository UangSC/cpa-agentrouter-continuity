package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

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

static const cliproxy_host_api* stored_host;

static void store_host_api(const cliproxy_host_api* host) {
	stored_host = host;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (stored_host == NULL || stored_host->call == NULL) {
		return 1;
	}
	return stored_host->call(stored_host->host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (stored_host != NULL && stored_host->free_buffer != NULL && ptr != NULL) {
		stored_host->free_buffer(ptr, len);
	}
}
*/
import "C"

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || plugin == nil || host.abi_version != 1 {
		return 1
	}
	C.store_host_api(host)
	plugin.abi_version = 1
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, size C.size_t, response *C.cliproxy_buffer) (rc C.int) {
	if response == nil {
		return 1
	}
	response.ptr = nil
	response.len = 0
	defer func() {
		if recover() != nil {
			raw, _ := json.Marshal(envelope{Error: &rpcError{Code: "plugin_panic", Message: "渠道处理发生内部错误", HTTPStatus: 500}})
			response.ptr = C.CBytes(raw)
			response.len = C.size_t(len(raw))
			rc = 0
		}
	}()
	var raw []byte
	if request != nil && size > 0 {
		if size > 128<<20 {
			return 1
		}
		raw = C.GoBytes(unsafe.Pointer(request), C.int(size))
	}
	result, err := handle(C.GoString(method), raw)
	e := envelope{OK: err == nil}
	if err != nil {
		e.Error = asRPCError(err)
	} else {
		e.Result, _ = json.Marshal(result)
	}
	out, _ := json.Marshal(e)
	response.ptr = C.CBytes(out)
	response.len = C.size_t(len(out))
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, size C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() { shutdown() }
func nativeHost(method string, value any, result any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	m := C.CString(method)
	defer C.free(unsafe.Pointer(m))
	p := C.CBytes(raw)
	defer C.free(p)
	var out C.cliproxy_buffer
	rc := C.call_host_api(m, (*C.uint8_t)(p), C.size_t(len(raw)), &out)
	if out.ptr == nil {
		return fmt.Errorf("host callback unavailable")
	}
	defer C.free_host_buffer(out.ptr, out.len)
	if out.len > 128<<20 {
		return fmt.Errorf("host callback response exceeds buffer limit")
	}
	var e envelope
	if err = json.Unmarshal(C.GoBytes(out.ptr, C.int(out.len)), &e); err != nil {
		return err
	}
	if !e.OK {
		if e.Error != nil {
			return e.Error
		}
		return fmt.Errorf("host callback failed")
	}
	if rc != 0 {
		return fmt.Errorf("host callback failed")
	}
	if result != nil {
		return json.Unmarshal(e.Result, result)
	}
	return nil
}
