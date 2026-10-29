package main

// identity_family_test.go — v0.9.79 pins for the generalized 11-128
// "client self-introduction + brand" content gate.
//
// Root-cause chain (field report 2026-10-27, model deepseek-v4.1-flash, CN
// realm): DSH's system prompt grew a second identity line — "You are a coding
// agent powered by the deepseek-v4.1-flash model." — restored upstream on
// 2026-10-16 (deepseek-ai/deepseek-harness d6adc5d9b9). The Tencent CN gate
// had generalized its 11-128 family into a verb table by then, and the
// reference reverse-proxy gateways (workbuddy2api-hub #533/#537/#539/#570/
// #574, single-variable replays against copilot.tencent.com) mapped the
// boundaries this file pins:
//
//   - assistant/system carrying an identity sentence → 400 / 11-128
//   - user carrying the SAME sentence → 200 (user data is not gated for
//     this family, and rewriting it desyncs the model from disk, #537)
//   - the sentence quoted once into assistant history poisons EVERY later
//     turn of that conversation (#539/#574: "聊到一半突然全挂")
//   - a pasted upstream error (the digits 11128 as text) is itself a
//     trigger — the poisoning loop users hit while debugging
//
// The role matrix below mirrors the #574 boundary table one-for-one.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSanitizeIdentitySentences_Matrix walks the verb family through
// sanitizeIdentitySentences (the identity-scoped pass). Every confirmed
// trigger sentence must collapse to the neutral replacement; every guarded
// shape must survive byte-identical.
func TestSanitizeIdentitySentences_Matrix(t *testing.T) {
	neutral := identitySelfIntroReplacement
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The field-report trigger: DSH persona line naming the exact model.
		{"persona line names model", "You are a coding agent powered by the deepseek-v4.1-flash model.", neutral},
		{"intro line (identity family form)", "You are an AI agent powered by DeepSeek Harness.", neutral},
		{"built on", "You are an AI assistant built on DeepSeek.", neutral},
		{"verb built by", "You are an agent built by Acme.", neutral},
		{"verb created by", "You are a helpful agent created by Acme Labs.", neutral},
		{"verb developed by", "You are the coding agent developed by Acme.", neutral},
		{"verb made by", "You are a model made by Acme.", neutral},
		{"verb trained by", "You are an assistant trained by Acme.", neutral},
		// Noun-less form: "You are powered by ..." (the family's bare shape).
		{"no noun", "You are powered by Acme.", neutral},
		// Product-noun compound with comma before the verb.
		{"product agent comma", "You are Qoder Agent, built by Qoder Inc.", neutral},
		// Case drift.
		{"case drift", "you are an ai agent POWERED BY deepseek harness.", neutral},
		// Boundaries: newline before, punctuation before, start of text.
		{"newline boundary", "Header line.\nYou are an agent powered by Acme.\nTail line.", "Header line.\n" + neutral + "\nTail line."},
		{"period boundary", "Intro sentence. You are an agent powered by Acme. Outro.", "Intro sentence. " + neutral + " Outro."},
		{"colon boundary", "Role: you are an agent powered by Acme.", "Role: " + neutral},
		// Terminator followers: the sentence period must consume and re-emit
		// its follower, and a version number's dot must NOT end the sentence.
		{"version dot not a terminator", "You are an agent powered by Acme v1.2 toolkit.", neutral},
		{"mid-text keeps surroundings", "Before. You are a coding agent powered by the deepseek-v4.1-flash model. After.", "Before. " + neutral + " After."},
		// Bounded-window guards (wb-hub's false-positive notes).
		{"four-word window misses", "You are free to use tools built by others.", "You are free to use tools built by others."},
		{"encouraged sentence", "You are encouraged to explore plugins made by the community.", "You are encouraged to explore plugins made by the community."},
		// Sentence-boundary guard (our precision divergence from wb-hub):
		// a mid-sentence "you are" clause is NOT an identity sentence.
		{"mid-sentence you-are clause", "The tools you are using were built by Acme.", "The tools you are using were built by Acme."},
		// Brand shrink follows the identity rewrite in the same blob.
		{"brand shrinks after rewrite", "You are an AI agent powered by DeepSeek Harness. See the DeepSeek Harness docs.", neutral + " See the DeepSeek docs."},
		// Brand shrink alone (no identity sentence in the blob).
		{"brand shrinks alone", "Built on the DeepSeek Harness runtime.", "Built on the DeepSeek runtime."},
		// Identifier-shaped brand occurrences stay byte-identical (#574).
		{"hyphen identifier untouched", "path: /opt/deepseek-harness/app", "path: /opt/deepseek-harness/app"},
		{"underscore identifier untouched", "pkg deepseek_harness v2", "pkg deepseek_harness v2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeIdentitySentences(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeIdentitySentences(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeIdentityRoleScope pins the #574 boundary matrix at the body
// level: the identity family is rewritten in system/developer/assistant and
// left byte-identical in user/tool.
func TestSanitizeIdentityRoleScope(t *testing.T) {
	trigger := "You are a coding agent powered by the deepseek-v4.1-flash model."
	body := map[string]any{
		"model": "deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "system", "content": "Preamble. " + trigger},
			map[string]any{"role": "user", "content": "hi"},
			map[string]any{"role": "assistant", "content": trigger},
			map[string]any{"role": "user", "content": "Quote check: " + trigger},
			map[string]any{"role": "tool", "content": "tool output mentions " + trigger},
		},
	}
	if !sanitizeIdentityInPlace(body) {
		t.Fatal("expected changes")
	}
	msgs := body["messages"].([]any)
	role := func(i int) string { return msgs[i].(map[string]any)["role"].(string) }
	content := func(i int) string { return msgs[i].(map[string]any)["content"].(string) }

	if !strings.Contains(content(0), identitySelfIntroReplacement) {
		t.Errorf("system content not rewritten: %q", content(0))
	}
	if !strings.Contains(content(2), identitySelfIntroReplacement) {
		t.Errorf("assistant content not rewritten: %q", content(2))
	}
	// The #574 boundary: the SAME sentence from a user message passes the
	// upstream gate (200) and is user data — it must survive untouched.
	if content(3) != "Quote check: "+trigger {
		t.Errorf("user content must stay byte-identical, got %q", content(3))
	}
	if content(4) != "tool output mentions "+trigger {
		t.Errorf("tool content must stay byte-identical, got %q", content(4))
	}
	_ = role
}

// TestSanitizeIdentityReasoningMirror: the step 3.5 reasoning fold happens
// AFTER the identity pass, so an identity sentence living in an assistant
// reasoning mirror must be scrubbed here or the fold re-arms it.
func TestSanitizeIdentityReasoningMirror(t *testing.T) {
	trigger := "You are an AI assistant built on DeepSeek."
	body := map[string]any{
		"model": "deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "assistant", "content": "working", "reasoning_content": trigger, "reasoning": "Context: " + trigger + " and quoted: \"" + trigger + "\""},
		},
	}
	if !sanitizeIdentityInPlace(body) {
		t.Fatal("expected changes")
	}
	m := body["messages"].([]any)[0].(map[string]any)
	if rc, _ := m["reasoning_content"].(string); strings.Contains(rc, "built on DeepSeek") {
		t.Errorf("reasoning_content not scrubbed: %q", rc)
	}
	if r, _ := m["reasoning"].(string); strings.Contains(r, "built on DeepSeek") {
		t.Errorf("reasoning not scrubbed: %q", r)
	}
	if r, _ := m["reasoning"].(string); !strings.Contains(r, "Context: "+identitySelfIntroReplacement) {
		t.Errorf("colon-boundary form must be rewritten in place, got %q", r)
	}
}

