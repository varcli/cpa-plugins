package main

// 11115 prompt-overflow 数字提炼：上游 msg 里已经带了具体 token 数
// (field report 2026-10-09: "prompt is too long: 1130300 tokens \u003e
// 1048576 maximum")。把它提到用户可见文案里，用户一眼能看出超出多少。

import (
	"strings"
	"testing"
)

func TestPromptOverflowDetailExtractsCounts(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    []string
	}{
		{
			// 日志里的真实形态：> 被 JSON 转义成 \u003e
			name:    "escaped gt",
			payload: `{"code":11115,"msg":"prompt is too long: 1130300 tokens \u003e 1048576 maximum"}`,
			// 上游给的数字原样引用; 只有"超出量"加千分位
			want: []string{"1130300 tokens > 1048576 maximum", "超出 81,724"},
		},
		{
			name:    "raw gt",
			payload: `{"code":11115,"msg":"prompt is too long: 2000 tokens > 1000 maximum"}`,
			want:    []string{"2000", "1000", "1,000"},
		},
		{
			name:    "singular token word",
			payload: `prompt is too long: 5000 token > 4096 maximum`,
			want:    []string{"5000", "4096", "904"},
		},
	}
	for _, tc := range cases {
		got := promptOverflowDetail(tc.payload)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: promptOverflowDetail()=%q missing %q", tc.name, got, w)
			}
		}
	}
}

// 没有数字时不编造：裸 413 / 空体返回空串，调用方退回通用文案。
func TestPromptOverflowDetailAbsentOnBarePayload(t *testing.T) {
	for _, payload := range []string{"", "<html>413</html>", `{"code":11115,"msg":"prompt is too long"}`} {
		if got := promptOverflowDetail(payload); got != "" {
			t.Errorf("promptOverflowDetail(%q)=%q want empty", payload, got)
		}
	}
}

// 端到端：11115 文案里要能看到具体数字与超出量。
func TestTranslateChatUpstreamError11115ShowsCounts(t *testing.T) {
	payload := `{"code":11115,"msg":"prompt is too long: 1130300 tokens \u003e 1048576 maximum"}`
	msg := translateChatUpstreamErrorFull(400, payload, nil, nil).Error()
	for _, want := range []string{"提示词过长", "code 11115", "1130300", "1048576", "超出 81,724"} {
		if !strings.Contains(msg, want) {
			t.Errorf("11115 copy missing %q: %s", want, msg)
		}
	}
	// 裸 413 仍走通用文案，且不硬提 11115 码（既有约定）
	bare := translateChatUpstreamErrorFull(413, `<html></html>`, nil, nil).Error()
	if strings.Contains(bare, "11115") {
		t.Errorf("bare 413 must not claim code 11115: %s", bare)
	}
	if !strings.Contains(bare, "提示词过长") {
		t.Errorf("bare 413 copy lost: %s", bare)
	}
}

func TestFormatThousands(t *testing.T) {
	cases := map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000",
		81724: "81,724", 1130300: "1,130,300", -1500: "-1,500",
	}
	for in, want := range cases {
		if got := formatThousands(in); got != want {
			t.Errorf("formatThousands(%d)=%q want %q", in, got, want)
		}
	}
}
