package main

import (
	"flag"
	"fmt"
	"io"
	"runtime"
	"runtime/debug"
	"strings"
)

// These values are set for canonical release builds. A binary installed with
// `go install module/cmd/acrelay@version` obtains its version from Go build
// metadata instead.
var (
	releaseVersion = "devel"
	releaseCommit  = "unknown"
)

type versionInfo struct {
	Version   string
	Commit    string
	GoVersion string
}

func currentVersionInfo() versionInfo {
	info := versionInfo{
		Version:   releaseVersion,
		Commit:    releaseCommit,
		GoVersion: runtime.Version(),
	}
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return info
	}
	if info.Version == "devel" && build.Main.Version != "" && build.Main.Version != "(devel)" {
		info.Version = build.Main.Version
	}
	if info.Commit == "unknown" {
		for _, setting := range build.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				info.Commit = setting.Value
				break
			}
		}
	}
	return info
}

func runVersion(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	short := fs.Bool("short", false, "print only the release version")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("version accepts only --short")
	}
	info := currentVersionInfo()
	if strings.TrimSpace(info.Version) == "" {
		return fmt.Errorf("release version is empty")
	}
	if *short {
		_, err := fmt.Fprintln(out, info.Version)
		return err
	}
	_, err := fmt.Fprintf(out, "acRelay %s\ncommit %s\ngo %s\n", info.Version, info.Commit, info.GoVersion)
	return err
}
