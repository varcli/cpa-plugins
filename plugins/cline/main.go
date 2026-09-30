// Package main implements the Cline / ClinePass CLIProxyAPI dynamic plugin.
//
// cline exposes Cline's own API as a cliproxy provider: it logs in through the
// WorkOS device flow Cline Desktop uses, registers the resulting WorkOS grant as
// a Cline token pair, keeps that pair fresh with a single-flight refresh plus a
// 401 retry, discovers the account's model catalog from
// /api/v1/ai/cline/recommended-models and merges it across accounts, executes
// chat completions (streaming and non-streaming), and serves a management panel
// for accounts and models.
//
// Protocol layer for the CPA dynamic plugin C ABI.
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
	"net/http"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/varcli/cpa-plugins/plugins/cline/clinerpc"
	"github.com/varcli/cpa-plugins/plugins/cline/provider"
)

var hostAPI *C.cliproxy_host_api

// pluginVersionLiteral is the single `Version: "x.y.z"` literal the release
// tooling rewrites (scripts/release.go globs plugins/<id>/*.go at the top level
// only, and requires exactly one match). It is declared here and pushed into the
// provider at init so `release.go version` can find and bump it.
var pluginVersionLiteral = struct {
	Version string
}{Version: "0.2.0"}

// pluginConfigFields is the plugin's declared configuration surface. It lives at
// the top level for the same reason as the version literal: scripts/dev-sandbox.go
// globs plugins/<id>/*.go at the top level only and asserts the host reports every
// field declared there, so a declaration inside a subpackage would be invisible to
// that check.
//
// The sandbox parser is textual, not syntactic: it looks for the literal
// ConfigFields marker token and then counts braces. Wrapping the slice in a
// struct keeps that token on a real assignment, so it cannot be silently lost by
// rewording a comment.
var pluginConfigFields = struct {
	ConfigFields []pluginapi.ConfigField
}{ConfigFields: []pluginapi.ConfigField{
	{Name: "model_prefix", Type: pluginapi.ConfigFieldTypeString,
		Description: "模型 ID 前缀, 用于与其他同 id 插件/原生 provider 共存 (默认 cline/)。"},
	{Name: "enable_model_prefix", Type: pluginapi.ConfigFieldTypeBoolean,
		Description: "是否在注册的模型 ID 上添加 model_prefix (默认 true)。关闭后本插件使用 Cline 原生模型 ID。"},
	{Name: "hidden_models", Type: pluginapi.ConfigFieldTypeString,
		Description: "隐藏的模型 ID 列表 (逗号分隔), 使用注册后的 ID (含 model_prefix); 以 * 结尾表示按前缀隐藏整个系列。"},
	{Name: "models", Type: pluginapi.ConfigFieldTypeString,
		Description: "额外附加到模型列表的 Cline 原生模型 ID (逗号分隔), 用于上游 feed 未列出但客户端可用的模型。"},
}}

func main() {}

// -----------------------------------------------------------------------------
// C ABI exports
// -----------------------------------------------------------------------------

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	hostAPI = host
	C.store_host_api(host)
	provider.SetVersion(pluginVersionLiteral.Version)
	provider.SetConfigFields(pluginConfigFields.ConfigFields)
	provider.SetHostCaller(callHost)
	plugin.abi_version = C.uint32_t(pluginabi.ABIVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	raw, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, raw)
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
	// No-op: the host calls this on its own exit path. Touching Go runtime state
	// here risks SIGSEGV in cgo after dlclose.
}

// -----------------------------------------------------------------------------
// Host calls
// -----------------------------------------------------------------------------

func callHost(method string, payload any) (json.RawMessage, error) {
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback %s: %w", method, errMarshal)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	var response C.cliproxy_buffer
	var requestPtr *C.uint8_t
	if len(rawPayload) > 0 {
		ptr := C.CBytes(rawPayload)
		if ptr == nil {
			return nil, fmt.Errorf("allocate host callback %s", method)
		}
		defer C.free(ptr)
		requestPtr = (*C.uint8_t)(ptr)
	}
	callCode := C.call_host_api(cMethod, requestPtr, C.size_t(len(rawPayload)), &response)
	var rawResponse []byte
	if response.ptr != nil && response.len > 0 {
		rawResponse = C.GoBytes(response.ptr, C.int(response.len))
	}
	if response.ptr != nil {
		C.free_host_buffer(response.ptr, response.len)
	}
	if len(rawResponse) == 0 {
		return nil, fmt.Errorf("host callback %s returned no response, code=%d", method, int(callCode))
	}
	var env struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if errJSON := json.Unmarshal(rawResponse, &env); errJSON != nil {
		return nil, fmt.Errorf("decode host callback %s: %w", method, errJSON)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	if callCode != 0 {
		return nil, fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

// -----------------------------------------------------------------------------
// Method dispatch
// -----------------------------------------------------------------------------

func handleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return provider.Register(request)

	case pluginabi.MethodModelStatic:
		return provider.StaticModels(request)

	case pluginabi.MethodModelForAuth:
		return provider.ModelsForAuth(request)

	case pluginabi.MethodAuthIdentifier, pluginabi.MethodExecutorIdentifier:
		return provider.Identifier()

	case pluginabi.MethodAuthParse:
		return provider.ParseAuth(request)

	case pluginabi.MethodAuthLoginStart:
		return provider.StartLogin(request)

	case pluginabi.MethodAuthLoginPoll:
		return provider.PollLogin(request)

	case pluginabi.MethodAuthRefresh:
		return provider.RefreshAuth(request)

	case pluginabi.MethodExecutorExecute:
		return provider.Execute(request)

	case pluginabi.MethodExecutorExecuteStream:
		return provider.ExecuteStream(request)

	case pluginabi.MethodExecutorCountTokens:
		return provider.CountTokens(request)

	case pluginabi.MethodExecutorHTTPRequest:
		return provider.ExecutorHTTPRequest(request)

	case pluginabi.MethodManagementRegister:
		var regReq pluginapi.ManagementRegistrationRequest
		if errUnmarshal := json.Unmarshal(request, &regReq); errUnmarshal == nil {
			provider.SetManagementBasePath(regReq.BasePath)
			provider.SetResourceBasePath(regReq.ResourceBasePath)
		}
		return provider.RegisterManagement()

	case pluginabi.MethodManagementHandle:
		return provider.HandleManagement(request)

	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func errorEnvelope(code, message string) []byte {
	return clinerpc.Error(code, message, false, http.StatusInternalServerError)
}
