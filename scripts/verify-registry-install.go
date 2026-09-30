package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"debug/elf"
	"debug/macho"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Artifact struct {
	GOOS   string `json:"goos"`
	GOARCH string `json:"goarch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

type InstallPlan struct {
	Type      string     `json:"type"`
	Artifacts []Artifact `json:"artifacts"`
}

type Plugin struct {
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
	Author      string       `json:"author"`
	Version     string       `json:"version"`
	Repository  string       `json:"repository"`
	Install     *InstallPlan `json:"install,omitempty"`
}

type Registry struct {
	SchemaVersion int      `json:"schema_version"`
	Plugins       []Plugin `json:"plugins"`
}

func main() {
	defaultRegistryURL := "https://raw.githubusercontent.com/varcli/cpa-plugins/main/registry.json"

	registryFlag := flag.String("registry", defaultRegistryURL, "URL or local file path to registry.json")
	pluginFlag := flag.String("plugin", "", "Comma-separated plugin IDs to verify (e.g. workbuddy)")
	localFlag := flag.Bool("local", false, "Shortcut to verify against local registry.json")
	flag.Parse()

	registrySource := *registryFlag
	if *localFlag {
		registrySource = "registry.json"
	}

	// Collect requested plugin IDs from flag and positional arguments
	targetPlugins := make(map[string]bool)
	if *pluginFlag != "" {
		for _, id := range strings.Split(*pluginFlag, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				targetPlugins[id] = true
			}
		}
	}
	for _, arg := range flag.Args() {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			targetPlugins[arg] = true
		}
	}
	if len(targetPlugins) == 0 {
		fmt.Fprintf(os.Stderr, "错误: 脚本严禁默认下载/验证所有插件。必须明确指定要验证的插件 ID。\n用法: go run scripts/verify-registry-install.go [-local] <plugin-id>\n示例: go run scripts/verify-registry-install.go -local workbuddy\n")
		os.Exit(1)
	}

	reg, err := loadRegistry(registrySource)
	if err != nil {
		fmt.Fprintf(os.Stderr, "FAILED to load registry: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Loaded registry from: %s (schema_version=%d, total=%d plugins)\n",
		registrySource, reg.SchemaVersion, len(reg.Plugins))

	// Filter plugins to verify
	var pluginsToVerify []Plugin
	found := make(map[string]bool)
	for _, p := range reg.Plugins {
		if targetPlugins[p.ID] {
			pluginsToVerify = append(pluginsToVerify, p)
			found[p.ID] = true
		}
	}
	for id := range targetPlugins {
		if !found[id] {
			fmt.Fprintf(os.Stderr, "FAILED: requested plugin %q not found in registry\n", id)
			os.Exit(1)
		}
	}

	fmt.Printf("Verifying %d plugin(s)...\n\n", len(pluginsToVerify))

	client := &http.Client{Timeout: 60 * time.Second}
	failedCount := 0

	for idx, p := range pluginsToVerify {
		fmt.Printf("[%d/%d] Verifying plugin: %s (v%s)\n", idx+1, len(pluginsToVerify), p.ID, p.Version)

		if p.Install != nil && p.Install.Type == "direct" {
			if len(p.Install.Artifacts) == 0 {
				fmt.Fprintf(os.Stderr, "  FAILED: direct install plan declared but has no artifacts\n")
				failedCount++
				continue
			}
			fmt.Printf("  Install type: direct (%d artifact(s))\n", len(p.Install.Artifacts))
			for _, artifact := range p.Install.Artifacts {
				if err := verifyDirectArtifact(client, p.ID, artifact); err != nil {
					fmt.Fprintf(os.Stderr, "  FAILED for %s/%s: %v\n", artifact.GOOS, artifact.GOARCH, err)
					failedCount++
				} else {
					fmt.Printf("  Artifact %s/%s: PASSED (SHA256 verified, library format valid)\n", artifact.GOOS, artifact.GOARCH)
				}
			}
		} else {
			// External or repository reference
			fmt.Printf("  Install type: github-release / reference\n")
			if strings.TrimSpace(p.Repository) == "" {
				fmt.Fprintf(os.Stderr, "  FAILED: missing repository field\n")
				failedCount++
				continue
			}
			parsed, err := url.Parse(p.Repository)
			if err != nil || parsed.Host == "" {
				fmt.Fprintf(os.Stderr, "  FAILED: invalid repository URL %q\n", p.Repository)
				failedCount++
				continue
			}
			fmt.Printf("  Repository: %s (valid URL)\n", p.Repository)
		}
		fmt.Println()
	}

	if failedCount > 0 {
		fmt.Fprintf(os.Stderr, "VERIFICATION FAILED: %d error(s) encountered.\n", failedCount)
		os.Exit(1)
	}

	fmt.Println("ALL REQUESTED PLUGINS VERIFIED SUCCESSFULLY.")
}

func loadRegistry(source string) (Registry, error) {
	var body []byte
	if strings.HasPrefix(source, "http://") || strings.HasPrefix(source, "https://") {
		// 强制不使用 gzip 协商: raw.githubusercontent 对压缩与非压缩两个变体分别缓存,
		// 压缩变体更新滞后, 刚推送完会取到过期副本, 表现为清单里的哈希凭空消失, 看起来
		// 像真实缺陷。实测时间戳查询参数无法绕开该缓存, 只有 identity 变体是即时的。
		request, errRequest := http.NewRequest(http.MethodGet, source, nil)
		if errRequest != nil {
			return Registry{}, fmt.Errorf("build request: %w", errRequest)
		}
		request.Header.Set("Accept-Encoding", "identity")

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(request)
		if err != nil {
			return Registry{}, fmt.Errorf("fetch URL: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return Registry{}, fmt.Errorf("HTTP status %s", resp.Status)
		}
		body, err = io.ReadAll(resp.Body)
		if err != nil {
			return Registry{}, fmt.Errorf("read body: %w", err)
		}
	} else {
		var err error
		body, err = os.ReadFile(source)
		if err != nil {
			return Registry{}, fmt.Errorf("read file: %w", err)
		}
	}

	var reg Registry
	if err := json.Unmarshal(body, &reg); err != nil {
		return Registry{}, fmt.Errorf("parse JSON: %w", err)
	}
	return reg, nil
}

func verifyDirectArtifact(client *http.Client, pluginID string, artifact Artifact) error {
	if artifact.URL == "" {
		return fmt.Errorf("empty artifact URL")
	}
	if artifact.SHA256 == "" {
		return fmt.Errorf("empty artifact SHA256")
	}

	resp, err := client.Get(artifact.URL)
	if err != nil {
		return fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned HTTP status %s", resp.Status)
	}

	zipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read download: %w", err)
	}

	hash := sha256.Sum256(zipBytes)
	computedHash := hex.EncodeToString(hash[:])
	if !strings.EqualFold(computedHash, artifact.SHA256) {
		return fmt.Errorf("checksum mismatch: computed=%s, declared=%s", computedHash, artifact.SHA256)
	}

	zipReader, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	extension := platformExtension(artifact.GOOS)
	var libraryFile *zip.File
	for _, f := range zipReader.File {
		name := f.Name
		if !strings.HasSuffix(name, extension) {
			continue
		}
		// 宿主要求包内只有一个根级动态库, 名为 <id><扩展名> 或 <id>-v<版本><扩展名>
		if name == pluginID+extension || strings.HasPrefix(name, pluginID+"-v") {
			libraryFile = f
			break
		}
	}

	if libraryFile == nil {
		return fmt.Errorf("%s%s or %s-v<version>%s not found in zip archive", pluginID, extension, pluginID, extension)
	}

	libraryReader, err := libraryFile.Open()
	if err != nil {
		return fmt.Errorf("open library inside zip: %w", err)
	}
	defer libraryReader.Close()

	libraryBytes, err := io.ReadAll(libraryReader)
	if err != nil {
		return fmt.Errorf("read library inside zip: %w", err)
	}

	return verifyLibraryFormat(artifact.GOOS, artifact.GOARCH, libraryBytes)
}

// verifyLibraryFormat 按目标平台校验动态库格式, 与宿主的扩展名约定保持一致。
// 只支持单个平台产物会被漏掉 mac, 而本机正是 darwin, 因此两种格式都要认。
func verifyLibraryFormat(goos, goarch string, data []byte) error {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "linux":
		elfFile, err := elf.NewFile(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("invalid ELF format: %w", err)
		}
		defer elfFile.Close()

		if elfFile.Type != elf.ET_DYN {
			return fmt.Errorf("ELF type is %v, expected ET_DYN (shared library)", elfFile.Type)
		}

		var expectedMachine elf.Machine
		switch goarch {
		case "amd64":
			expectedMachine = elf.EM_X86_64
		case "arm64":
			expectedMachine = elf.EM_AARCH64
		default:
			expectedMachine = elf.EM_NONE
		}

		if elfFile.Machine != expectedMachine {
			return fmt.Errorf("ELF machine architecture mismatch: got %v, want %v", elfFile.Machine, expectedMachine)
		}
		return nil

	case "darwin":
		machoFile, err := macho.NewFile(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("invalid Mach-O format: %w (fat binary 需另行处理)", err)
		}
		defer machoFile.Close()

		if machoFile.Type != macho.TypeDylib {
			return fmt.Errorf("Mach-O type is %v, expected dylib", machoFile.Type)
		}

		var expectedCPU macho.Cpu
		switch goarch {
		case "amd64":
			expectedCPU = macho.CpuAmd64
		case "arm64":
			expectedCPU = macho.CpuArm64
		}

		if expectedCPU != 0 && machoFile.Cpu != expectedCPU {
			return fmt.Errorf("Mach-O CPU mismatch: got %v, want %v", machoFile.Cpu, expectedCPU)
		}
		return nil

	default:
		// 其它平台只校验哈希, 不做格式判定
		return nil
	}
}

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
