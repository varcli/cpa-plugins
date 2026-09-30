package main

import (
	"os"
	"strings"
	"testing"
)

// v0.12.66: 「对齐设备绑定」按钮的接线与安全约束。
//
// 面板侧：按钮只在 device_match===false 且服务端有 bound_device_id 时出现，
// 且必须带 data-action="align"（否则 bindCardActions 不会派发，点了没反应）。
func TestPanelAlignButtonWiring(t *testing.T) {
	html := string(panelHTML)
	for _, want := range []string{
		`data-action="align"`,
		`else if(act==="align") alignDevice(idx, ev.currentTarget)`,
		`async function alignDevice(idx, btn)`,
		`api("/device/align"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("panel missing %q — 对齐按钮未接线", want)
		}
	}
	// 出现条件：必须同时要求不一致与服务端绑定值在场。
	if !strings.Contains(html, "a.device_match===false&&a.bound_device_id") {
		t.Errorf("对齐按钮的出现条件必须同时约束 device_match===false 与 bound_device_id")
	}
}

// 后端：bound_device_id 必须现取（CheckLogin），不接受调用方传入 ——
// 否则等于开放"任意改写凭证设备指纹"的接口。
func TestDeviceAlignTakesBoundValueFromServerNotCaller(t *testing.T) {
	// 源码级断言：handler 里出现 CheckLogin 且 body 结构体只解 auth_index。
	srcBytes, err := os.ReadFile("management.go")
	if err != nil {
		t.Fatalf("read management.go: %v", err)
	}
	src := string(srcBytes)
	start := strings.Index(src, "func handleDeviceAlign")
	if start < 0 {
		t.Fatal("handleDeviceAlign not found")
	}
	end := strings.Index(src[start:], "\n// ----")
	if end < 0 {
		end = len(src) - start
	}
	body := src[start : start+end]
	if !strings.Contains(body, "CheckLogin") {
		t.Errorf("handleDeviceAlign 必须用 CheckLogin 现取服务端绑定值")
	}
	// 请求体只允许 auth_index；不得解析 bound_device_id 之类的调用方输入。
	reqStruct := body[strings.Index(body, "var body struct"):]
	reqStruct = reqStruct[:strings.Index(reqStruct, "}")]
	if strings.Contains(reqStruct, "BoundDevice") || strings.Contains(reqStruct, "DeviceID") {
		t.Errorf("请求体不得接受调用方传入的设备绑定值: %s", reqStruct)
	}
	if !strings.Contains(reqStruct, "AuthIndex") {
		t.Errorf("请求体必须接受 auth_index: %s", reqStruct)
	}
	// 写入必须走 mergeAuthStorage（保留设备密钥对与 parity 字段）。
	if !strings.Contains(body, "mergeAuthStorage") {
		t.Errorf("写入必须走 mergeAuthStorage，避免抹掉 devicePublicKey/devicePrivateKey")
	}
	// 空绑定值必须被拒绝。
	if !strings.Contains(body, `bound == ""`) {
		t.Errorf("BoundDeviceID 为空时必须拒绝（无法对齐）")
	}
}
