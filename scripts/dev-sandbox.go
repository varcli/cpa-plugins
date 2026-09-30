package main

// 插件本地沙箱: scripts/dev-sandbox.go
//
// 存在的理由: 验证一个插件能否被宿主正确装载, 此前需要手工做七件事 —— 编译动态库、
// 按平台放置、生成 config.yaml、填管理密钥、启动宿主、轮询日志、逐个 curl 断言。
// 本脚本把它压成一条命令, 且断言与退出码可被 CI 或人工直接复用。
//
// 用法:
//
//	go run scripts/dev-sandbox.go --plugin workbuddy [--plugin trae] [--checks load,models]
//	                              [--host <二进制>] [--host-src <源码目录>]
//	                              [--port 18317] [--profile desktop] [--keep]
//
// --plugin 可重复或逗号分隔, 多插件共用一个宿主进程与沙箱目录 (默认目录名为 id 以 + 连接)。
// --checks 选择要跑的断言子集, `--checks list` 打印清单; 断言都是离线的, 默认全跑。
//
// 沙箱不需要真实凭据: 装载、注册、模型清单三条断言都是离线的。凭据相关的验证
// (扫码登录、真实对话、真实额度) 不在此脚本范围内。
//
// 宿主二进制解析顺序: --host > $CPA_HOST_BIN > 缓存 > --host-src / $CPA_HOST_SRC 现场构建。
// 缓存路径: ~/.cache/cpa-plugins/host/v<SDK版本>/cliproxyapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

var reGoModSDK = regexp.MustCompile(`github\.com/router-for-me/CLIProxyAPI/v[0-9]+\s+v([0-9][^\s]*)`)

// check 是断言的注册单位: 每个断言对单个插件各跑一次, 返回失败即终止。
// 加断言 = 写一个 run 函数 + 注册一行, 调度不随断言数量增长。
type check struct {
	id   string
	desc string
	run  func(*sandbox, string) error
}

// checkRegistry 是断言清单的唯一权威来源: --checks list、解析、调度全部由它派生。
var checkRegistry = []check{
	{"load", "插件已装载并注册 (宿主日志 plugin loaded / plugin registered)", checkLoad},
	{"models", "/v1/models 覆盖静态清单声明的全部模型", checkModels},
	{"resource", "插件声明的 resource 页面可被宿主服务", checkResource},
	{"menus", "管理面 plugins 列表暴露插件菜单", checkMenus},
	{"config", "插件声明了 ConfigFields 时管理面必须返回", checkConfig},
	{"quota", "插件声明了 QuotaProvider 时额度列表必须含它", checkQuota},
}

var defaultChecks = []string{"load", "models", "resource", "menus", "config", "quota"}

// listFlag 支持 `-x a,b` 与 `-x a -x b` 两种写法, 多项参数是验收子集选择的载体。
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }

func (l *listFlag) Set(value string) error {
	for _, part := range strings.Split(value, ",") {
		if s := strings.TrimSpace(part); s != "" {
			*l = append(*l, s)
		}
	}
	return nil
}

func printCheckCatalog() {
	fmt.Println("可用断言 (按需点名, 不必全跑):")
	for _, c := range checkRegistry {
		fmt.Printf("  %-10s %s\n", c.id, c.desc)
	}
	fmt.Printf("  %-10s 以上全部 (默认)\n", "all")
}

func lookupCheck(id string) (check, bool) {
	for _, c := range checkRegistry {
		if c.id == id {
			return c, true
		}
	}
	return check{}, false
}

// resolveChecks 把用户输入折成有序执行集 (顺序即注册顺序), 并回传无法识别的名字。
func resolveChecks(input []string) ([]check, []string) {
	if len(input) == 0 {
		input = defaultChecks
	}
	want := map[string]bool{}
	var unknown []string
	for _, raw := range input {
		id := strings.ToLower(strings.TrimSpace(raw))
		if id == "" {
			continue
		}
		if id == "all" {
			for _, c := range checkRegistry {
				want[c.id] = true
			}
			continue
		}
		if _, ok := lookupCheck(id); !ok {
			unknown = append(unknown, id)
			continue
		}
		want[id] = true
	}

	ordered := make([]check, 0, len(want))
	for _, c := range checkRegistry {
		if want[c.id] {
			ordered = append(ordered, c)
		}
	}
	return ordered, unknown
}

