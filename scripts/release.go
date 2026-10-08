package main

// 插件发布工具: 本地打包 + 契约自检 + 哈希回填 + 重建 registry.json
//
// 存在的理由: 宿主 internal/pluginstore 对产物命名有硬性要求, 不满足则安装失败,
// 而 CI 只有推到标签后才会执行一次。本脚本把同一套约束前移到本地, 让发布风险在
// 打包阶段暴露, 而不是等用户装不上才发现。
//
// 用法:
//	go run scripts/release.go version --plugin <id> [--version <v>]
//	go run scripts/release.go pack    --plugin <id> [--version <v>] [--out dist] [--goos <os>] [--goarch <arch>]
//	go run scripts/release.go record  --plugin <id> [--dist dist]
//	go run scripts/release.go publish --plugin <id> [--version <v>] [--dry-run] [--no-push]
//
// --version 是显式版本覆盖: 默认按变更集递增推导, 指定后直接用该版本号(仍要求有未消费的
// 变更集, 且必须是 x.y.z)。用于"把若干插件统一到同一版本号"这类递增推导做不到的需求;
// 版本号与产物地址、Go 字面量、标签仍由本脚本一处写出, 不走手改。
//
// publish 把发版收敛成一条命令: 预检 → version → 重建 registry → 门禁 → 提交 → 打标签 → 推送。
// 顺序、命名与推送次序由脚本固定, 人只写变更集; 变更集已被消费(版本已 bump 但标签未推)时
// 只补标签, 可重复执行。--dry-run 只打印将要发生的事。
//
// version 消费变更集, 计算并更新 plugin.json 版本与产物地址, 同步 Go 版本字面量。
// pack    构建当前平台动态库, 打成宿主契约命名的 zip, 打印 sha256 与 size; 产物用于本地预检,
//         只有在确实被上传发布时才能据其回填哈希。
// record  读取 dist 下已有 zip, 把 sha256 回填进 plugin.json, 再重建 registry.json。
//         只用于本地预检; 正式回填由 CI 的 record 作业在标签发布后执行。
//
// 发布流程 (哈希必须来自真实上传的产物, 否则安装时报 checksum mismatch):
//
//	go run scripts/check-plugins.go --release-ready          # 发布前门禁
//	go run scripts/release.go pack --plugin <id> --out dist  # 本地预检包结构与命名
//	git tag <id>/v<X.Y.Z> && git push origin main <id>/v<X.Y.Z>
//
// 标签触发流水线后, 由 CI 的 record 作业下载真实产物并回填 plugin.json 与 registry.json。
// 禁止本地代填: 本机 pack 的产物未上传, 用它回填会让安装校验失败; 失败恢复用重打标签,
// 见 docs/how-to/plugin-release.md。多插件发版逐个推标签, 等上一条 record 完成再推下一个。
//
// 说明: 只支持为当前平台打包。c-shared 是 CGO 构建, 交叉编译需要目标平台的 C 工具链,
// 因此 CI 用各平台原生运行器分别构建, 本地同理只构建本机平台。

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// ---------------- 宿主契约 ----------------

// releaseRepoBase 是本仓在 GitHub 上的仓库地址, 产物下载地址由它派生。
// 必须与 build-registry.go 的 --repo 默认值一致, 否则 registry.json 里的
// 产物地址会指向另一个仓库。
const releaseRepoBase = "https://github.com/varcli/cpa-plugins"

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

// archiveName 与宿主 internal/pluginstore 的 ArchiveName 保持一致
func archiveName(id, version, goos, goarch string) string {
	return fmt.Sprintf("%s_%s_%s_%s.zip", id, version, goos, goarch)
}

var reGoVersionLiteral = regexp.MustCompile(`(\bVersion:\s*)"([0-9]+\.[0-9]+\.[0-9]+[^"]*)"`)

// 发布工作流是各平台构建矩阵的唯一事实来源: version 按它物化 plugin.json 的
// artifacts, 新增平台只需改工作流一处, 不会出现"矩阵里有、清单里没有"的漂移。
var reWorkflowGOOS = regexp.MustCompile(`^\s*-\s*goos:\s*"?([A-Za-z0-9_]+)"?\s*$`)
var reWorkflowGOARCH = regexp.MustCompile(`^\s*goarch:\s*"?([A-Za-z0-9_]+)"?\s*$`)

const releaseWorkflowPath = ".github/workflows/release-plugin.yml"

