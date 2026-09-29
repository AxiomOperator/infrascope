//go:build !linux

package main

// dropPrivileges is only used by the Linux container image.
func dropPrivileges() {}