type sandbox struct {
	pluginIDs  []string
	pluginDirs map[string]string
	hostBinary string
	hostSource string
	port       int
	profile    string
	timeout    time.Duration
	keep       bool
	chosen     []check

	// sandboxDir 为空时由 pluginIDs 推导; 显式传 --dir 时原样使用
	sandboxDir string

	// modelIDs 是本次运行 /v1/models 的快照, 供各插件的模型断言复用
	modelIDs map[string]bool

	// pluginEntries 是本次运行 /v0/management/plugins 的快照, 供能力与菜单断言复用
	pluginEntries map[string]pluginEntry

	secret     string
	configPath string
	logPath    string
	keyPath    string

	process *exec.Cmd
	logFile *os.File

	// exited 在宿主进程退出后关闭, exitErr 保存 Wait 的结果。
	// 只 Wait 一次: 退出检测与 stop 共用这个通道, 重复 Wait 会误判。
	exited  chan struct{}
	exitErr error
}

func main() {
	s := &sandbox{pluginDirs: map[string]string{}}
	var plugins listFlag
	var checks listFlag
	flag.Var(&plugins, "plugin", "插件 id (plugins/ 下的目录名), 逗号分隔或重复传参")
	flag.StringVar(&s.hostBinary, "host", "", "宿主二进制路径")
	flag.StringVar(&s.hostSource, "host-src", os.Getenv("CPA_HOST_SRC"), "CLIProxyAPI 源码目录, 用于现场构建宿主")
	flag.StringVar(&s.sandboxDir, "dir", "", "沙箱目录, 默认 ~/.cache/cpa-plugins/sandbox/<id>")
	flag.StringVar(&s.profile, "profile", "", "写入插件的 identity-profile/login-profile, 默认不写")
	flag.IntVar(&s.port, "port", 18317, "宿主监听端口")
	flag.DurationVar(&s.timeout, "timeout", 30*time.Second, "等待宿主就绪与断言的总超时")
	flag.BoolVar(&s.keep, "keep", false, "结束后保留宿主机进程与沙箱目录")
	flag.Var(&checks, "checks", "要跑的断言, 逗号分隔或重复传参; --checks list 打印清单")
	flag.Parse()

	if len(checks) == 1 && strings.EqualFold(strings.TrimSpace(checks[0]), "list") {
		printCheckCatalog()
		return
	}

	// 去重保持顺序, 同一个插件重复传参不重复构建
	seen := map[string]bool{}
	for _, id := range plugins {
		if !seen[id] {
			seen[id] = true
			s.pluginIDs = append(s.pluginIDs, id)
		}
	}
	if len(s.pluginIDs) == 0 {
		fmt.Fprintln(os.Stderr, "[-] 必须指定 --plugin")
		flag.Usage()
		os.Exit(2)
	}

	chosen, unknown := resolveChecks(checks)
	if len(unknown) > 0 {
		fmt.Fprintf(os.Stderr, "[-] 未知断言: %s\n\n", strings.Join(unknown, ", "))
		printCheckCatalog()
		os.Exit(2)
	}
	if len(chosen) == 0 {
		fmt.Fprintln(os.Stderr, "[-] 没有可执行的断言")
		os.Exit(2)
	}
	s.chosen = chosen

	if s.sandboxDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] 无法定位用户目录: %v\n", err)
			os.Exit(1)
		}
		s.sandboxDir = filepath.Join(home, ".cache", "cpa-plugins", "sandbox", strings.Join(s.pluginIDs, "+"))
	}
	for _, id := range s.pluginIDs {
		s.pluginDirs[id] = filepath.Join("plugins", id)
	}

	names := make([]string, 0, len(s.chosen))
	for _, c := range s.chosen {
		names = append(names, c.id)
	}
	fmt.Printf("[*] 执行断言: %s (未点名的不执行)\n", strings.Join(names, ", "))

	if err := s.run(); err != nil {
		fmt.Fprintf(os.Stderr, "\n[-] %v\n", err)
		s.dumpLogTail()
		s.stop()
		os.Exit(1)
	}
	fmt.Printf("\n[+] 沙箱验证通过。日志: %s\n", s.logPath)
	if s.keep {
		fmt.Printf("[+] 宿主仍在运行: http://127.0.0.1:%d (管理密钥: %s)\n", s.port, s.keyPath)
		return
	}
	s.stop()
}

func (s *sandbox) run() error {
	for _, id := range s.pluginIDs {
		if _, err := os.Stat(filepath.Join(s.pluginDirs[id], "go.mod")); err != nil {
			return fmt.Errorf("找不到插件 %s 的 go.mod, 确认 id 是否正确", id)
		}
	}

	sdkVersion, err := s.readSDKVersion(s.pluginDirs[s.pluginIDs[0]])
	if err != nil {
		return err
	}
	fmt.Printf("[*] 插件 %s, 目标宿主 SDK v%s\n", strings.Join(s.pluginIDs, ", "), sdkVersion)

	if err := s.resolveHost(sdkVersion); err != nil {
		return err
	}

	if err := os.RemoveAll(s.sandboxDir); err != nil {
		return err
	}
	if err := s.prepareLayout(); err != nil {
		return err
	}
	for _, id := range s.pluginIDs {
		if err := s.buildPlugin(id); err != nil {
			return err
		}
	}
	if err := s.writeConfig(); err != nil {
		return err
	}
	if err := s.startHost(); err != nil {
		return err
	}
	return s.assert()
}