type releasePlatform struct {
	GOOS   string
	GOARCH string
}

// releaseWorkflowPlatforms 按工作流里出现的顺序返回构建矩阵的平台集合。
func releaseWorkflowPlatforms() ([]releasePlatform, error) {
	data, err := os.ReadFile(releaseWorkflowPath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", releaseWorkflowPath, err)
	}
	var platforms []releasePlatform
	pendingGOOS := ""
	for _, line := range strings.Split(string(data), "\n") {
		if match := reWorkflowGOOS.FindStringSubmatch(line); len(match) > 1 {
			pendingGOOS = strings.TrimSpace(match[1])
			continue
		}
		if match := reWorkflowGOARCH.FindStringSubmatch(line); len(match) > 1 && pendingGOOS != "" {
			platforms = append(platforms, releasePlatform{GOOS: pendingGOOS, GOARCH: strings.TrimSpace(match[1])})
			pendingGOOS = ""
		}
	}
	if len(platforms) == 0 {
		return nil, fmt.Errorf("%s 中未解析到任何构建平台", releaseWorkflowPath)
	}
	return platforms, nil
}

// reSemver 校验 --version 显式覆盖值; 与 bumpVersion 的 x.y.z 约定一致。
var reSemver = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

// ---------------- 变更集 ----------------

type changeset struct {
	Bump string `json:"bump"`
	Note string `json:"note"`
}

const (
	bumpRankNone  = 0
	bumpRankPatch = 1
	bumpRankMinor = 2
	bumpRankMajor = 3
)

func parseBumpRank(bump string) (int, error) {
	switch strings.TrimSpace(bump) {
	case "patch":
		return bumpRankPatch, nil
	case "minor":
		return bumpRankMinor, nil
	case "major":
		return bumpRankMajor, nil
	default:
		return bumpRankNone, fmt.Errorf("非法的 bump 值 %q (仅支持 patch, minor, major)", bump)
	}
}

func bumpVersion(current, bump string) (string, error) {
	v := strings.TrimPrefix(current, "v")
	parts := strings.Split(v, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("版本号 %q 不符合 x.y.z 语义化版本格式", current)
	}
	major, err1 := strconv.Atoi(parts[0])
	minor, err2 := strconv.Atoi(parts[1])
	patch, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil || major < 0 || minor < 0 || patch < 0 {
		return "", fmt.Errorf("版本号 %q 包含非法数字", current)
	}

	switch bump {
	case "major":
		return fmt.Sprintf("%d.0.0", major+1), nil
	case "minor":
		return fmt.Sprintf("%d.%d.0", major, minor+1), nil
	case "patch":
		return fmt.Sprintf("%d.%d.%d", major, minor, patch+1), nil
	default:
		return "", fmt.Errorf("未知的 bump 类型: %s", bump)
	}
}

// compareSemver 按 x.y.z 逐段比较, 返回 -1/0/1。用于拦截 --version 的降级误操作。
func compareSemver(a, b string) (int, error) {
	parse := func(s string) ([3]int, error) {
		var out [3]int
		parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(s), "v"), ".")
		if len(parts) != 3 {
			return out, fmt.Errorf("版本号 %q 不符合 x.y.z 语义化版本格式", s)
		}
		for i, p := range parts {
			n, err := strconv.Atoi(p)
			if err != nil || n < 0 {
				return out, fmt.Errorf("版本号 %q 包含非法数字", s)
			}
			out[i] = n
		}
		return out, nil
	}
	av, err := parse(a)
	if err != nil {
		return 0, err
	}
	bv, err := parse(b)
	if err != nil {
		return 0, err
	}
	for i := 0; i < 3; i++ {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	return 0, nil
}

// ---------------- 任务: version ----------------

// versionPlan 是一次版本产出的前置计算: 变更集、当前版本与目标版本。
// version 与 publish 共用同一份计算, 保证 dry-run 打印的版本与实际执行一致。
type versionPlan struct {
	currentVersion string
	nextVersion    string
	changesetFiles []string
	manifest       map[string]any
	manifestPath   string
}

