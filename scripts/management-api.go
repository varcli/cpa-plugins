package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// 管理面单点交互工具。密钥不出现在命令行里: 管理密钥经 sec-run 隐式读取, 客户端密钥
// 需要时由本进程从管理面就地取得, 两者都只进请求头, 响应回显默认打码。
func main() {
	base := flag.String("base", "http://127.0.0.1:18317", "目标地址")
	path := flag.String("path", "/v0/management/plugins", "管理面路径")
	method := flag.String("method", http.MethodGet, "请求方法")
	body := flag.String("body", "", "请求体")
	auth := flag.String("auth", "management", "鉴权身份: management 用管理密钥, client 用就地取得的客户端密钥")
	tokenFile := flag.String("token-file", "", "管理密钥文件路径 (本地沙箱 per-run key), 设置时优先于 sec-run")
	bodyFile := flag.String("body-file", "", "请求体文件路径, 设置时优先于 -body")
	flag.Parse()
	if *bodyFile != "" {
		raw, errRead := os.ReadFile(*bodyFile)
		if errRead != nil {
			fmt.Fprintf(os.Stderr, "[-] 读取 body-file 失败: %v\n", errRead)
			os.Exit(2)
		}
		*body = string(raw)
	}

	managementToken := ""
	if *tokenFile != "" {
		raw, errRead := os.ReadFile(*tokenFile)
		if errRead != nil {
			fmt.Fprintf(os.Stderr, "[-] 读取 token-file 失败: %v\n", errRead)
			os.Exit(3)
		}
		managementToken = "Bearer " + strings.TrimSpace(string(raw))
	} else {
		token, err := secRun("CPA_TOKEN")
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] 未能隐式读取管理密钥: %v, 不发请求\n", err)
			os.Exit(3)
		}
		managementToken = token
	}

	headerValue := managementToken
	if strings.EqualFold(strings.TrimSpace(*auth), "client") {
		clientKey, err := fetchClientKey(*base, managementToken)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] 未能就地取得客户端密钥: %v, 不发请求\n", err)
			os.Exit(3)
		}
		if clientKey != "" {
			headerValue = "Bearer " + clientKey
		} else {
			// 实例没有配置任何客户端密钥时 /v1 不鉴权, 不带鉴权头即可。
			headerValue = ""
		}
	}

	endpoint := strings.TrimRight(*base, "/") + "/" + strings.TrimLeft(*path, "/")
	var payload io.Reader
	if *body != "" {
		payload = bytes.NewReader([]byte(*body))
	}

	req, err := http.NewRequest(strings.ToUpper(*method), endpoint, payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] 构造请求失败: %v\n", err)
		os.Exit(2)
	}
	if headerValue != "" {
		req.Header.Set("Authorization", headerValue)
	}
	if *body != "" {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] 请求失败: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	if v := resp.Header.Get("X-CPA-VERSION"); v != "" {
		fmt.Fprintf(os.Stderr, "[host] X-CPA-VERSION=%s\n", v)
	}
	fmt.Fprintf(os.Stderr, "[res] HTTP %d\n", resp.StatusCode)
	os.Stdout.Write(redactBody(raw, resp.Header.Get("Content-Type")))
	if len(raw) > 0 && raw[len(raw)-1] != '\n' {
		fmt.Println()
	}
	if resp.StatusCode >= 300 {
		os.Exit(1)
	}
}

// secRun 只经 sec-run 隐式读取密钥, 取值不落在命令行里。
func secRun(name string) (string, error) {
	out, err := exec.Command("sec-run", "printenv", name).Output()
	if err != nil {
		return "", fmt.Errorf("sec-run printenv %s 执行失败", name)
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "", fmt.Errorf("sec-run 没有给出 %s", name)
	}
	return value, nil
}

// fetchClientKey 从管理面就地取一个客户端密钥, 供直连 /v1 的探测使用。
// 密钥只在进程内流转: 回显走打码, 解析用原始响应, 任何错误信息都不带取值。
// 返回空串表示实例没有配置客户端密钥, 此时 /v1 不鉴权。
func fetchClientKey(base, managementToken string) (string, error) {
	endpoint := strings.TrimRight(base, "/") + "/v0/management/api-keys"
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", managementToken)

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("管理面 /v0/management/api-keys 返回 HTTP %d", resp.StatusCode)
	}

	var doc struct {
		Keys []string `json:"api-keys"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("解析 api-keys 响应失败: %w", err)
	}
	for _, key := range doc.Keys {
		if strings.TrimSpace(key) != "" {
			return key, nil
		}
	}
	return "", nil
}

// 管理面会原样回吐各类密钥与凭据, 终端输出是它们唯一会外泄的出口, 因此默认全部打码。
// 打码保留长度与前 4 字节哈希, 足以判断「是哪一条、换没换」, 又不足以还原。
var sensitiveKey = regexp.MustCompile(`(?i)^(api[_-]?keys?|keys?|id[_-]?tokens?|access[_-]?tokens?|refresh[_-]?tokens?|tokens?|secrets?|passwords?|credentials?|authorization|cookies?|private[_-]?key)$`)

func redactBody(raw []byte, contentType string) []byte {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw
	}
	if strings.Contains(contentType, "json") {
		if out, ok := redactJSON(raw); ok {
			return out
		}
	}
	return redactText(raw)
}

func redactJSON(raw []byte) ([]byte, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return nil, false
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(redactValue(doc)); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func redactValue(v any) any {
	switch typed := v.(type) {
	case map[string]any:
		for k, val := range typed {
			if sensitiveKey.MatchString(k) {
				typed[k] = maskValue(val)
			} else {
				typed[k] = redactValue(val)
			}
		}
		return typed
	case []any:
		for i := range typed {
			typed[i] = redactValue(typed[i])
		}
		return typed
	default:
		return v
	}
}

func maskValue(v any) any {
	switch typed := v.(type) {
	case string:
		return maskString(typed)
	case []any:
		for i := range typed {
			typed[i] = maskValue(typed[i])
		}
		return typed
	case map[string]any:
		for k := range typed {
			typed[k] = maskValue(typed[k])
		}
		return typed
	default:
		return v
	}
}

func maskString(s string) string {
	if s == "" {
		// 空值不是秘密, 保留原样是为了不掩盖「这个字段没配」这一事实。
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("<redacted len=%d sha256=%s>", len(s), hex.EncodeToString(sum[:4]))
}

var yamlLine = regexp.MustCompile(`^(\s*(?:- )?)([A-Za-z0-9_.-]+)(\s*:\s*)(.+)$`)
var yamlItem = regexp.MustCompile(`^(\s*-\s*)(\S.*)$`)

func redactText(raw []byte) []byte {
	lines := strings.Split(string(raw), "\n")
	sensitiveBlockIndent := -1
	for i, line := range lines {
		if m := yamlLine.FindStringSubmatch(line); m != nil {
			if sensitiveKey.MatchString(m[2]) {
				if strings.TrimSpace(m[4]) == "" {
					sensitiveBlockIndent = len(m[1])
				} else {
					lines[i] = m[1] + m[2] + m[3] + maskString(strings.TrimSpace(m[4]))
				}
			}
			continue
		}
		if m := yamlItem.FindStringSubmatch(line); m != nil && sensitiveBlockIndent >= 0 {
			if len(m[1]) > sensitiveBlockIndent {
				lines[i] = m[1] + maskString(strings.TrimSpace(m[2]))
			} else {
				sensitiveBlockIndent = -1
			}
		}
	}
	return []byte(strings.Join(lines, "\n"))
}