func (s *sandbox) readSDKVersion(pluginDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(pluginDir, "go.mod"))
	if err != nil {
		return "", err
	}
	match := reGoModSDK.FindStringSubmatch(string(data))
	if len(match) < 2 {
		return "", fmt.Errorf("插件 go.mod 未声明 CLIProxyAPI SDK 依赖")
	}
	return match[1], nil
}

func (s *sandbox) resolveHost(sdkVersion string) error {
	if s.hostBinary != "" {
		if _, err := os.Stat(s.hostBinary); err != nil {
			return fmt.Errorf("--host 指定的二进制不存在: %s", s.hostBinary)
		}
		return s.verifyHostVersion(sdkVersion)
	}
	if env := strings.TrimSpace(os.Getenv("CPA_HOST_BIN")); env != "" {
		s.hostBinary = env
		return s.verifyHostVersion(sdkVersion)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	cached := filepath.Join(home, ".cache", "cpa-plugins", "host", "v"+sdkVersion, hostBinaryName())
	if _, err := os.Stat(cached); err == nil {
		s.hostBinary = cached
		fmt.Printf("[*] 使用缓存的宿主二进制: %s\n", cached)
		return s.verifyHostVersion(sdkVersion)
	}

	if s.hostSource == "" {
		return fmt.Errorf(`未找到可用的宿主二进制。请任选其一:
      --host /path/to/cliproxyapi          直接指定二进制
      --host-src /path/to/CLIProxyAPI      现场构建 (该目录应已 checkout 到 v%s)
      环境变量 CPA_HOST_BIN 或 CPA_HOST_SRC
      或把二进制预置到 %s`, sdkVersion, cached)
	}

	sourceVersion, err := gitDescribe(s.hostSource)
	if err != nil {
		return err
	}
	if sourceVersion != "v"+sdkVersion {
		return fmt.Errorf(`宿主源码版本与插件目标 SDK 不一致, 这样装载成功也不能证明兼容:
      插件 go.mod 要求: v%s
      源码当前版本:     %s
      请先在该目录执行: git checkout v%s`, sdkVersion, sourceVersion, sdkVersion)
	}

	if err := os.MkdirAll(filepath.Dir(cached), 0o755); err != nil {
		return err
	}
	fmt.Printf("[*] 从 %s 构建宿主 (v%s) 到缓存...\n", s.hostSource, sdkVersion)
	build := exec.Command("go", "build", "-o", cached, "./cmd/server")
	build.Dir = s.hostSource
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("构建宿主失败: %w", err)
	}
	s.hostBinary = cached
	return nil
}

// verifyHostVersion 只能核对源码版本, 二进制无法自省; 仅当显式传入 --host-src 时才校验。
func (s *sandbox) verifyHostVersion(sdkVersion string) error {
	if s.hostSource == "" {
		return nil
	}
	sourceVersion, err := gitDescribe(s.hostSource)
	if err != nil {
		return err
	}
	if sourceVersion != "v"+sdkVersion {
		return fmt.Errorf("宿主源码版本 %s 与插件目标 SDK v%s 不一致", sourceVersion, sdkVersion)
	}
	return nil
}

// cleanupHeader 清理 c-shared 顺带生成的同名头文件, 留在插件目录里属于构建垃圾。
func (s *sandbox) cleanupHeader(library string) error {
	header := strings.TrimSuffix(library, filepath.Ext(library)) + ".h"
	_ = os.Remove(header)
	return nil
}

func (s *sandbox) prepareLayout() error {
	s.configPath = filepath.Join(s.sandboxDir, "config.yaml")
	s.logPath = filepath.Join(s.sandboxDir, "host.log")
	s.keyPath = filepath.Join(s.sandboxDir, "management-key")

	if err := os.MkdirAll(filepath.Join(s.sandboxDir, "plugins", runtime.GOOS, runtime.GOARCH), 0o755); err != nil {
		return err
	}
	return os.MkdirAll(filepath.Join(s.sandboxDir, "auth"), 0o755)
}