func planNextVersion(id string, overrideVersion string) (*versionPlan, error) {
	changesetsDir := filepath.Join("plugins", id, "changesets")
	dirInfo, err := os.Stat(changesetsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("变更集目录不存在: %s", changesetsDir)
		}
		return nil, fmt.Errorf("访问变更集目录失败: %w", err)
	}
	if !dirInfo.IsDir() {
		return nil, fmt.Errorf("变更集路径不是目录: %s", changesetsDir)
	}

	changesetFiles, err := filepath.Glob(filepath.Join(changesetsDir, "*.json"))
	if err != nil {
		return nil, fmt.Errorf("扫描变更集文件失败: %w", err)
	}
	if len(changesetFiles) == 0 {
		return nil, fmt.Errorf("变更集目录中没有 json 文件: %s", changesetsDir)
	}

	maxRank := bumpRankNone
	for _, csPath := range changesetFiles {
		data, err := os.ReadFile(csPath)
		if err != nil {
			return nil, fmt.Errorf("读取变更集 %s 失败: %w", csPath, err)
		}
		var cs changeset
		if err := json.Unmarshal(data, &cs); err != nil {
			return nil, fmt.Errorf("解析变更集 %s 失败: %w", csPath, err)
		}
		rank, err := parseBumpRank(cs.Bump)
		if err != nil {
			return nil, fmt.Errorf("变更集 %s: %w", csPath, err)
		}
		if rank > maxRank {
			maxRank = rank
		}
	}

	var highestBump string
	switch maxRank {
	case bumpRankPatch:
		highestBump = "patch"
	case bumpRankMinor:
		highestBump = "minor"
	case bumpRankMajor:
		highestBump = "major"
	default:
		return nil, fmt.Errorf("未解析到有效的 bump 级别")
	}

	manifestPath := filepath.Join("plugins", id, "plugin.json")
	manifest, err := readPluginManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("读取 plugin.json 失败: %w", err)
	}
	currentVersion := manifestString(manifest, "version")
	if currentVersion == "" {
		return nil, fmt.Errorf("plugin.json 缺少 version")
	}

	nextVersion, err := bumpVersion(currentVersion, highestBump)
	if err != nil {
		return nil, fmt.Errorf("计算新版本号失败: %w", err)
	}

	// 显式覆盖: 用于递增推导到不了的版本号 (例如把多个插件统一到 0.5.0)。
	// 仍要求存在未消费的变更集, 保证"改动留下过版本意图"的门禁不被绕过。
	if v := strings.TrimSpace(overrideVersion); v != "" {
		v = strings.TrimPrefix(v, "v")
		if !reSemver.MatchString(v) {
			return nil, fmt.Errorf("--version %q 不符合 x.y.z 语义化版本格式", overrideVersion)
		}
		if cmp, err := compareSemver(v, currentVersion); err != nil {
			return nil, err
		} else if cmp == 0 {
			return nil, fmt.Errorf("--version %s 与当前版本相同, 无需发布", v)
		} else if cmp < 0 {
			return nil, fmt.Errorf("--version %s 低于当前版本 %s, 拒绝降级发布", v, currentVersion)
		}
		nextVersion = v
	}

	return &versionPlan{
		currentVersion: currentVersion,
		nextVersion:    nextVersion,
		changesetFiles: changesetFiles,
		manifest:       manifest,
		manifestPath:   manifestPath,
	}, nil
}

