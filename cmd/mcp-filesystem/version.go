package main

import "github.com/michaelquigley/push/build"

func init() {
	// advertise the dev base for unstamped developer builds; stamped release
	// builds (goreleaser) and stamped CI builds (push ci/ldflags.sh) override
	// build.Version/Hash/Date/Builder/Branch via ldflags.
	build.DevVersion = "v0.1.x"
	rootCmd.AddCommand(build.NewVersionCmd("mcp-filesystem"))
}