// TestSanitizeIdentityToolCallArguments: agents echo identity prose into
// tool-call argument strings; the identity pass scrubs those too.
func TestSanitizeIdentityToolCallArguments(t *testing.T) {
	trigger := "You are a coding agent powered by the deepseek-v4.1-flash model."
	body := map[string]any{
		"model": "deepseek-v4.1-flash",
		"messages": []any{
			map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"function": map[string]any{"name": "write", "arguments": `{"path":"a.txt","text":"` + trigger + `"}`}},
			}},
		},
	}
	if !sanitizeIdentityInPlace(body) {
		t.Fatal("expected changes")
	}
	args := body["messages"].([]any)[0].(map[string]any)["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)["arguments"].(string)
	if strings.Contains(strings.ToLower(args), "powered by") {
		t.Fatalf("tool_call arguments not scrubbed: %q", args)
	}
	var parsed struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("arguments must stay valid JSON, got %q: %v", args, err)
	}
	if parsed.Text != identitySelfIntroReplacement {
		t.Fatalf("scrubbed text = %q, want %q", parsed.Text, identitySelfIntroReplacement)
	}
}

// TestSanitizeBlockedTemplatesNewcomers pins the all-roles additions riding
// with v0.9.79: the pasted-error poisoning-loop breaker, the billing-header
// strip, the Codex attribution sentence standalone, and the OMO fingerprint.
func TestSanitizeBlockedTemplatesNewcomers(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The poisoning loop: a user pastes the upstream error back into the
		// agent; the bare digits are a trigger, the hyphenated form passes.
		{"pasted error digits", "HTTP 500 code 11128 unapproved channel", "HTTP 500 code 11-128 unapproved channel"},
		// v0.9.80: the gate trigger is the BARE code — every known paste
		// shape has the digits as their own token. 0.9.79's substring
		// replace also rewrote 11128 embedded inside longer digit runs,
		// silently corrupting logs/order numbers the model was asked to
		// read (probe: ts=1729111280 -> ts=172911-1280). These pin the
		// word-boundary behavior: embedded stays, bare rewrites.
		{"json code value", `{"code":11128,"msg":"x"}`, `{"code":11-128,"msg":"x"}`},
		{"cjk-flanked code", "错误码11128已复现", "错误码11-128已复现"},
		{"timestamp keeps digits", "ts=1729111280 pid=31128", "ts=1729111280 pid=31128"},
		{"order number keeps digits", "order 900111285 shipped", "order 900111285 shipped"},
		{"prefix-embedded keeps digits", "ref=311128 end", "ref=311128 end"},
		{"letter-flanked keeps digits", "hash x11128x tail", "hash x11128x tail"},
		// Claude Code's billing header, value shape and bare shape. The value
		// shape eats the trailing separator + spaces (wb-hub SANITIZE_HDR_RE
		// semantics — the next token lands flush).
		{"billing header value", "x-anthropic-billing-header: {\"tier\":\"pro\"}; rest", "rest"},
		{"billing header bare", "see x-anthropic-billing-header for details", "see x-anthropic-billing-hdr for details"},
		// Codex attribution sentence on its own (Codex Desktop shape).
		{"codex second sentence", "Preamble. Codex CLI is an open source project led by OpenAI. Rest.", "Preamble. The Codex CLI is an open source project led by OpenAI. Rest."},
		{"codex second sentence already the-prefixed", "The Codex CLI is an open source project led by OpenAI.", "The Codex CLI is an open source project led by OpenAI."},
		// OhMyOpenCode junior agent fingerprint.
		{"omo junior", "Sisyphus-Junior - Focused executor from OhMyOpenCode runs first.", "Sisyphus-Junior - Focused executor runs first."},
		// Claude Code feedback line spelling.
		{
			"feedback line",
			"To give feedback, users should report the issue at https://github.com/anthropics/claude-code/issues",
			"To provide feedback, users should report the issue at https://github.com/anthropics/claude-code/issues",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeBlockedTemplates(tc.in); got != tc.want {
				t.Fatalf("sanitizeBlockedTemplates(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestPrepareUpstreamBodyDSHCurrentPrompt is the end-to-end pin: a body
// shaped like the CURRENT DSH client (two identity lines, the web-surface
// runtime-context line, an assistant echo, and a user pasting the upstream
// error) leaves prepareUpstreamBody with zero trigger phrases in the
// identity-scoped roles, the hyphenated error spelling in the user paste,
// and the user's own quote of the identity sentence untouched.
func TestPrepareUpstreamBodyDSHCurrentPrompt(t *testing.T) {
	persona := "You are a coding agent powered by the deepseek-v4.1-flash model."
	intro := "You are an AI agent powered by DeepSeek Harness."
	webSurface := `You are interacting with the user through the DeepSeek Harness Web GUI whose URL the runtime context names. When the user refers to "this page", "this GUI", or "this app" without naming another target, they mean this GUI.`
	// v0.9.81: the CURRENT DSH system prompt also carries the
	// file-reference segment (injected by the official
	// @deepseek-ai/dsh-file-reference plugin) — the fourth 11-128
	// family member. The e2e body must exercise it end to end.
	fileRef := dshFileRefA + " " + dshFileRefB + dshFileRefTail
	body := map[string]any{
		"model":  "deepseek-v4.1-flash",
		"stream": false,
		"messages": []any{
			map[string]any{"role": "system", "content": intro + "\n\n" + persona + "\n\n" + webSurface + "\n\n" + fileRef + "\n\nUse the read tool."},
			map[string]any{"role": "user", "content": "debug this failure please"},
			map[string]any{"role": "assistant", "content": "The error was: "},
			map[string]any{"role": "user", "content": "HTTP 500 {\"code\":11128,\"msg\":\"Illegal API invocation from an unapproved channel\"} — and for the record: " + persona},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := prepareUpstreamBody(raw, nil, nil, "deepseek-v4.1-flash")

	var obj struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Stream bool `json:"stream"`
	}
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out)
	}
	if !obj.Stream {
		t.Error("forceStream lost")
	}
	if len(obj.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(obj.Messages))
	}

	sys := strings.ToLower(obj.Messages[0].Content)
	for _, banned := range []string{
		"powered by deepseek harness",
		"powered by the deepseek-v4.1-flash model",
		"deepseek harness web gui",
		"deepseek harness web interface", // brand must shrink further
		"deepseek harness",               // no prose brand left in system at all
		// v0.9.81: the file-reference segment must leave with the
		// "that" inserted — the raw contiguous form is the trigger.
		"are paths the user explicitly referenced",
	} {
		if strings.Contains(sys, banned) {
			t.Errorf("system content still carries trigger %q:\n%s", banned, obj.Messages[0].Content)
		}
	}
	// v0.9.81: the "that"-inserted A sentence and the verbatim B+tail.
	if !strings.Contains(obj.Messages[0].Content, dshFileRefAFixed) {
		t.Errorf("file-reference A sentence should carry the inserted that, got:\n%s", obj.Messages[0].Content)
	}
	if !strings.Contains(obj.Messages[0].Content, dshFileRefB+dshFileRefTail) {
		t.Errorf("file-reference B sentence and tail must survive verbatim, got:\n%s", obj.Messages[0].Content)
	}
	if !strings.Contains(obj.Messages[0].Content, "DeepSeek web interface whose URL the runtime context names") {
		t.Errorf("web-surface line should read the shrunk-brand form, got:\n%s", obj.Messages[0].Content)
	}
	if !strings.Contains(obj.Messages[0].Content, identitySelfIntroReplacement) {
		t.Errorf("persona line should be the neutral sentence, got:\n%s", obj.Messages[0].Content)
	}
	if !strings.Contains(obj.Messages[0].Content, "Use the read tool.") {
		t.Errorf("unrelated guidance must survive, got:\n%s", obj.Messages[0].Content)
	}

	// User paste: digits hyphenated, quote of the identity sentence intact
	// (the #574 user-role boundary), everything else as typed.
	user2 := obj.Messages[3].Content
	if strings.Contains(user2, "11128") {
		t.Errorf("pasted error digits must be hyphenated, got %q", user2)
	}
	if !strings.Contains(user2, "11-128") {
		t.Errorf("hyphenated code missing from user paste: %q", user2)
	}
	if !strings.Contains(user2, persona) {
		t.Errorf("user-role quote of the identity sentence must survive, got %q", user2)
	}
}

// v0.9.81: the DSH file-reference segment — the fourth confirmed member of
// the 11-128 content family (wb2api-hub #675/#677/#680, fixes #681/#683/
// #684). The official @deepseek-ai/dsh-file-reference plugin injects
// FILE_REFERENCE_PROMPT into the system prompt; the segment (each sentence
// alone 200, contiguous 400) is the gate's match unit, and inserting one
// "that" into sentence A breaks it while preserving the meaning verbatim.
const (
	dshFileRefA      = "Tokens prefixed with @ are paths the user explicitly referenced."
	dshFileRefAFixed = "Tokens prefixed with @ are paths that the user explicitly referenced."
	dshFileRefB      = "Relative paths resolve from the workspace root; absolute paths identify files or directories on the host."
	dshFileRefTail   = " A trailing slash marks a directory: list it when its contents matter. Anything else is a file: use the read tool when its contents are needed, and do not claim to have inspected it before reading. @\"...\" quotes a path containing spaces."
)

func TestSanitizeDshFileRefParagraph(t *testing.T) {
	para := dshFileRefA + " " + dshFileRefB + dshFileRefTail
	want := dshFileRefAFixed + " " + dshFileRefB + dshFileRefTail

	// The full paragraph: sentence A gets exactly one "that", B and the
	// tail survive byte-for-byte.
	if got := sanitizeIdentitySentences(para); got != want {
		t.Fatalf("full paragraph:\n got  %q\n want %q", got, want)
	}
	// Idempotent: the fixed sentence can never match again ("paths that
	// the user" is not "paths the user").
	once := sanitizeIdentitySentences(para)
	if again := sanitizeIdentitySentences(once); again != once {
		t.Fatalf("not idempotent:\n once  %q\n again %q", once, again)
	}
	// Sentence A alone rewrites the same way — DSH may reword the tail
	// without the rule caring.
	if got := sanitizeIdentitySentences(dshFileRefA); got != dshFileRefAFixed {
		t.Fatalf("sentence A alone: got %q", got)
	}
	// Case-insensitive in, original case preserved out (capture groups;
	// only the inserted "that" is literal).
	if got := sanitizeIdentitySentences(strings.ToLower(para)); got != strings.ToLower(want) {
		t.Fatalf("lowercase paragraph:\n got  %q\n want %q", got, strings.ToLower(want))
	}
	// Embedded with surrounding prompt text.
	embedded := "You are a coding agent.\n\n" + para + "\n\nMore rules follow."
	wantEmbedded := "You are a coding agent.\n\n" + want + "\n\nMore rules follow."
	if got := sanitizeIdentitySentences(embedded); got != wantEmbedded {
		t.Fatalf("embedded:\n got  %q\n want %q", got, wantEmbedded)
	}
	// Unrelated @ text is not touched: e-mail, npm package, Windows path.
	for _, s := range []string{
		"mail me at foo@bar.com",
		"npm i @deepseek-ai/dsh",
		`see C:\work\@cache for details`,
	} {
		if got := sanitizeIdentitySentences(s); got != s {
			t.Errorf("unrelated @ text rewritten: %q -> %q", s, got)
		}
	}
}

func TestSanitizeDshFileRefRoleScope(t *testing.T) {
	para := dshFileRefA + " " + dshFileRefB
	fixed := dshFileRefAFixed + " " + dshFileRefB
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": para},
			map[string]any{"role": "developer", "content": para},
			map[string]any{"role": "assistant", "content": para},
			map[string]any{"role": "user", "content": para},
			map[string]any{"role": "tool", "content": para},
		},
	}
	if !sanitizeIdentityInPlace(body) {
		t.Fatal("expected changes")
	}
	msgs := body["messages"].([]any)
	for i, want := range []string{fixed, fixed, fixed, para, para} {
		role := msgs[i].(map[string]any)["role"].(string)
		if got := msgs[i].(map[string]any)["content"].(string); got != want {
			t.Errorf("role %s:\n got  %q\n want %q", role, got, want)
		}
	}
}