func runVersion(args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	pluginID := fs.String("plugin", "", "插件 id")
	overrideVersion := fs.String("version", "", "显式指定版本号 (默认按变更集递增)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*pluginID) == "" {
		fs.Usage()
		return fmt.Errorf("必须指定 --plugin")
	}
	id := strings.TrimSpace(*pluginID)

	plan, err := planNextVersion(id, *overrideVersion)
	if err != nil {
		return err
	}
	nextVersion := plan.nextVersion
	manifest, manifestPath, changesetFiles := plan.manifest, plan.manifestPath, plan.changesetFiles

	goFiles, err := filepath.Glob(filepath.Join("plugins", id, "*.go"))
	if err != nil {
		return fmt.Errorf("扫描 Go 源文件失败: %w", err)
	}
	if len(goFiles) == 0 {
		return fmt.Errorf("在 plugins/%s/ 下未找到任何 .go 文件", id)
	}

	totalHits := 0
	var targetFile string
	var targetContent string

	for _, goFile := range goFiles {
		data, err := os.ReadFile(goFile)
		if err != nil {
			return fmt.Errorf("读取 %s 失败: %w", goFile, err)
		}
		content := string(data)
		matches := reGoVersionLiteral.FindAllStringIndex(content, -1)
		if len(matches) > 0 {
			totalHits += len(matches)
			targetFile = goFile
			targetContent = content
		}
	}

	if totalHits == 0 {
		return fmt.Errorf("在 plugins/%s/*.go 中未找到 Version: \"x.y.z\" 版本字面量", id)
	}
	if totalHits > 1 {
		return fmt.Errorf("在 plugins/%s/*.go 中匹配 Version: \"x.y.z\" 命中 %d 处, 期望恰好 1 处", id, totalHits)
	}

	// 1. 同步 Go 代码中的版本字面量
	newGoContent := reGoVersionLiteral.ReplaceAllString(targetContent, "${1}\""+nextVersion+"\"")
	if err := os.WriteFile(targetFile, []byte(newGoContent), 0o644); err != nil {
		return fmt.Errorf("更新 %s 失败: %w", targetFile, err)
	}

	// 2. 更新 plugin.json: version 与 artifacts[].url 重算, sha256 置空, size 置 0
	manifest["version"] = nextVersion
	install, ok := manifest["install"].(map[string]any)
	if !ok {
		return fmt.Errorf("plugin.json 缺少 install 段")
	}
	// artifacts 以发布工作流的构建矩阵为准: 矩阵新增平台(如 windows)时, 这里自动
	// 补出对应条目; 矩阵去掉平台时一并移除。哈希留空, 由 CI 的 record 作业按真实
	// 上传产物回填 —— 本地产物不能用来回填, 否则安装时报 checksum mismatch。
	platforms, err := releaseWorkflowPlatforms()
	if err != nil {
		return err
	}
	existing := map[string]map[string]any{}
	if rawArtifacts, ok := install["artifacts"].([]any); ok {
		for _, raw := range rawArtifacts {
			entry, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			goos, _ := entry["goos"].(string)
			goarch, _ := entry["goarch"].(string)
			if goos == "" || goarch == "" {
				continue
			}
			existing[goos+"/"+goarch] = entry
		}
	}
	materialized := make([]any, 0, len(platforms))
	for _, platform := range platforms {
		entry := existing[platform.GOOS+"/"+platform.GOARCH]
		if entry == nil {
			entry = map[string]any{"goos": platform.GOOS, "goarch": platform.GOARCH}
			fmt.Printf("[+] 按工作流矩阵新增平台 %s/%s\n", platform.GOOS, platform.GOARCH)
		}
		entry["url"] = fmt.Sprintf("%s/releases/download/%s%%2Fv%s/%s",
			releaseRepoBase, id, nextVersion, archiveName(id, nextVersion, platform.GOOS, platform.GOARCH))
		entry["sha256"] = ""
		entry["size"] = json.Number("0")
		materialized = append(materialized, entry)
	}
	install["artifacts"] = materialized

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 plugin.json 失败: %w", err)
	}
	if err := os.WriteFile(manifestPath, append(encoded, '\n'), 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", manifestPath, err)
	}

	// 3. 删除已消费的变更集文件
	for _, csPath := range changesetFiles {
		if err := os.Remove(csPath); err != nil {
			return fmt.Errorf("删除变更集文件 %s 失败: %w", csPath, err)
		}
	}

	// 4. stdout 打印新版本与 tag 供人复制; 不执行任何 git 命令
	tag := fmt.Sprintf("%s/v%s", id, nextVersion)
	fmt.Printf("新版本: %s\n", nextVersion)
	fmt.Printf("标签: %s\n", tag)
	return nil
}

// ---------------- 任务: publish ----------------