// buildPlugin 编译单个插件到宿主扫描目录; 多插件时每个都产出一个动态库。
func (s *sandbox) buildPlugin(id string) error {
	library := filepath.Join(s.sandboxDir, "plugins", runtime.GOOS, runtime.GOARCH, id+platformExtension(runtime.GOOS))
	fmt.Printf("[*] 构建插件 %s -> %s\n", id, library)
	build := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
	build.Dir = s.pluginDirs[id]
	build.Env = append(os.Environ(), "CGO_ENABLED=1")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		// 本机默认 SDK 可能与 clang 不兼容 (27.0 的 tbd 含 arm64e.x1, tapi 报 unknown architecture);
		// 仅在首次失败后换已知可用的 SDK 重试一次, 不预先覆盖, 避免掩盖未来 SDK 问题。
		if _, set := os.LookupEnv("SDKROOT"); set {
			return fmt.Errorf("构建插件 %s 失败: %w", id, err)
		}
		if compat := compatSDKRoot(); compat != "" {
			fmt.Printf("[!] 默认 SDK 链接失败 (%v), 用 %s 重试\n", err, compat)
			retry := exec.Command("go", "build", "-buildmode=c-shared", "-o", library, ".")
			retry.Dir = s.pluginDirs[id]
			retry.Env = append(sdkRootEnv(compat), "CGO_ENABLED=1")
			retry.Stdout, retry.Stderr = os.Stdout, os.Stderr
			if err := retry.Run(); err != nil {
				return fmt.Errorf("构建插件 %s 失败 (SDKROOT=%s): %w", id, compat, err)
			}
			return s.cleanupHeader(library)
		}
		return fmt.Errorf("构建插件 %s 失败: %w", id, err)
	}
	return s.cleanupHeader(library)
}

// sdkRootEnv 把指定 SDK 路径注入环境, 替换已有 SDKROOT。
func sdkRootEnv(sdk string) []string {
	env := os.Environ()
	for i, kv := range env {
		if strings.HasPrefix(kv, "SDKROOT=") {
			env[i] = "SDKROOT=" + sdk
			return env
		}
	}
	return append(env, "SDKROOT="+sdk)
}

// compatSDKRoot 按已知可用版本挑本机 SDK, 找不到返回空。
func compatSDKRoot() string {
	base := "/Library/Developer/CommandLineTools/SDKs"
	for _, v := range []string{"MacOSX26.5.sdk", "MacOSX26.sdk", "MacOSX26.0.sdk"} {
		if st, err := os.Stat(filepath.Join(base, v)); err == nil && st.IsDir() {
			return filepath.Join(base, v)
		}
	}
	return ""
}

func (s *sandbox) writeConfig() error {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return err
	}
	s.secret = hex.EncodeToString(buffer)

	var builder strings.Builder
	fmt.Fprintf(&builder, "port: %d\n", s.port)
	fmt.Fprintf(&builder, "auth-dir: %q\n", filepath.Join(s.sandboxDir, "auth"))
	builder.WriteString("remote-management:\n")
	fmt.Fprintf(&builder, "  secret-key: %q\n", s.secret)
	builder.WriteString("plugins:\n")
	builder.WriteString("  enabled: true\n")
	fmt.Fprintf(&builder, "  dir: %q\n", filepath.Join(s.sandboxDir, "plugins"))
	builder.WriteString("  configs:\n")
	for _, id := range s.pluginIDs {
		fmt.Fprintf(&builder, "    %s:\n", id)
		builder.WriteString("      enabled: true\n")
		if s.profile != "" {
			fmt.Fprintf(&builder, "      identity-profile: %q\n", s.profile)
			fmt.Fprintf(&builder, "      login-profile: %q\n", s.profile)
		}
	}

	if err := os.WriteFile(s.configPath, []byte(builder.String()), 0o644); err != nil {
		return err
	}
	// 宿主装载后会把明文密钥哈希再写回配置, 明文随即消失; 要驱动管理面只能从这里读。
	if err := os.WriteFile(s.keyPath, []byte(s.secret), 0o600); err != nil {
		return err
	}
	fmt.Printf("[*] 写入沙箱配置: %s\n", s.configPath)
	return nil
}

