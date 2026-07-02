// Command report-portal is the entry point: it dispatches CLI subcommands and
// otherwise starts the HTTP server. The application core lives in internal/app.
package main

import (
	"fmt"
	"log"
	"os"

	"golang.org/x/crypto/bcrypt"

	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/app"
	"github.com/KKazuhaK/StockAnalysisPrediction-Report-Portal/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version": // report-portal version — print version/commit/build date
			fmt.Println(version.String())
			return
		case "hashpw": // report-portal hashpw <password> — bcrypt hash for config.yaml
			if len(os.Args) < 3 {
				fmt.Fprintln(os.Stderr, "usage: report-portal hashpw <password>")
				os.Exit(1)
			}
			h, err := bcrypt.GenerateFromPassword([]byte(os.Args[2]), 12)
			if err != nil {
				log.Fatal(err)
			}
			fmt.Println(string(h))
			return
		case "fetchnames": // report-portal fetchnames — fetch full A-share names to data/names.json
			n, path, err := app.FetchNames(configPath())
			if err != nil {
				log.Fatalf("fetch failed: %v", err)
			}
			fmt.Printf("wrote %s: %d\n", path, n)
			return
		case "adduser": // report-portal adduser <username> <password> [admin] — lockout fallback
			if len(os.Args) < 4 {
				log.Fatal("usage: report-portal adduser <username> <password> [admin]")
			}
			admin := len(os.Args) > 4 && os.Args[4] == "admin"
			if err := app.AddUser(configPath(), os.Args[2], os.Args[3], admin); err != nil {
				log.Fatal(err)
			}
			role := "user"
			if admin {
				role = "admin"
			}
			fmt.Printf("user saved: %s (role=%s)\n", os.Args[2], role)
			return
		case "import-legacy": // report-portal import-legacy — resumable one-shot pull of all legacy reports (incl. body) into the store, then old system can be retired
			imported, skipped, failed, failedIDs, err := app.RunLegacyImport(configPath(), log.Printf)
			fmt.Printf("legacy import: imported=%d skipped=%d failed=%d\n", imported, skipped, failed)
			if len(failedIDs) > 0 {
				fmt.Printf("failed ids (re-run to retry): %v\n", failedIDs)
			}
			if err != nil {
				log.Fatalf("legacy import stopped: %v", err)
			}
			return
		}
	}
	app.RunServer(configPath())
}

func configPath() string {
	p := os.Getenv("RP_CONFIG")
	if p == "" {
		p = "config.yaml"
	}
	return p
}