// publish 一条命令做完发版: 预检 → 产出新版本 → 重建 registry → 门禁 → 提交 → 打标签 → 推送。
// 存在的理由: 手工按文档跑六步时, 顺序与命名全靠人记 —— 版本产出却没打标签会让 registry 的
// 产物地址指向不存在的资产, 而"先 tag 后 commit"会让 CI 拿到标签时提交还不在 main 上。
// 这里把顺序写成代码, 人只写变更集。
func runPublish(args []string) error {
	fs := flag.NewFlagSet("publish", flag.ContinueOnError)
	pluginID := fs.String("plugin", "", "插件 id")
	overrideVersion := fs.String("version", "", "显式指定版本号 (默认按变更集递增)")
	dryRun := fs.Bool("dry-run", false, "只打印将要执行的动作, 不写文件、不提交、不推送")
	noPush := fs.Bool("no-push", false, "提交与打标签后不推送 (自检用)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*pluginID) == "" {
		return fmt.Errorf("必须指定 --plugin")
	}
	id := strings.TrimSpace(*pluginID)

	if err := preflightPublish(); err != nil {
		return err
	}

	changesets, err := filepath.Glob(filepath.Join("plugins", id, "changesets", "*.json"))
	if err != nil {
		return fmt.Errorf("扫描变更集文件失败: %w", err)
	}

	// 变更集已被消费: 版本已 bump, 缺的是标签。只补标签, 保证重跑幂等。
	if len(changesets) == 0 {
		manifest, err := readPluginManifest(filepath.Join("plugins", id, "plugin.json"))
		if err != nil {
			return fmt.Errorf("读取 plugin.json 失败: %w", err)
		}
		version := manifestString(manifest, "version")
		if version == "" {
			return fmt.Errorf("plugin.json 缺少 version")
		}
		tag := fmt.Sprintf("%s/v%s", id, version)
		if tagExists(tag) {
			fmt.Printf("标签 %s 已存在, 本次只补提交与标签的推送 (可重复执行)\n", tag)
		} else {
			fmt.Printf("变更集已消费 (plugin.json = %s), 补标签与推送: %s\n", version, tag)
		}
		if *dryRun {
			printPublishTail(tag)
			return nil
		}
		if err := tagAndPush(tag, *noPush); err != nil {
			return err
		}
		printPublishTail(tag)
		return nil
	}

	plan, err := planNextVersion(id, *overrideVersion)
	if err != nil {
		return err
	}
	tag := fmt.Sprintf("%s/v%s", id, plan.nextVersion)
	if tagExists(tag) {
		return fmt.Errorf("标签 %s 已存在但变更集未消费: 变更集与版本可能重复, 先确认再发布", tag)
	}

	fmt.Printf("插件 %s: %s -> %s (标签 %s)\n", id, plan.currentVersion, plan.nextVersion, tag)
	if *dryRun {
		fmt.Println("将依次执行: check-plugins → version → build-registry → git add/commit → git tag → git push(main, tag)")
		printPublishTail(tag)
		return nil
	}

	// 先过与 CI 同一条门禁: 它检查的是仓库既有状态, 必须在写文件之前跑,
	// 否则"刚 bump 还没打标签"会被它自己当成问题报出来。
	if err := goRunScript("check-plugins.go"); err != nil {
		return err
	}
	if err := runVersion(versionArgs(id, *overrideVersion)); err != nil {
		return err
	}
	if err := goRunScript("build-registry.go"); err != nil {
		return err
	}
	if err := gitRun("add", filepath.Join("plugins", id), "registry.json"); err != nil {
		return err
	}
	if err := gitRun("commit", "-m",
		fmt.Sprintf("chore(%s): %s -> %s", id, plan.currentVersion, plan.nextVersion)); err != nil {
		return err
	}
	if err := tagAndPush(tag, *noPush); err != nil {
		return err
	}
	printPublishTail(tag)
	return nil
}

// preflightPublish 发布前置条件: 在 main 上、无未提交的已跟踪改动、不落后于 origin/main。
// 允许"本地领先"这种状态 —— 上一次发布提交成功但推送失败时, 重跑要能接着把提交与标签推上去。
func preflightPublish() error {
	branch := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	if branch != "main" {
		return fmt.Errorf("发布必须在 main 上执行, 当前分支 %q", branch)
	}
	if dirty := gitOutput("status", "--porcelain", "--untracked-files=no"); dirty != "" {
		return fmt.Errorf("工作区有未提交的已跟踪改动, 先提交或还原:\n%s", dirty)
	}
	if err := gitRun("fetch", "origin", "main"); err != nil {
		return err
	}
	local, remote := gitOutput("rev-parse", "HEAD"), gitOutput("rev-parse", "origin/main")
	if local == "" || remote == "" {
		return fmt.Errorf("读取 HEAD 或 origin/main 失败")
	}
	if local == remote {
		return nil
	}
	if isAncestor(remote, local) {
		fmt.Printf("[i] 本地领先 origin/main (%s -> %s), 发布时会一起推送\n", shortSHA(remote), shortSHA(local))
		return nil
	}
	return fmt.Errorf("HEAD (%s) 落后或分叉于 origin/main (%s), 先 git pull origin main", shortSHA(local), shortSHA(remote))
}

func isAncestor(maybeAncestor, ref string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", maybeAncestor, ref)
	return cmd.Run() == nil
}

