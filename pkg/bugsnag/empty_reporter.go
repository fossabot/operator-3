//go:build !debug

// Package bugsnag provides an interface to the bugsnag error reporting library
// This is an instance of a empty interface, designed to compile with release builds.
package bugsnag

func Init()   {}
func Notify() {}
