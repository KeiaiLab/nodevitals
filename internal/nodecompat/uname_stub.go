//go:build !linux

package nodecompat

import "errors"

// realUname stub for non-Linux platforms. nodevitals ships only as a
// linux/amd64 container (Makefile docker/gpu-check targets build under
// linux/amd64 explicitly), and unix.Utsname's shape is Linux-specific (it
// carries Domainname; most other platforms' struct utsname does not) — so
// this exists solely to keep `go build ./...` and this package's tests
// working on non-Linux dev machines. Mirrors
// internal/collector/smart_probe_stub.go. The real syscall lives in
// uname_linux.go.
func realUname() (unameInfo, error) {
	return unameInfo{}, errors.New("uname(2): unsupported on this platform")
}