// tagAndPush 固定推送次序: 先 main 再标签, 保证 CI 收到标签时提交已在 main 上。
func tagAndPush(tag string, noPush bool) error {
	if tagExists(tag) {
		fmt.Printf("标签 %s 已存在, 跳过创建\n", tag)
	} else if err := gitRun("tag", tag); err != nil {
		return err
	}
	if noPush {
		fmt.Printf("标签 %s 就绪 (--no-push, 未推送)\n", tag)
		return nil
	}
	if err := gitRun("push", "origin", "main"); err != nil {
		return err
	}
	return gitRun("push", "origin", tag)
}

// versionArgs 组装 publish 内部转调 version 的参数; 显式版本必须透传, 否则
// 预检打印的版本与实际写盘的版本会不一致。
func versionArgs(id, overrideVersion string) []string {
	args := []string{"--plugin", id}
	if v := strings.TrimSpace(overrideVersion); v != "" {
		args = append(args, "--version", v)
	}
	return args
}

func printPublishTail(tag string) {
	fmt.Printf("\n下一步: 1) 等 CI 的 record 作业回填 sha256; 2) git pull origin main; ")
	fmt.Printf("3) go run scripts/verify-registry-install.go %s\n", strings.SplitN(tag, "/", 2)[0])
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

func gitOutput(args ...string) string {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func gitRun(args ...string) error {
	cmd := exec.Command("git", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git %s 失败: %w", strings.Join(args, " "), err)
	}
	return nil
}

func tagExists(tag string) bool {
	return gitOutput("tag", "--list", tag) == tag
}

func goRunScript(name string, args ...string) error {
	cmd := exec.Command("go", append([]string{"run", filepath.Join("scripts", name)}, args...)...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("go run scripts/%s 失败: %w", name, err)
	}
	return nil
}

// ---------------- plugin.json ----------------

type artifact struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

func readPluginManifest(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	var manifest map[string]any
	if err := decoder.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", path, err)
	}
	return manifest, nil
}

func manifestArtifacts(manifest map[string]any) ([]artifact, error) {
	install, ok := manifest["install"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("plugin.json 缺少 install 段")
	}
	rawArtifacts, ok := install["artifacts"].([]any)
	if !ok || len(rawArtifacts) == 0 {
		return nil, fmt.Errorf("plugin.json 的 install.artifacts 为空")
	}
	artifacts := make([]artifact, 0, len(rawArtifacts))
	for _, raw := range rawArtifacts {
		entry, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("artifacts 条目不是对象")
		}
		item := artifact{}
		item.GOOS, _ = entry["goos"].(string)
		item.GOARCH, _ = entry["goarch"].(string)
		item.URL, _ = entry["url"].(string)
		item.SHA256, _ = entry["sha256"].(string)
		artifacts = append(artifacts, item)
	}
	return artifacts, nil
}

func manifestString(manifest map[string]any, key string) string {
	value, _ := manifest[key].(string)
	return value
}

// ---------------- 任务: pack ----------------

