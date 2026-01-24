package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/minhan1/vmate-cli/lib/fileUtil"
	"github.com/minhan1/vmate-cli/lib/network"
	"github.com/minhan1/vmate-cli/lib/vpn"

	"github.com/spf13/cobra"
)

var (
	dir        string
	limit      int
	timeout    int
	maxworkers int
	verbose    bool
	recent     bool
	modify     bool
	connect    string
)

var rootCmd = &cobra.Command{
	Use:   "vmate-cli",
	Short: "VPN config tester",
	Long:  `A tool to test and manage VPN configurations.`,
	Run: func(cmd *cobra.Command, args []string) {
		if recent {
			if checkIncompatibleFlags("recent", false) {
				return
			}
			// Fixed: fileUtil -> fileUtil
			vpns, err := fileUtil.OpenText()
			if err != nil {
				return
			}
			fmt.Println("Recently used VPN configurations:")
			fmt.Println("-----------------------------------")
			for _, vpn := range vpns {
				fmt.Println(vpn.Path + " | " + vpn.Country)
			}
			return
		}
		if connect != "" {
			expandedPath, _ := expandPath(dir)
			reconnect := false
			Proconnect := &reconnect
			if checkIncompatibleFlags("connect", true) {
				return
			}
			ensureAdminPrivileges(expandedPath, verbose, maxworkers, limit, timeout, modify, connect)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			defer exec.Command("taskkill", "/F", "/IM", "openvpn.exe").Run()

			loopcount := 0
			currentConfig := connect
			pCurrentConfig := &currentConfig
			for {
				loopcount++
				fmt.Println("connecting to :", filepath.Base(currentConfig))
				c := network.GetLocation(currentConfig)

				err := vpn.ConnectAndMonitor(ctx, currentConfig, c, Proconnect, verbose)
				fmt.Println(reconnect, " after getting back from func")
				if ctx.Err() != nil {
					fmt.Println("Operation cancelled by user.")
					return
				}
				if err != nil {
					fmt.Println("Reconnecting")
					if !reconnect {
						reconnect = true
						exec.Command("taskkill", "/F", "/IM", "openvpn.exe").Run()
						continue
					}
					if reconnect {
						fmt.Println("In the reconnect attempt")
						reconnect = false

						// Fixed: fileUtil -> fileUtil
						vpns, err := fileUtil.OpenText()
						if err != nil {
							return
						}
						if len(vpns) == 1 {
							fmt.Println("There's no saved config in your recent")
							return
						}
						// Fixed: vpn.Vpn -> vpn.VPN (Case sensitivity)
						fileFiltered := slices.DeleteFunc(vpns, func(s vpn.VPN) bool {
							return s.Path == strings.TrimSpace(currentConfig)

						})
						// Fixed: failFiltered -> fileFiltered (Typo), fileUtil -> fileUtil
						_, err = fileUtil.SaveAsText(fileFiltered)
						if err != nil {
							fmt.Println("Save failed")
							return
						}

						// Fixed: failFiltered -> fileFiltered
						if len(fileFiltered) == 0 {
							fmt.Println("There's no saved config in your recent")
							return
						}
						if len(fileFiltered) > 0 {
							// Fixed: failFiltered -> fileFiltered, Index [1] -> [0] to avoid panic if len is 1
							newConfig := fileFiltered[0].Path
							*pCurrentConfig = newConfig
							fmt.Println("New Config inserted", filepath.Base(currentConfig))
							continue
						}
					}
					return
				}
			}
		}

		expandedPath, _ := expandPath(dir)
		ensureAdminPrivileges(expandedPath, verbose, maxworkers, limit, timeout, modify, connect)

		// Fixed: fileUtil -> fileUtil
		paths, err := fileUtil.GetConfigs(expandedPath)

		if modify {
			fmt.Println("Modifying!!")
			// Fixed: fileUtil -> fileUtil
			fileUtil.ModifyConfigs(paths)
		}

		if err != nil {
			fmt.Println("Error reading configs:", err)
			return
		}

		if maxworkers > len(paths) {
			maxworkers = len(paths)
		}

		// 1. Setup Signal Handling
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()

		// Safety net: Force kill openvpn on exit
		defer exec.Command("taskkill", "/F", "/IM", "openvpn.exe").Run()

		// 2. Setup Progress Bar Channel
		progressChan := make(chan int, len(paths))

		if !verbose {
			fmt.Printf("Testing %d configs with %d workers (Limit: %d success)\n", len(paths), maxworkers, limit)
			go runProgressBar(len(paths), progressChan)
		}

		// 3. Run the Tests
		succeedConfigs := vpn.RunTest(ctx, paths, verbose, maxworkers, limit, timeout, progressChan)

		close(progressChan)

		// 4. Output Results
		fmt.Println("\n\n--- Final Result ---")
		for _, config := range succeedConfigs {
			fmt.Printf("%s -- %s\n", config.Path, config.Country)
		}
		fmt.Printf("Found: %d / Scanned: %d\n", len(succeedConfigs), len(paths))
		// Fixed: fileUtil -> fileUtil
		status, err := fileUtil.SaveAsText(succeedConfigs)
		if err != nil {
			fmt.Println("Can't create the file")
		}
		if status {
			fmt.Println("Saved to your history access via --recent or -r flag")
		}
	},
}

