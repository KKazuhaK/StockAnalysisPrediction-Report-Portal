// Command report-portal is the entry point: it dispatches CLI subcommands and
// otherwise starts the HTTP server. The application core lives in internal/app.
package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"

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
		case "backup": // report-portal backup <file|-> — dump the whole database to a portable file
			path := "-"
			if len(os.Args) > 2 {
				path = os.Args[2]
			}
			tables, rows, err := app.Backup(configPath(), path)
			if err != nil {
				log.Fatalf("backup failed: %v", err)
			}
			// To stderr: stdout may be the dump itself.
			fmt.Fprintf(os.Stderr, "backup: %d tables, %d rows -> %s\n", tables, rows, path)
			return
		case "restore": // report-portal restore <file|-> [--force] — load a dump, replacing everything
			if len(os.Args) < 3 {
				log.Fatal("usage: report-portal restore <file|-> [--force]   (without --force it is a dry run)")
			}
			force := len(os.Args) > 3 && os.Args[3] == "--force"
			rep, err := app.Restore(configPath(), os.Args[2], force)
			if err != nil {
				log.Fatalf("restore failed: %v", err)
			}
			printRestore(rep, force)
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

// printRestore reports what happened, or — without --force — what would have. The dry run is the
// default on purpose: restore replaces every table, and that should never be one typo away.
func printRestore(rep *app.RestoreReport, force bool) {
	verb := "would load"
	if force {
		verb = "loaded"
	}
	fmt.Printf("backup written %s by %s (%s)\n", rep.Header.CreatedAt, rep.Header.AppVersion, rep.Header.Driver)
	tables := make([]string, 0, len(rep.Rows))
	for t := range rep.Rows {
		tables = append(tables, t)
	}
	sort.Strings(tables)
	for _, t := range tables {
		line := fmt.Sprintf("  %-24s %8d", t, rep.Rows[t])
		if n := rep.Existing[t]; n > 0 {
			line += fmt.Sprintf("   (replaces %d)", n)
		}
		if cols := rep.SkipCols[t]; len(cols) > 0 {
			line += "   [not in the backup, left at default: " + strings.Join(cols, ", ") + "]"
		}
		fmt.Println(line)
	}
	fmt.Printf("%s %d rows into %d of the backup's %d tables\n", verb, rep.Total, len(rep.Rows), len(rep.Header.Tables))
	if force {
		fmt.Printf("restore complete; %d existing rows were replaced\n", rep.Replaced)
		return
	}
	fmt.Printf("DRY RUN — nothing was written. This would DELETE %d existing rows.\n", rep.Replaced)
	fmt.Println("Re-run with --force to apply, after stopping the portal.")
}

func configPath() string {
	p := os.Getenv("RP_CONFIG")
	if p == "" {
		p = "config.yaml"
	}
	return p
}
