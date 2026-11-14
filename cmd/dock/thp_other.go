//go:build !linux

package main

// withoutTHP does nothing off Linux (thp_linux.go).
func withoutTHP() {}
