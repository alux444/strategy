package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func parseOptions(args []string) map[string]string {
	options := make(map[string]string)
	var positional string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.TrimSpace(arg) == "" {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			normalized := strings.TrimPrefix(arg, "--")
			parts := strings.SplitN(normalized, "=", 2)
			key := strings.ToLower(strings.TrimSpace(parts[0]))
			if len(parts) == 2 {
				options[key] = parts[1]
			} else {
				if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
					options[key] = args[i+1]
					i++
				} else {
					options[key] = "true"
				}
			}
		} else if positional == "" {
			positional = arg
		}
	}
	options["positional"] = positional
	return options
}

func firstNonBlank(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func joinTickers(tickers []string) string {
	cleaned := make([]string, 0, len(tickers))
	for _, ticker := range tickers {
		if strings.TrimSpace(ticker) != "" {
			cleaned = append(cleaned, strings.ToUpper(strings.TrimSpace(ticker)))
		}
	}
	return strings.Join(cleaned, ",")
}

func parseTickers(tickersArg string) []string {
	parts := strings.Split(tickersArg, ",")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		ticker := strings.ToUpper(strings.TrimSpace(part))
		if ticker != "" {
			result = append(result, ticker)
		}
	}
	return result
}

func loadSettings(settingsPath string) settings {
	resolvedPath := settingsPath
	if resolvedPath == "" {
		resolvedPath = defaultSettingsFile
	}
	resolvedPath = filepath.Clean(resolvedPath)
	loaded := settings{SettingsFile: resolvedPath}
	content, err := os.ReadFile(resolvedPath)
	if err != nil {
		return loaded
	}
	if err := json.Unmarshal(content, &loaded); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to parse settings file %s: %v\n", resolvedPath, err)
		loaded.SettingsFile = resolvedPath
		return loaded
	}
	loaded.SettingsFile = resolvedPath
	return loaded
}
