package buildinfo

// Populated via -ldflags at build time. See Makefile.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)
