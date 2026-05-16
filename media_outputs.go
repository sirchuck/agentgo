package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var mediaRevisionDirPattern = regexp.MustCompile(`_r(\d{3})$`)

func (a *App) nextMediaProjectworkOutputRoot(projectName string, model ModelConfig) (string, string, error) {
	projectRoot, err := a.projectWorkRoot(projectName)
	if err != nil {
		return "", "", err
	}
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		return "", "", err
	}
	mediaFolder := "videos"
	if modelIsMeshGeneration(model) {
		mediaFolder = "3dmesh"
	}
	mediaRoot := filepath.Join(projectRoot, mediaFolder)
	if err := os.MkdirAll(mediaRoot, 0o755); err != nil {
		return "", "", err
	}
	base := modelSlug(model.Label)
	if base == "" || base == "model" {
		base = "model_" + modelIDString(model.ID)
	}
	next := 1
	entries, err := os.ReadDir(mediaRoot)
	if err != nil && !os.IsNotExist(err) {
		return "", "", err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimSpace(entry.Name())
		if !strings.HasPrefix(name, base+"_r") {
			continue
		}
		match := mediaRevisionDirPattern.FindStringSubmatch(name)
		if len(match) != 2 {
			continue
		}
		if n, err := strconv.Atoi(match[1]); err == nil && n >= next {
			next = n + 1
		}
	}
	for i := next; i < next+10000; i++ {
		folder := fmt.Sprintf("%s_r%03d", base, i)
		full := filepath.Join(mediaRoot, folder)
		if err := os.Mkdir(full, 0o755); err == nil {
			rel := filepath.ToSlash(filepath.Join("projects", projectName, "projectwork", mediaFolder, folder))
			return rel, full, nil
		} else if os.IsExist(err) {
			continue
		} else {
			return "", "", err
		}
	}
	return "", "", fmt.Errorf("could not allocate media output folder for %s", model.Label)
}

func writeProjectworkJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
