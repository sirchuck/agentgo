package main

import (
	"os"
	"strings"
	"testing"
)

func TestR461WorkModeBottomPolish(t *testing.T) {
	data, err := os.ReadFile("templates/index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(data)

	if !strings.Contains(html, ".work-mode-stage{max-width:1180px;width:100%;height:calc(100vh - 103px)") {
		t.Fatal("r461 Work Mode stage height polish is missing")
	}
	if strings.Contains(html, "height:calc(100vh - 112px)") {
		t.Fatal("legacy desktop Work Mode stage height still present")
	}
}