func runPack(args []string) error {
	fs := flag.NewFlagSet("pack", flag.ExitOnError)
	pluginID := fs.String("plugin", "", "插件 id (plugins/ 下的目录名)")
	version := fs.String("version", "", "覆盖版本号, 默认取 plugin.json")
	outDir := fs.String("out", "dist", "产物输出目录")
	wantGOOS := fs.String("goos", "", "断言本机 GOOS (CI 矩阵对齐用); 与 runtime.GOOS 不一致时失败")
	wantGOARCH := fs.String("goarch", "", "断言本机 GOARCH (CI 矩阵对齐用); 与 runtime.GOARCH 不一致时失败")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*pluginID) == "" {
		fs.Usage()
		return fmt.Errorf("必须指定 --plugin")
	}

	// c-shared 是 CGO 构建, 交叉编译需要目标平台 C 工具链, 只能为当前平台打包。
	// CI 矩阵按 runner 分发平台, 这里断言两者一致: 矩阵写错或 runner 漂移时立刻失败,
	// 而不是产出一个标着 A 平台、实际是 B 平台的产物。
	if want := strings.TrimSpace(*wantGOOS); want != "" && !strings.EqualFold(want, runtime.GOOS) {
		return fmt.Errorf("本机 GOOS 是 %s, 无法产出 %s 的产物; c-shared 不能交叉编译", runtime.GOOS, want)
	}
	if want := strings.TrimSpace(*wantGOARCH); want != "" && !strings.EqualFold(want, runtime.GOARCH) {
		return fmt.Errorf("本机 GOARCH 是 %s, 无法产出 %s 的产物; c-shared 不能交叉编译", runtime.GOARCH, want)
	}

	pluginDir := filepath.Join("plugins", *pluginID)
	manifestPath := filepath.Join(pluginDir, "plugin.json")
	manifest, err := readPluginManifest(manifestPath)
	if err != nil {
		return err
	}
	resolvedVersion := strings.TrimSpace(*version)
	if resolvedVersion == "" {
		resolvedVersion = manifestString(manifest, "version")
	}
	if resolvedVersion == "" {
		return fmt.Errorf("无法确定版本号, 请用 --version 指定")
	}

	goos, goarch := runtime.GOOS, runtime.GOARCH
	extension := platformExtension(goos)

	// 1. 构建动态库到临时目录, 文件名就是宿主期望的 <id><扩展名>
	buildDir, err := os.MkdirTemp("", "cpa-pack-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(buildDir)

	libraryPath := filepath.Join(buildDir, *pluginID+extension)
	buildCmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", libraryPath, ".")
	buildCmd.Dir = pluginDir
	buildCmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	buildCmd.Stdout, buildCmd.Stderr = os.Stdout, os.Stderr
	fmt.Printf("[*] 构建 %s/%s 动态库...\n", goos, goarch)
	if err := buildCmd.Run(); err != nil {
		return fmt.Errorf("构建失败: %w", err)
	}
	if _, err := os.Stat(libraryPath); err != nil {
		return fmt.Errorf("构建未产出 %s", libraryPath)
	}

	// 2. 打包: 包内只放一个根级动态库
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	assetName := archiveName(*pluginID, resolvedVersion, goos, goarch)
	assetPath := filepath.Join(*outDir, assetName)

	if err := writeZip(assetPath, libraryPath, *pluginID+extension); err != nil {
		return err
	}

	// 3. 读回自检, 用宿主的规则校验
	if err := verifyArchive(assetPath, *pluginID, resolvedVersion, goos); err != nil {
		return fmt.Errorf("产物自检失败: %w", err)
	}

	digest, size, err := fileDigest(assetPath)
	if err != nil {
		return err
	}
	fmt.Printf("[+] 产物: %s\n", assetPath)
	fmt.Printf("[+] 包内条目: %s%s\n", *pluginID, extension)
	fmt.Printf("[+] 平台: %s/%s\n", goos, goarch)
	fmt.Printf("[+] 大小: %d 字节\n", size)
	fmt.Printf("[+] sha256: %s\n", digest)
	fmt.Printf("\n[!] 提示: 其余平台的产物必须在对应平台上分别执行 pack, 无法交叉产出。\n")
	return nil
}

func writeZip(assetPath, libraryPath, entryName string) error {
	source, err := os.Open(libraryPath)
	if err != nil {
		return err
	}
	defer source.Close()

	info, err := source.Stat()
	if err != nil {
		return err
	}

	target, err := os.Create(assetPath)
	if err != nil {
		return err
	}
	defer target.Close()

	writer := zip.NewWriter(target)
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = entryName
	header.Method = zip.Deflate
	header.SetMode(0o755)

	entry, err := writer.CreateHeader(header)
	if err != nil {
		return err
	}
	if _, err := io.Copy(entry, source); err != nil {
		return err
	}
	return writer.Close()
}

