package vpn

import (
	"bufio"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minhan1/vmate-cli/lib/network"
)

var ErrorKeywords = []string{
	"No route to host",
	"TLS key negotiation failed",
	"Connection timed out",
	"Connection refused",
	"AUTH_FAILED",
	"Network unreachable",
	"Host is down",
	"Name or service not known",
	"VERIFY ERROR",
	"certificate verify failed",
	"Inactivity timeout",
	"Ping timeout",
	"Cannot open TUN/TAP dev",
	"write to TUN/TAP: Input/output error",
	"read: Connection reset by peer",
	"handshake failure",
	"fatal error",
	"process exiting",
	"killed",
}

func getArgs(fun string, filePath string) []string {
	if fun == "test" {
		return []string{
			"--config", filePath,
			"--route-noexec",
			"--ifconfig-noexec",
			"--nobind",
			"--auth-nocache",
		}
	}
	return []string{}
}

// killProcessTree kills the process and its children on Windows
func killProcessTree(pid int) {
	// /F = force, /T = tree (child processes), /PID = process id
	exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(pid)).Run()
}

func testVPN(ctx context.Context, dir string, timeoutSec int) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// Windows: ensure openvpn is in your System PATH
	cmd := exec.CommandContext(ctx, "openvpn", getArgs("test", dir)...)

	// REMOVED: Unix specific SysProcAttr
	// cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdPipe, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	cmd.Stderr = cmd.Stdout

	if err := cmd.Start(); err != nil {
		return false
	}

	resultChan := make(chan bool, 1)

	go func() {
		defer close(resultChan)
		scanner := bufio.NewScanner(stdPipe)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.Contains(line, "Initialization Sequence Completed") {
				select {
				case resultChan <- true:
				default:
				}
				return
			}
			for _, keyword := range ErrorKeywords {
				if strings.Contains(line, keyword) {
					select {
					case resultChan <- false:
					default:
					}
					return
				}
			}
		}
		select {
		case resultChan <- false:
		default:
		}
	}()

	var success bool
	select {
	case success = <-resultChan:
	case <-ctx.Done():
		success = false
	}

	// Cleanup: Use Windows taskkill instead of syscall.Kill
	if cmd.Process != nil {
		killProcessTree(cmd.Process.Pid)
	}

	return success
}

type VPN struct {
	Path    string
	Country string
}

func RunTest(ctx context.Context, paths []string, verbose bool, maxworkers int, limit int, timeout int, progressChan chan<- int) []VPN {
	succeedConfigs := []VPN{}
	limitCtx, cancelLimit := context.WithCancel(ctx)
	defer cancelLimit()

	sem := make(chan struct{}, maxworkers)
	var wg sync.WaitGroup
	var mu sync.Mutex
LOOP:
	for _, path := range paths {
		if limitCtx.Err() != nil {
			break
		}

		wg.Add(1)
		select {
		case sem <- struct{}{}:
		case <-limitCtx.Done():
			wg.Done()
			break LOOP
		}

		go func(p string) {
			defer wg.Done()
			defer func() { <-sem }()

			if progressChan != nil {
				defer func() { progressChan <- 1 }()
			}

			if limitCtx.Err() != nil {
				return
			}

			if testVPN(limitCtx, p, timeout) {
				mu.Lock()
				if len(succeedConfigs) < limit {
					c := network.GetLocation(p)
					succeedConfigs = append(succeedConfigs, VPN{
						Path:    p,
						Country: c,
					})
					if verbose {
						fmt.Printf("\n[SUCCESS] %s --- %s\n", p, c)
					}
					if len(succeedConfigs) >= limit {
						cancelLimit()
					}
				}
				mu.Unlock()
			} else {
				if verbose {
					fmt.Printf("\n[FAILED] %s\n", p)
				}
			}
		}(path)
	}

	wg.Wait()
	return succeedConfigs
}

func ConnectAndMonitor(ctx context.Context, configPath string, c string, preconnect *bool, verbose bool) error {
	cmd := exec.CommandContext(ctx, "openvpn", "--config", configPath)
	// REMOVED: Unix specific SysProcAttr
	// cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	stdPipe, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return err
	}

	successChan := make(chan bool, 1)
	errorChan := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(stdPipe)
		isConnected := false

		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())

			if !isConnected && strings.Contains(line, "Initialization Sequence Completed") {
				isConnected = true
				select {
				case successChan <- true:
				default:
				}
			}

			for _, keyword := range ErrorKeywords {
				if strings.Contains(line, keyword) {
					errorChan <- fmt.Errorf("error keyword found: %s", keyword)
					return
				}
			}
			if strings.Contains(line, "Restart pause") {
				errorChan <- fmt.Errorf("restart pause detected")
				return
			}

			if verbose {
				fmt.Println(line)
			}
		}
		errorChan <- fmt.Errorf("process exited unexpectedly")
	}()

	kill := func() {
		if cmd.Process != nil {
			killProcessTree(cmd.Process.Pid)
		}
	}

	select {
	case <-successChan:
		fmt.Println("Connected successfully to", c)
		*preconnect = false

	case <-time.After(5 * time.Second):
		kill()
		*preconnect = true
		return fmt.Errorf("connection timed out (exceeded 5s)")

	case err := <-errorChan:
		kill()
		*preconnect = true
		return err

	case <-ctx.Done():
		kill()
		return nil
	}

	select {
	case err := <-errorChan:
		kill()
		*preconnect = false
		return err

	case <-ctx.Done():
		kill()
		return nil
	}
}
