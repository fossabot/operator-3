//go:build debug

// Package bugsnag provides an interface to the bugsnag error reporting library
// This is an instance of a implemented interface for non-release builds
// As more bugsnag functionality is required, the two
package bugsnag

import (
	"fmt"
	"os"

	bs "github.com/bugsnag/bugsnag-go/v2"
)

// Init configures the application with bugsnag features
// and connects the library to bugsnag's api with a secret token
// Should be called at startup.
func Init() {
	t := os.Getenv("BUGSNAG_API_TOKEN")
	if len(t) == 0 {
		panic("BUGSNAG_API_TOKEN not set in environment")
	} else {
		fmt.Println("Initializing Bugsnag")
		bs.Configure(bs.Configuration{
			APIKey:          t,
			ReleaseStage:    "development",
			ProjectPackages: []string{"main", "github.com/greymatter-io/operator/pkg"},
		})
	}
}

//Notify shadows bugsnag's Notify function which sends an error to the api.
//https://godoc.org/github.com/bugsnag/bugsnag-go/v2/#Notify
func Notify(err error, rawData ...interface{}) error {
	return bs.Notify(err, rawData)
}
