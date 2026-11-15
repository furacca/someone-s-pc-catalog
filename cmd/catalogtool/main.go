// Command catalogtool builds, inspects, compares and verifies prepared catalog
// archives.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "build":
		err = runBuild(os.Args[2:])
	case "canonicalize":
		err = runCanonicalize(os.Args[2:])
	case "inspect":
		err = runInspect(os.Args[2:])
	case "diff":
		err = runDiff(os.Args[2:])
	case "fetch":
		err = runFetch(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "catalogtool: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "catalogtool: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `Usage: catalogtool <command> [flags]

Commands:
  build     Compile a source snapshot and generated JSON into one archive
  canonicalize Normalize generated entity order before preserving the snapshot
  inspect   Validate an archive and report its identity and coverage
  diff      Compare two valid archives
  fetch     Verify a lock and stage the artifact it identifies

Run "catalogtool <command> -h" for the flags of one command.
`)
}
