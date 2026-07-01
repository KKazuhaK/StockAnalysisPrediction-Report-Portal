package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ErrNoWkhtmltopdf 本机未装 wkhtmltopdf（Docker 镜像内置）。
var ErrNoWkhtmltopdf = errors.New("wkhtmltopdf 不可用")

// wkhtmltopdfBin 找 wkhtmltopdf：环境变量 → PATH → 常见安装路径。
func wkhtmltopdfBin() string {
	if b := os.Getenv("WKHTMLTOPDF_BIN"); b != "" {
		return b
	}
	if p, err := exec.LookPath("wkhtmltopdf"); err == nil {
		return p
	}
	for _, c := range []string{
		"/usr/local/bin/wkhtmltopdf", "/opt/homebrew/bin/wkhtmltopdf", "/usr/bin/wkhtmltopdf",
	} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// htmlToPDF 用 wkhtmltopdf 把 HTML 渲染成 PDF（镜像内自带，零依赖旧门户）。
func htmlToPDF(html string) ([]byte, error) {
	bin := wkhtmltopdfBin()
	if bin == "" {
		return nil, ErrNoWkhtmltopdf
	}
	cmd := exec.Command(bin, "-q", "--encoding", "utf-8",
		"--margin-top", "14mm", "--margin-bottom", "16mm",
		"--margin-left", "14mm", "--margin-right", "14mm",
		"-", "-") // stdin -> stdout
	cmd.Stdin = strings.NewReader(html)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if errb.Len() > 0 {
			return nil, errors.New(err.Error() + ": " + errb.String())
		}
		return nil, err
	}
	return out.Bytes(), nil
}
