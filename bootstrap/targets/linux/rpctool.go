package main

import (
	"flag"
	"fmt"

	log "github.com/Sirupsen/logrus"
	"github.com/vmware/vmw-guestinfo/rpcout"
	"github.com/vmware/vmw-guestinfo/rpcvmx"
	"github.com/vmware/vmw-guestinfo/vmcheck"
)

var (
	set  bool
	get  bool
	fork bool
)

func init() {
	flag.BoolVar(&set, "set", false, "Sets the guestinfo.KEY with the string VALUE")
	flag.BoolVar(&get, "get", false, "Returns the config string in the guestinfo.* namespace")
	flag.BoolVar(&fork, "fork", false, "VMFork")

	flag.Parse()
}

func main() {
	if !vmcheck.IsVirtualWorld() {
		log.Fatalf("ERROR: not in a virtual world.")
	}

	if !set && !get && !fork {
		flag.Usage()
	}

	config := rpcvmx.NewConfig()
	if set {
		if flag.NArg() != 2 {
			log.Fatalf("ERROR: Please provide guestinfo key / value pair (eg; -set foo bar")
		}
		if err := config.SetString(flag.Arg(0), flag.Arg(1)); err != nil {
			log.Fatalf("ERROR: SetString failed with %s", err)
		}
	}

	if get {
		if flag.NArg() != 1 {
			log.Fatalf("ERROR: Please provide guestinfo key (eg; -get foo)")
		}
		if out, err := config.String(flag.Arg(0), ""); err != nil {
			log.Fatalf("ERROR: String failed with %s", err)
		} else {
			fmt.Printf("%s\n", out)
		}
	}

	if fork {
		out, ok, err := rpcout.SendOne("vmfork-begin -1 -1")
		if err != nil {
			log.Fatalf("ERROR: %s | %s | %t", err, out, ok)
		} else if !ok {
			log.Fatalf("FAILED: %s", out)
		} else {
			fmt.Printf("%s\n", out)
		}
	}
}