// v0.9.81: Claude Code's cc_* key-value metadata pairs strip in every role
// (reference SANITIZE_KV_RE, same fingerprint family as the billing header).
func TestSanitizeCCKeyValuePairs(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"entrypoint pair", "meta cc_entrypoint=cli; rest", "meta rest"},
		// No semicolon: the value class [^;\r\n]* runs to end of line —
		// the reference's exact SANITIZE_KV_RE semantics (metadata comes
		// in "k=v; k2=v2" blocks, so the value boundary is the ; or EOL).
		{"pair without semicolon eats to eol", "cc_version=1.2.3 tail", ""},
		{"uppercase pair", "CC_MODE=PROD; next", "next"},
		{"nested pair fixpoint", "cc_a=cc_b=x; tail", "tail"},
		{"multiple pairs", "cc_a=1; cc_b=2; tail", "tail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeBlockedTemplates(tc.in); got != tc.want {
				t.Fatalf("sanitizeBlockedTemplates(%q)\n got  %q\n want %q", tc.in, got, tc.want)
			}
		})
	}
	// No word boundary before cc_ (identifier hosts) — untouched.
	for _, s := range []string{
		"foo_cc_x=1 stays",
		"gcc_=nope also stays",
		"the word occur here: successful",
	} {
		if got := sanitizeBlockedTemplates(s); got != s {
			t.Errorf("boundary violation: %q -> %q", s, got)
		}
	}
	// The strip reaches user messages too (all-roles family).
	body := map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "my config says cc_entrypoint=vscode; help"},
		},
	}
	sanitizeHistoryPhrasesInPlace(body)
	if got := body["messages"].([]any)[0].(map[string]any)["content"].(string); got != "my config says help" {
		t.Errorf("user-role cc_ pair not stripped: %q", got)
	}
}