// verifyArchive 按 host/internal/pluginstore 的 readTargetLibrary 规则校验
func verifyArchive(assetPath, pluginID, version, goos string) error {
	extension := platformExtension(goos)
	wanted := map[string]bool{
		pluginID + extension:                  true,
		pluginID + "-v" + version + extension: true,
	}

	reader, err := zip.OpenReader(assetPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	var found string
	for _, file := range reader.File {
		name := strings.TrimSpace(file.Name)
		if strings.Contains(name, `\`) {
			return fmt.Errorf("条目 %s 使用了反斜杠分隔符", name)
		}
		if strings.HasPrefix(name, "/") || strings.HasPrefix(name, "../") {
			return fmt.Errorf("条目 %s 逃逸出压缩包根目录", name)
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if !wanted[name] {
			if filepath.Base(name) == pluginID+extension {
				return fmt.Errorf("动态库必须在压缩包根级, 实际为 %s", name)
			}
			return fmt.Errorf("动态库命名必须是 %s%s 或 %s-v%s%s, 实际为 %s",
				pluginID, extension, pluginID, version, extension, name)
		}
		if found != "" {
			return fmt.Errorf("压缩包含多个目标动态库: %s 与 %s", found, name)
		}
		found = name
	}
	if found == "" {
		return fmt.Errorf("压缩包内没有 %s%s", pluginID, extension)
	}
	return nil
}

// ---------------- 任务: record ----------------

func runRecord(args []string) error {
	fs := flag.NewFlagSet("record", flag.ExitOnError)
	pluginID := fs.String("plugin", "", "插件 id")
	distDir := fs.String("dist", "dist", "产物目录")
	skipRegistry := fs.Bool("skip-registry", false, "只回填 plugin.json, 不重建 registry.json")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*pluginID) == "" {
		fs.Usage()
		return fmt.Errorf("必须指定 --plugin")
	}

	manifestPath := filepath.Join("plugins", *pluginID, "plugin.json")
	manifest, err := readPluginManifest(manifestPath)
	if err != nil {
		return err
	}
	version := manifestString(manifest, "version")
	if version == "" {
		return fmt.Errorf("plugin.json 缺少 version")
	}
	declared, err := manifestArtifacts(manifest)
	if err != nil {
		return err
	}
	seenPlatform := map[string]bool{}
	for _, item := range declared {
		key := item.GOOS + "/" + item.GOARCH
		if seenPlatform[key] {
			return fmt.Errorf("plugin.json 重复声明平台 %s", key)
		}
		seenPlatform[key] = true
	}

	// 仅回填 dist 中实际存在的产物, 缺哪个平台就保留原值并显式报告, 不猜测。
	install := manifest["install"].(map[string]any)
	rawArtifacts := install["artifacts"].([]any)

	filled, missing := 0, []string{}
	for _, raw := range rawArtifacts {
		entry, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("artifacts 条目不是对象")
		}
		goos, _ := entry["goos"].(string)
		goarch, _ := entry["goarch"].(string)
		assetPath := filepath.Join(*distDir, archiveName(*pluginID, version, goos, goarch))
		if _, err := os.Stat(assetPath); err != nil {
			missing = append(missing, fmt.Sprintf("%s/%s", goos, goarch))
			continue
		}
		digest, size, err := fileDigest(assetPath)
		if err != nil {
			return err
		}
		entry["sha256"] = digest
		entry["size"] = json.Number(fmt.Sprintf("%d", size))
		filled++
		fmt.Printf("[+] 回填 %s/%s: %s\n", goos, goarch, digest)
	}

	if filled == 0 {
		return fmt.Errorf("在 %s 中没有找到任何名为 %s 的产物", *distDir, archiveName(*pluginID, version, "goos", "goarch"))
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		fmt.Fprintf(os.Stderr, "[!] 以下平台缺少产物, 对应条目未回填: %s\n", strings.Join(missing, ", "))
	}

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("[+] 已更新 %s (字段顺序按字典序规范化)\n", manifestPath)

	if *skipRegistry {
		return nil
	}

	fmt.Println("[*] 重建 registry.json...")
	registryCmd := exec.Command("go", "run", "scripts/build-registry.go")
	registryCmd.Stdout, registryCmd.Stderr = os.Stdout, os.Stderr
	if err := registryCmd.Run(); err != nil {
		return fmt.Errorf("重建 registry.json 失败: %w", err)
	}
	return nil
}

// ---------------- 工具 ----------------

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()

	hasher := sha256.New()
	size, err := io.Copy(hasher, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), size, nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "用法: go run scripts/release.go <version|pack|record|publish> [选项]\n\n")
		fmt.Fprintf(os.Stderr, "  version --plugin <id> [--version <v>]\n")
		fmt.Fprintf(os.Stderr, "  pack    --plugin <id> [--version <v>] [--out dist]\n")
		fmt.Fprintf(os.Stderr, "  record  --plugin <id> [--dist dist] [--skip-registry]\n")
		fmt.Fprintf(os.Stderr, "  publish --plugin <id> [--version <v>] [--dry-run] [--no-push]\n")
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "version":
		err = runVersion(os.Args[2:])
	case "pack":
		err = runPack(os.Args[2:])
	case "record":
		err = runRecord(os.Args[2:])
	case "publish":
		err = runPublish(os.Args[2:])
	default:
		err = fmt.Errorf("未知子命令 %q", os.Args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n[-] %v\n", err)
		os.Exit(1)
	}
}
