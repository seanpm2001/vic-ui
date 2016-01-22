// +build linux
package main

import (
	"crypto/rand"
	"flag"
	"io/ioutil"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"

	"enatai-gerrit.eng.vmware.com/bonneville-container/tether"
	"enatai-gerrit.eng.vmware.com/bonneville-container/tether/serial"
)

var (
	tetherKey string
	hup       chan os.Signal

	// Set of child PIDs created by us.
	childPidTable = make(map[int]chan syscall.WaitStatus)
	// Exclusive access to childPidTable
	childPidTableMutex = &sync.Mutex{}
)

type GlobalHandler struct {
	id string
}

type SessionHandler struct {
	*GlobalHandler
	// TODO: add some locking in here if non exec requests can touch exec or shell related items
	cmd       *exec.Cmd
	channel   *ssh.Channel
	env       map[string]string
	assignTty bool
	pty       *os.File
	waitGroup sync.WaitGroup
	pendingFn func()
}

func privateKey(file string) ssh.Signer {
	privateBytes, err := ioutil.ReadFile(file)
	if err != nil {
		log.Fatalf("failed to load private key: %v", tetherKey)
	}

	private, err := ssh.ParsePrivateKey(privateBytes)
	if err != nil {
		log.Fatalf("failed to parse private key: %v", tetherKey)
	}
	return private
}

// Hopefully this gets rolled into go.sys at some point
func Mkdev(majorNumber int64, minorNumber int64) int {
	return int((majorNumber << 8) | (minorNumber & 0xff) | ((minorNumber & 0xfff00) << 12))
}

func init() {
	flag.StringVar(&tetherKey, "key", "/.tether-init/init_key", "tetherd control channel private key")
}

// load the ID from the file system
func id() string {
	id, err := ioutil.ReadFile("/.tether-init/docker-id")
	if err != nil {
		log.Fatalf("failed to read ID from file: %s", err)
	}

	// strip any trailing garbage
	if len(id) > 64 {
		return string(id[:64])
	}
	return string(id)
}

func childReaper() {
	var incoming = make(chan os.Signal, 10)
	signal.Notify(incoming, syscall.SIGCHLD)

	for _ = range incoming {
		var status syscall.WaitStatus

		// reap until no more children to process
		for {
			log.Printf("Reaping child processes")
			pid, err := syscall.Wait4(-1, &status, syscall.WNOHANG, nil)
			if pid == 0 || err == syscall.ECHILD {
				log.Printf("No more child processes to reap")
				break
			}
			if err == nil {
				log.Printf("Reaped process %d, return code: %d\n", pid, status.ExitStatus())
				// Send this status over to exec_and_wait if this process was
				// created by us with 'exec'.
				var statusChan chan syscall.WaitStatus
				childPidTableMutex.Lock()
				statusChan, ok := childPidTable[pid]
				childPidTableMutex.Unlock()
				if ok {
					// This is one of our children. Send the status through
					// the channel, which has a bufferring capacity of 1, so
					// we won't block here
					statusChan <- status
				} else {
					// This is an adopted zombie. The Wait4 call
					// already clean it up from the kernel
					log.Printf("Reaped zombie process PID %d\n", pid)
				}
			} else {
				log.Printf("Wait4 got error: %v\n", err)
			}
		}
	}
}

func main() {
	// seems necessary given rand.Reader access
	var err error

	flag.Parse()

	go func() {
		log.Println(http.ListenAndServe("0.0.0.0:6060", nil))
	}()

	// make an access to urandom
	if err = syscall.Mknod("/.tether/urandom", syscall.S_IFCHR|uint32(os.FileMode(0444)), Mkdev(1, 9)); err != nil {
		log.Printf("failed to create urandom access: %s", err)
		return
	}

	if rand.Reader, err = os.Open("/.tether/urandom"); err != nil {
		log.Printf("failed to open new urandom device")
		return
	}

	// Parse ssh private key
	private := privateKey(tetherKey)

	// HACK: workaround file descriptor conflict in pipe2 return from the exec.Command.Start
	_, _, _ = os.Pipe()

	// TODO: abstract connection into driver
	// Bind to com1
	if err := syscall.Mknod("/.tether/ttyS0", syscall.S_IFCHR|uint32(os.FileMode(0660)), Mkdev(4, 64)); err != nil {
		log.Printf("failed to create device for com1: %s", err)
		return
	}

	// if we're the init process, start reaping children
	pid := os.Getpid()
	if pid == 1 {
		go childReaper()
	}

	// register the signal handling that we use to determine when the tether should initialize runtime data
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	syscall.Kill(pid, syscall.SIGHUP)

	for {
		// block until HUP
		log.Printf("Waiting for HUP signal - blocking tether initialization")
		<-hup
		log.Printf("Received HUP signal - initializing tether")

		log.Println("opening ttyS0")
		f, err := os.OpenFile("/.tether/ttyS0", os.O_RDWR|os.O_SYNC|syscall.O_NOCTTY, 777)
		if err != nil {
			log.Printf("failed to open com1 for ssh server: %s", err)
			return
		}
		defer f.Close()

		log.Printf("creating raw connection from ttyS0 (fd=%d)\n", f.Fd())
		conn, err := serial.NewFileConn(f)

		if err != nil {
			log.Printf("failed to create raw connection from ttyS0 file handle: %s", err)
			return
		}

		// Delete ourselves from the filesystem so we're not polluting the container
		// TODO: the deletion routine should be a closure passed to tether as we don't know what filesystem
		// access is still needed for basic setup

		handler := GlobalHandler{
			id: id(),
		}

		// HACK: currently RawConn dosn't implement timeout
		serial.HandshakeServer(conn, time.Duration(10*time.Second))

		log.Println("creating ssh connection over ttyS0")
		tether.StartTether(conn, private, &handler)

		f.Close()
	}
}
