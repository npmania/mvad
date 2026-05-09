package main

import (
	"flag"
	"fmt"
	"os"
)

func usage() {
	fmt.Fprint(os.Stderr, `usage: mvad <command> [arguments]

The commands are:

	login       store a Mullvad account number
	relays      list relays
	connect     connect to a relay
	disconnect  disconnect
	status      print connection status
	version     print version
`)
	os.Exit(2)
}

func main() {
	flag.Usage = usage
	flag.Parse()
	if flag.NArg() == 0 {
		usage()
	}
	switch flag.Arg(0) {
	case "version":
		fmt.Println("mvad (devel)")
	default:
		fmt.Fprintf(os.Stderr, "mvad: unknown command %q\n", flag.Arg(0))
		os.Exit(2)
	}
}