func runProgressBar(total int, updates <-chan int) {
	current := 0
	for range updates {
		current++
		percent := float64(current) / float64(total) * 100
		barLen := 40
		filled := int((float64(current) / float64(total)) * float64(barLen))
		bar := strings.Repeat("#", filled) + strings.Repeat("-", barLen-filled)
		fmt.Printf("\r[%s] %.1f%% (%d/%d)", bar, percent, current, total)
		if current == total {
			break
		}
	}
	fmt.Print("\n")
}

func Execute() {
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.Version = "beta-0.0.2a"
	// Update: Changed default to ~/Downloads
	rootCmd.PersistentFlags().StringVarP(&dir, "dir", "d", "~/Downloads", "The ovpn files' dir")
	rootCmd.PersistentFlags().IntVarP(&limit, "limit", "l", 100, "Limit the amount of succeed ovpn to find")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "", false, "To get more output")
	rootCmd.PersistentFlags().IntVarP(&timeout, "timeout", "t", 15, "The time given to each test process")
	rootCmd.PersistentFlags().IntVarP(&maxworkers, "max", "m", 200, "The max processes allowed per session")
	rootCmd.PersistentFlags().BoolVarP(&recent, "recent", "r", false, "To access the recent")
	rootCmd.PersistentFlags().BoolVarP(&modify, "modify", "", false, "To modify wrong cipher of the configs")
	rootCmd.PersistentFlags().StringVarP(&connect, "connect", "c", "", "To connect to a config")
}

// ensureAdminPrivileges checks if we are admin, if not relaunches via PowerShell "runas"
func ensureAdminPrivileges(expandedDir string, verbose bool, maxworkers int, limit int, timeout int, modify bool, connect string) {
	if isAdmin() {
		return
	}

	// Reconstruct arguments
	exe, err := os.Executable()
	if err != nil {
		fmt.Println("Error getting executable path")
		os.Exit(1)
	}

	// Build arguments string for PowerShell
	var args []string
	if expandedDir != "" {
		args = append(args, "--dir", fmt.Sprintf("'%s'", expandedDir))
	}
	if verbose {
		args = append(args, "--verbose", "true")
	}
	if maxworkers != 200 {
		args = append(args, "--max", strconv.Itoa(maxworkers))
	}
	if limit != 100 {
		args = append(args, "--limit", strconv.Itoa(limit))
	}
	if timeout != 15 {
		args = append(args, "--timeout", strconv.Itoa(timeout))
	}
	if modify {
		args = append(args, "--modify", "true")
	}
	if connect != "" {
		args = append(args, "--connect", fmt.Sprintf("'%s'", connect))
	}

	argString := strings.Join(args, " ")

	// Use PowerShell to elevate
	fmt.Println("Relaunching as Administrator...")
	cmd := exec.Command("powershell", "Start-Process", fmt.Sprintf("'%s'", exe), "-Verb", "RunAs", "-ArgumentList", fmt.Sprintf("'%s'", argString))
	err = cmd.Run()
	if err != nil {
		fmt.Println("Failed to launch as Admin:", err)
	}
	os.Exit(0)
}

// Simple check for Admin privileges on Windows by attempting to open a physical drive
func isAdmin() bool {
	_, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	return err == nil
}

func expandPath(path string) (string, error) {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("could not get home directory: %w", err)
		}
		// Handle both slash types
		path = strings.TrimPrefix(path, "~")
		path = strings.TrimPrefix(path, "/")
		path = strings.TrimPrefix(path, "\\")
		return filepath.Join(homeDir, path), nil
	}
	return path, nil
}

func checkIncompatibleFlags(current string, verboseAllow bool) bool {
	conditions := []bool{
		dir != "~/Downloads", // Updated check to match new default
		verbose,
		maxworkers != 200,
		limit != 100,
		timeout != 15,
		recent,
	}
	var totalFlags int
	if verboseAllow && verbose {
		totalFlags = -1
	} else {
		totalFlags = 0
	}

	for _, active := range conditions {
		if active {
			totalFlags++
		}
	}

	if totalFlags > 1 {
		fmt.Println("You can only use", current, "flag as a single flag")
		return true
	}
	return false
}
