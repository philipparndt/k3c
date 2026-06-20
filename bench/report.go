package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"os"
	"strings"
	"time"
)

//go:embed report.tmpl.html
var reportTemplate string

// writeReport renders a self-contained interactive HTML report from the store.
func writeReport(path string, recs []Record) error {
	aggs := aggregate(recs)
	data, err := json.Marshal(aggs)
	if err != nil {
		return err
	}
	envJSON, _ := json.Marshal(reportEnv())
	html := strings.Replace(reportTemplate, "/*DATA*/", string(data), 1)
	html = strings.Replace(html, "/*ENV*/", string(envJSON), 1)
	return os.WriteFile(path, []byte(html), 0o644)
}

func reportEnv() map[string]string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	chip, _ := runQ(ctx, "sysctl", "-n", "machdep.cpu.brand_string")
	ver, _ := runQ(ctx, "sw_vers", "-productVersion")
	build, _ := runQ(ctx, "sw_vers", "-buildVersion")
	macos := strings.TrimSpace(ver)
	if b := strings.TrimSpace(build); b != "" {
		macos += " (" + b + ")"
	}
	return map[string]string{
		"chip":      strings.TrimSpace(chip),
		"macos":     macos,
		"generated": time.Now().Format("2006-01-02 15:04"),
	}
}
