package app

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// ErrNoWkhtmltopdf indicates wkhtmltopdf is not installed on this machine (it is bundled in the Docker image).
var ErrNoWkhtmltopdf = errors.New("wkhtmltopdf 不可用")

// wkhtmltopdfBin locates wkhtmltopdf: environment variable → PATH → common install paths.
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

// htmlToPDF renders HTML to PDF using wkhtmltopdf (bundled in the image; no dependency on the legacy portal).
func htmlToPDF(html string) ([]byte, error) {
	bin := wkhtmltopdfBin()
	if bin == "" {
		return nil, ErrNoWkhtmltopdf
	}
	// --disable-local-file-access blocks a report body from reading server files via
	// file:// / relative paths (SSRF + local-file disclosure at export time); the input HTML is
	// externally ingested, so it is untrusted. Kept minimal so legitimate remote images still render.
	cmd := exec.Command(bin, "-q", "--encoding", "utf-8",
		"--disable-local-file-access",
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
