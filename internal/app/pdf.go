package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// ErrNoWkhtmltopdf indicates wkhtmltopdf is not installed on this machine (it is bundled in the Docker image).
var ErrNoWkhtmltopdf = errors.New("wkhtmltopdf 不可用")

var errPDFTooLarge = errors.New("generated PDF exceeds the 64 MiB limit")

const (
	pdfRenderTimeout = 90 * time.Second
	maxPDFBytes      = 64 << 20
	maxPDFStderr     = 64 << 10
)

// wkhtmltopdf is relatively expensive and the release container has a 256 MiB memory limit.
// Bound process fan-out globally so authenticated users cannot exhaust the host with exports.
var pdfRenderSlots = make(chan struct{}, 2)

type cappedBuffer struct {
	bytes.Buffer
	limit int
	err   error
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		b.err = errPDFTooLarge
		return 0, b.err
	}
	if len(p) > remaining {
		n, _ := b.Buffer.Write(p[:remaining])
		b.err = errPDFTooLarge
		return n, b.err
	}
	return b.Buffer.Write(p)
}

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

func htmlToPDFContext(ctx context.Context, html string) ([]byte, error) {
	bin := wkhtmltopdfBin()
	if bin == "" {
		return nil, ErrNoWkhtmltopdf
	}
	renderCtx, cancel := context.WithTimeout(ctx, pdfRenderTimeout)
	defer cancel()
	select {
	case pdfRenderSlots <- struct{}{}:
		defer func() { <-pdfRenderSlots }()
	case <-renderCtx.Done():
		return nil, fmt.Errorf("pdf render capacity: %w", renderCtx.Err())
	}

	// The report body is sanitized before it reaches this command. These switches are a second
	// boundary: no scripts, plugins, images, local files, or external links are needed to render
	// the portal's self-contained template.
	cmd := exec.CommandContext(renderCtx, bin, "-q", "--encoding", "utf-8",
		"--disable-javascript", "--disable-plugins", "--no-images", "--disable-external-links",
		"--disable-local-file-access",
		"--margin-top", "14mm", "--margin-bottom", "16mm",
		"--margin-left", "14mm", "--margin-right", "14mm",
		"-", "-") // stdin -> stdout
	cmd.Stdin = strings.NewReader(html)
	out := &cappedBuffer{limit: maxPDFBytes}
	errout := &cappedBuffer{limit: maxPDFStderr}
	cmd.Stdout = out
	cmd.Stderr = errout
	if err := cmd.Run(); err != nil {
		if out.err != nil {
			return nil, out.err
		}
		if renderCtx.Err() != nil {
			return nil, fmt.Errorf("pdf render: %w", renderCtx.Err())
		}
		if errout.Len() > 0 {
			return nil, errors.New(err.Error() + ": " + errout.String())
		}
		return nil, err
	}
	return out.Bytes(), nil
}
