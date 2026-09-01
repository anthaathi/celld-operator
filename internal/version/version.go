// Package version exposes the build-time version of the operator.
package version

// Version is stamped at build time via:
//
//	-ldflags "-X github.com/anthaathi/celld-deploy/internal/version.Version=v1.2.3"
var Version = "dev"

// UserAgent returns an HTTP User-Agent identifying this operator build.
func UserAgent(component string) string {
	return "celld-operator/" + Version + " (" + component + ")"
}
