package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Plugin struct {
	ID           string      `json:"id"`
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Author       string      `json:"author"`
	Version      string      `json:"version"`
	Repository   string      `json:"repository,omitempty"`
	Homepage     string      `json:"homepage,omitempty"`
	License      string      `json:"license,omitempty"`
	Tags         []string    `json:"tags,omitempty"`
	AuthRequired bool        `json:"auth_required,omitempty"`
	Install      interface{} `json:"install,omitempty"`
}

type Registry struct {
	SchemaVersion int      `json:"schema_version"`
	Plugins       []Plugin `json:"plugins"`
}

func main() {
	checkMode := flag.Bool("check", false, "Check if registry.json is up to date without writing")
	outputPath := flag.String("output", "registry.json", "Output path for the generated registry.json")
	defaultRepo := flag.String("repo", "https://github.com/varcli/cpa-plugins", "Default repository URL for in-tree plugins")
	flag.Parse()

	pluginsMap := make(map[string]Plugin)

	// 1. Scan in-tree plugins
	pluginDirs, err := filepath.Glob("plugins/*/plugin.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning plugins: %v\n", err)
		os.Exit(1)
	}

	for _, path := range pluginDirs {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", path, err)
			os.Exit(1)
		}

		var p Plugin
		if err := json.Unmarshal(data, &p); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing JSON in %s: %v\n", path, err)
			os.Exit(1)
		}

		if strings.TrimSpace(p.ID) == "" {
			fmt.Fprintf(os.Stderr, "Error: plugin in %s has empty id\n", path)
			os.Exit(1)
		}

		if strings.TrimSpace(p.Repository) == "" {
			p.Repository = *defaultRepo
		}

		if _, exists := pluginsMap[p.ID]; exists {
			fmt.Fprintf(os.Stderr, "Error: duplicate plugin ID %q found in %s\n", p.ID, path)
			os.Exit(1)
		}
		pluginsMap[p.ID] = p
	}

	// 2. Scan external plugins
	externalFiles, err := filepath.Glob("external/*.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error scanning external plugins: %v\n", err)
		os.Exit(1)
	}

	for _, path := range externalFiles {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error reading %s: %v\n", path, err)
			os.Exit(1)
		}

		var p Plugin
		if err := json.Unmarshal(data, &p); err != nil {
			fmt.Fprintf(os.Stderr, "Error parsing JSON in %s: %v\n", path, err)
			os.Exit(1)
		}

		if strings.TrimSpace(p.ID) == "" {
			fmt.Fprintf(os.Stderr, "Error: external plugin in %s has empty id\n", path)
			os.Exit(1)
		}

		if _, exists := pluginsMap[p.ID]; exists {
			fmt.Fprintf(os.Stderr, "Error: duplicate plugin ID %q found in %s\n", p.ID, path)
			os.Exit(1)
		}
		pluginsMap[p.ID] = p
	}

	// 3. Assemble and sort
	plugins := make([]Plugin, 0, len(pluginsMap))
	for _, p := range pluginsMap {
		plugins = append(plugins, p)
	}
	sort.Slice(plugins, func(i, j int) bool {
		return plugins[i].ID < plugins[j].ID
	})

	schemaVersion := 1
	for _, p := range plugins {
		if p.Install != nil {
			schemaVersion = 2
			break
		}
	}

	registry := Registry{
		SchemaVersion: schemaVersion,
		Plugins:       plugins,
	}
	outputBytes, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error serializing registry: %v\n", err)
		os.Exit(1)
	}
	outputBytes = append(outputBytes, '\n')

	if *checkMode {
		existingBytes, err := os.ReadFile(*outputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Registry file %s does not exist or cannot be read: %v\n", *outputPath, err)
			os.Exit(1)
		}
		if string(existingBytes) != string(outputBytes) {
			fmt.Fprintf(os.Stderr, "Registry file %s is out of date. Run go run scripts/build-registry.go to update it.\n", *outputPath)
			os.Exit(1)
		}
		fmt.Println("Registry is up to date.")
		return
	}

	if err := os.WriteFile(*outputPath, outputBytes, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "Error writing %s: %v\n", *outputPath, err)
		os.Exit(1)
	}

	fmt.Printf("Successfully generated %s with %d plugins.\n", *outputPath, len(plugins))
}
