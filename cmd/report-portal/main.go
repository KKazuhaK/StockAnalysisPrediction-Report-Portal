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
		case "recompute-kinds": // report-portal recompute-kinds — re-derive every report's top-level kind after a taxonomy change
			n, err := app.RecomputeKinds(configPath())
			if err != nil {
				log.Fatalf("recompute-kinds failed: %v", err)
			}
			fmt.Printf("recompute-kinds: %d rows updated\n", n)
			return
		case "freeze-names": // report-portal freeze-names — snapshot each un-named report's current name onto its row so later renames never rewrite history
			n, err := app.FreezeReportNames(configPath())
			if err != nil {
				log.Fatalf("freeze-names failed: %v", err)
			}
			fmt.Printf("freeze-names: %d rows frozen\n", n)
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