func (s *sandbox) startHost() error {
	// 端口被占时先停下: 宿主会先打印 "API server started successfully" 再去 bind,
	// 若此处不拦, 就绪判据会通过, 断言则全部打到那个陌生实例上, 得出与本次插件无关的结论。
	if inUse, holder := portHolder(s.port); inUse {
		return fmt.Errorf("端口 %d 已被占用%s, 无法确认断言对象是本次启动的宿主; 先停掉占用方再跑",
			s.port, holder)
	}

	logFile, err := os.Create(s.logPath)
	if err != nil {
		return err
	}
	s.logFile = logFile

	command := exec.Command(s.hostBinary, "-config", s.configPath)
	command.Dir = s.sandboxDir
	command.Stdout, command.Stderr = logFile, logFile
	if err := command.Start(); err != nil {
		return fmt.Errorf("启动宿主失败: %w", err)
	}
	s.process = command
	s.exited = make(chan struct{})
	go func() {
		s.exitErr = command.Wait()
		close(s.exited)
	}()
	fmt.Printf("[*] 启动宿主 pid=%d, 等待就绪...\n", command.Process.Pid)

	deadline := time.Now().Add(s.timeout)
	for time.Now().Before(deadline) {
		select {
		case <-s.exited:
			return fmt.Errorf("宿主在就绪前退出: %v", s.exitErr)
		default:
		}
		if strings.Contains(s.readLog(), "API server started successfully") {
			// 日志行在 bind 之前就打印, 因此还要实证管理面认本次沙箱的密钥:
			// 认不了说明应答的不是我们启动的宿主。
			if err := s.verifyOwnership(); err != nil {
				return err
			}
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("等待宿主就绪超时 (%s)", s.timeout)
}

// verifyOwnership 用本次沙箱的密钥访问管理面, 确认应答者就是刚启动的宿主。
func (s *sandbox) verifyOwnership() error {
	body, status, err := s.httpGet("/v0/management/plugins", true)
	if err != nil {
		return fmt.Errorf("就绪后无法访问管理面, 无法确认宿主归属: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("就绪后管理面返回 HTTP %d (本沙箱密钥未被接受), 占用端口 %d 的不是本次启动的宿主: %s",
			status, s.port, truncate(body, 120))
	}
	return nil
}

// portHolder 探测端口是否已被监听, 并尽量给出占用者, 便于用户直接定位残留宿主。
func portHolder(port int) (bool, string) {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false, ""
	}
	_ = conn.Close()
	return true, ""
}

// assert 按注册顺序调度断言, 每个断言对每个插件各跑一次。
func (s *sandbox) assert() error {
	// 模型断言对所有插件复用一次 /v1/models 快照, 避免 N 次重复请求。
	served, err := s.servedModelIDs()
	if err != nil {
		return err
	}
	s.modelIDs = served

	for _, c := range s.chosen {
		for _, id := range s.pluginIDs {
			if err := c.run(s, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkLoad 断言宿主日志里该插件已完成装载与注册。
// 日志行形如 `pluginhost: plugin loaded plugin_id=<id> path=...`, 按 plugin_id 逐插件判定,
// 多插件共生时不会把「有插件装载了」误当成「这个插件装载了」。
func checkLoad(s *sandbox, pluginID string) error {
	log := s.readLog()
	loaded := strings.Contains(log, "plugin loaded plugin_id="+pluginID)
	registered := strings.Contains(log, "plugin registered plugin_id="+pluginID)
	if !loaded {
		return fmt.Errorf("宿主日志中没有 %s 的 plugin loaded 行, 插件未被装载", pluginID)
	}
	if !registered {
		return fmt.Errorf("宿主日志中有 %s 的 plugin loaded 但没有 plugin registered, 插件注册失败", pluginID)
	}
	fmt.Printf("[+] 断言通过: 插件 %s 已装载并注册\n", pluginID)

	if strings.Contains(log, "pluginhost: model registrar") && strings.Contains(log, "context deadline exceeded") {
		fmt.Println("[!] 警告: 日志中出现 model registrar 超时, 模型可能未完成注册")
	}
	return nil
}

// servedModelIDs 拉一次 /v1/models 供所有插件的模型断言复用; 未点名 models 断言时返回空。
func (s *sandbox) servedModelIDs() (map[string]bool, error) {
	if _, ok := lookupCheck("models"); !ok {
		return nil, nil
	}
	if !s.wantsCheck("models") {
		return nil, nil
	}
	body, status, err := s.httpGet("/v1/models", false)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("GET /v1/models 返回 %d: %s", status, truncate(body, 200))
	}

	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return nil, fmt.Errorf("解析 /v1/models 响应失败: %w", err)
	}

	served := map[string]bool{}
	for _, item := range payload.Data {
		served[item.ID] = true
	}
	return served, nil
}

// pluginEntry 是管理面 /v0/management/plugins 里单个插件的注册结果。
// 插件声明了什么能力, 以宿主注册后回报的字段为准, 不靠沙箱猜。
type pluginEntry struct {
	ID            string `json:"id"`
	SupportsQuota bool   `json:"supports_quota"`
	ConfigFields  []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"config_fields"`
	Menus []struct {
		Path string `json:"path"`
		Menu string `json:"menu"`
	} `json:"menus"`
}

// loadPluginEntries 拉一次 /v0/management/plugins 建立 id -> 条目快照。
// 管理面不可达时直接报错: 拿到空快照会让能力断言得出假绿结论。
func (s *sandbox) loadPluginEntries() error {
	if s.pluginEntries != nil {
		return nil
	}
	body, status, err := s.httpGet("/v0/management/plugins", true)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET /v0/management/plugins 返回 %d: %s", status, truncate(body, 200))
	}
	var payload struct {
		Plugins []pluginEntry `json:"plugins"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return fmt.Errorf("解析 /v0/management/plugins 响应失败: %w", err)
	}
	s.pluginEntries = make(map[string]pluginEntry, len(payload.Plugins))
	for _, entry := range payload.Plugins {
		s.pluginEntries[entry.ID] = entry
	}
	return nil
}

func (s *sandbox) pluginEntryFor(pluginID string) (pluginEntry, error) {
	if err := s.loadPluginEntries(); err != nil {
		return pluginEntry{}, err
	}
	entry, ok := s.pluginEntries[pluginID]
	if !ok {
		return pluginEntry{}, fmt.Errorf("/v0/management/plugins 列表中没有找到插件 %s", pluginID)
	}
	return entry, nil
}

func (s *sandbox) wantsCheck(id string) bool {
	for _, c := range s.chosen {
		if c.id == id {
			return true
		}
	}
	return false
}

func checkModels(s *sandbox, pluginID string) error {
	served := s.modelIDs
	declared, err := s.declaredModels(pluginID)
	if err != nil {
		return err
	}

	// 无静态清单的插件可能不提供模型 (例如只做用量或管理的插件), 因此空列表不算失败;
	// 有静态清单时, 声明的模型必须全部在列。
	if len(declared) == 0 {
		fmt.Printf("[+] 断言通过: /v1/models 返回 %d 个模型, 插件无静态清单, 不比对\n", len(served))
		return nil
	}

	// 插件注册侧可能给模型 id 加前缀 (workbuddy 的 enable-model-prefix, 默认开启),
	// 清单声明的是裸 id; 断言按「前缀 id 或裸 id 命中其一」匹配。
	var missing []string
	for _, id := range declared {
		prefixed := pluginID + "/" + id
		if !served[id] && !served[prefixed] {
			missing = append(missing, id)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("插件 %s 的静态清单声明了 %d 个模型, 但 /v1/models 少了 %d 个: %s",
			pluginID, len(declared), len(missing), strings.Join(missing, ", "))
	}
	fmt.Printf("[+] 断言通过: %s, /v1/models 返回 %d 个模型, 清单声明的 %d 个全部在列\n",
		pluginID, len(served), len(declared))
	return nil
}

// checkResource 验证插件声明的每个 resource 页面都能被宿主服务。
// 路径来自管理面回报的菜单 (插件自己声明的 ResourceRoute), 不硬编码: 各插件的
// resource 路径不同 (各插件的 panel 路由各不相同),
// 硬编码只会把「断言跑错页面」当成功能问题。
func checkResource(s *sandbox, pluginID string) error {
	entry, err := s.pluginEntryFor(pluginID)
	if err != nil {
		return err
	}
	if len(entry.Menus) == 0 {
		return fmt.Errorf("插件 %s 未声明任何 resource 页面, 面板侧边栏不会有入口", pluginID)
	}
	for _, menu := range entry.Menus {
		body, status, err := s.httpGet(menu.Path, false)
		if err != nil {
			return err
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET %s 返回 %d: %s", menu.Path, status, truncate(body, 200))
		}
		if !strings.Contains(body, pluginID) {
			return fmt.Errorf("%s 的内容不含 %q, 疑似服务了错误页面: %s",
				menu.Path, pluginID, truncate(body, 200))
		}
	}
	fmt.Printf("[+] 断言通过: 插件 %s 的 %d 个 resource 页面已注册且可访问\n", pluginID, len(entry.Menus))
	return nil
}

// checkMenus 验证管理面 plugins 列表暴露了插件菜单 (面板侧边栏入口)。
func checkMenus(s *sandbox, pluginID string) error {
	entry, err := s.pluginEntryFor(pluginID)
	if err != nil {
		return err
	}
	if len(entry.Menus) == 0 {
		return fmt.Errorf("插件 %s 未注册任何菜单, 面板侧边栏不会显示", pluginID)
	}
	menus := make([]string, 0, len(entry.Menus))
	for _, menu := range entry.Menus {
		menus = append(menus, menu.Menu+" ("+menu.Path+")")
	}
	fmt.Printf("[+] 断言通过: %s 插件菜单已注册: %s\n", pluginID, strings.Join(menus, ", "))
	return nil
}

// declaredModels 读取插件内嵌静态清单里声明的模型 id。
// 插件可能按自身规则做过滤, 因此断言方向是「声明的必须都在」而不是「完全相等」。
//
// 注意: 本仓当前三个 provider 插件 (workbuddy/trae/qoder) 都走动态模型发现, 没有
// data/static-config.json, 因此对它们而言该函数返回 nil, checkModels 会退化为
// 「不比对」。静态清单是给手工维护模型表的插件用的; 新增此类插件时把清单放进
// plugins/<id>/data/static-config.json 即可重新启用比对。
func (s *sandbox) declaredModels(pluginID string) ([]string, error) {
	path := filepath.Join(s.pluginDirs[pluginID], "data", "static-config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var manifest struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", path, err)
	}

	ids := make([]string, 0, len(manifest.Models))
	for _, item := range manifest.Models {
		for _, key := range []string{"id", "ID"} {
			if value, ok := item[key].(string); ok && value != "" {
				ids = append(ids, value)
				break
			}
		}
	}
	return ids, nil
}

// checkQuota 断言「插件声明的额度能力」与「宿主实际注册的额度提供方」一致。
// 判据取自宿主回报的 supports_quota (插件注册时自己声明的), 因此两个方向都可证伪:
// 声明了却不在提供方列表, 或没声明却混进列表, 都是注册层漂移。管理面不可达直接失败,
// 否则空结果会被当成「插件没实现额度」而静默放行。
func checkQuota(s *sandbox, pluginID string) error {
	entry, err := s.pluginEntryFor(pluginID)
	if err != nil {
		return err
	}
	body, status, err := s.httpGet("/v0/management/quota/providers", true)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("GET /v0/management/quota/providers 返回 %d: %s", status, truncate(body, 200))
	}
	var payload struct {
		Providers []struct {
			PluginID string `json:"plugin_id"`
			Provider string `json:"provider"`
		} `json:"providers"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return fmt.Errorf("解析额度提供方列表失败: %w", err)
	}
	found := false
	for _, p := range payload.Providers {
		if p.PluginID == pluginID || p.Provider == pluginID {
			found = true
			break
		}
	}
	switch {
	case entry.SupportsQuota && !found:
		return fmt.Errorf("插件 %s 声明了 supports_quota 但不在额度提供方列表中, 额度页不会有该渠道", pluginID)
	case !entry.SupportsQuota && found:
		return fmt.Errorf("插件 %s 未声明 supports_quota 却出现在额度提供方列表中, 注册层与声明不一致", pluginID)
	case !entry.SupportsQuota:
		fmt.Printf("[i] 插件 %s 未声明额度能力, 跳过 (不判失败)\n", pluginID)
		return nil
	}
	fmt.Printf("[+] 断言通过: %s 声明了额度能力且在提供方列表中\n", pluginID)
	return nil
}

// checkConfig 断言「插件在源码里声明的可视化配置字段」都被宿主回报出来。
// 声明从插件源码读 (注册元数据里唯一的前置事实), 回报从管理面读; 两者不一致即失败,
// 不会因为 config_fields 为空就默认通过 —— 没声明配置字段的插件本来就不该报字段。
func checkConfig(s *sandbox, pluginID string) error {
	declared, err := declaredConfigFields(s.pluginDirs[pluginID])
	if err != nil {
		return err
	}
	entry, err := s.pluginEntryFor(pluginID)
	if err != nil {
		return err
	}
	if len(declared) == 0 {
		if len(entry.ConfigFields) > 0 {
			return fmt.Errorf("插件 %s 源码未声明配置字段, 管理面却回报了 %d 个, 注册层与声明不一致",
				pluginID, len(entry.ConfigFields))
		}
		fmt.Printf("[i] 插件 %s 未声明可视化配置字段, 跳过 (不判失败)\n", pluginID)
		return nil
	}
	reported := make(map[string]bool, len(entry.ConfigFields))
	names := make([]string, 0, len(entry.ConfigFields))
	for _, field := range entry.ConfigFields {
		reported[field.Name] = true
		names = append(names, field.Name)
	}
	var missing []string
	for _, name := range declared {
		if !reported[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("插件 %s 源码声明了 %d 个配置字段, 管理面少了 %d 个: %s (实际回报: %s)",
			pluginID, len(declared), len(missing), strings.Join(missing, ", "), strings.Join(names, ", "))
	}
	fmt.Printf("[+] 断言通过: %s 声明的 %d 个配置字段管理面全部返回: %s\n",
		pluginID, len(declared), strings.Join(names, ", "))
	return nil
}

// declaredConfigFields 从插件源码里读出注册元数据声明的配置字段名。
// 配置字段只在注册元数据里声明一次 (main.go 的 metadata.ConfigFields), 宿主回报的
// config_fields 由它派生, 因此源码是这里的独立前置事实。
func declaredConfigFields(pluginDir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(pluginDir, "*.go"))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		names = append(names, configFieldNames(string(data))...)
	}
	return names, nil
}

// configFieldNames 取 ConfigFields 字面量块内的 Name 取值。
// 从 marker 后第一个 '{' 起算花括号深度, 归零即块结束, 不会把后续字面量里的 Name 算进来。
func configFieldNames(source string) []string {
	const marker = "ConfigFields:"
	var names []string
	for offset := 0; ; {
		idx := strings.Index(source[offset:], marker)
		if idx < 0 {
			return names
		}
		start := strings.Index(source[offset+idx+len(marker):], "{")
		if start < 0 {
			return names
		}
		start += offset + idx + len(marker)
		depth, end := 0, -1
		for i := start; i < len(source); i++ {
			switch source[i] {
			case '{':
				depth++
			case '}':
				if depth--; depth == 0 {
					end = i
				}
			}
			if end >= 0 {
				break
			}
		}
		if end < 0 {
			end = len(source)
		}
		names = append(names, fieldNamesIn(source[start:end])...)
		offset = end + 1
		if offset >= len(source) {
			return names
		}
	}
}

// fieldNamesIn 取代码片段里 `Name: "..."` 的取值, 空串不算声明。
func fieldNamesIn(fragment string) []string {
	var names []string
	for offset := 0; ; {
		idx := strings.Index(fragment[offset:], "Name:")
		if idx < 0 {
			return names
		}
		rest := fragment[offset+idx+len("Name:"):]
		quote := strings.Index(rest, `"`)
		if quote < 0 {
			return names
		}
		closing := strings.Index(rest[quote+1:], `"`)
		if closing < 0 {
			return names
		}
		value := rest[quote+1 : quote+1+closing]
		if strings.TrimSpace(value) != "" {
			names = append(names, value)
		}
		offset = offset + idx + len("Name:") + quote + 1 + closing + 1
	}
}

func (s *sandbox) httpGet(path string, management bool) (string, int, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d%s", s.port, path)
	request, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	if management {
		request.Header.Set("Authorization", "Bearer "+s.secret)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return "", 0, fmt.Errorf("请求 %s 失败: %w", path, err)
	}
	defer response.Body.Close()

	data, err := io.ReadAll(response.Body)
	if err != nil {
		return "", response.StatusCode, err
	}
	return string(data), response.StatusCode, nil
}

func (s *sandbox) readLog() string {
	if s.logPath == "" {
		return ""
	}
	data, err := os.ReadFile(s.logPath)
	if err != nil {
		return ""
	}
	return string(data)
}

func (s *sandbox) dumpLogTail() {
	log := s.readLog()
	if log == "" {
		return
	}
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	if len(lines) > 25 {
		lines = lines[len(lines)-25:]
	}
	fmt.Fprintf(os.Stderr, "\n--- 宿主日志尾部 (%s) ---\n%s\n", s.logPath, strings.Join(lines, "\n"))
}

func (s *sandbox) stop() {
	if s.process == nil || s.process.Process == nil {
		return
	}
	_ = s.process.Process.Signal(os.Interrupt)
	// 退出检测与 stop 共用 startHost 里那一个 Wait, 重复 Wait 拿不到真实退出状态。
	if s.exited != nil {
		select {
		case <-s.exited:
		case <-time.After(5 * time.Second):
			_ = s.process.Process.Kill()
			<-s.exited
		}
	}
	if s.logFile != nil {
		_ = s.logFile.Close()
	}
	s.process = nil
}

// platformExtension 与宿主 internal/pluginstore 的 pluginExtension 保持一致
func platformExtension(goos string) string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "darwin", "mac", "macos", "osx":
		return ".dylib"
	case "windows":
		return ".dll"
	default:
		return ".so"
	}
}

func hostBinaryName() string {
	if runtime.GOOS == "windows" {
		return "cliproxyapi.exe"
	}
	return "cliproxyapi"
}

func gitDescribe(dir string) (string, error) {
	command := exec.Command("git", "-C", dir, "describe", "--tags")
	output, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("读取 %s 的 git 版本失败: %w", dir, err)
	}
	return strings.TrimSpace(string(output)), nil
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}
