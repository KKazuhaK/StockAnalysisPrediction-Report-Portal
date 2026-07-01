package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
)

// htmlToPDF 用 wkhtmltopdf 把 HTML 渲染成 PDF（镜像内自带该二进制，零依赖旧门户）。
func htmlToPDF(html string) ([]byte, error) {
	bin := os.Getenv("WKHTMLTOPDF_BIN")
	if bin == "" {
		bin = "wkhtmltopdf"
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
			return nil, &pdfErr{err.Error() + ": " + errb.String()}
		}
		return nil, err
	}
	return out.Bytes(), nil
}

type pdfErr struct{ s string }

func (e *pdfErr) Error() string { return e.s }
