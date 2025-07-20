package daemoncheck

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/chainguard-dev/clog"
	"github.com/spf13/cobra"
)

type Config struct {
	Start          string
	Setup          string
	Post           string
	Timeout        int
	ExpectedOutput string
	ErrorStrings   string
}

type DaemonChecker struct {
	config        Config
	tempDir       string
	outputFile    string
	daemonCmd     *exec.Cmd
	daemonPid     int
	expectedLines []string
	errorLines    []string
}

func NewDaemonChecker(config Config) (*DaemonChecker, error) {
	tempDir, err := os.MkdirTemp("", "daemon-check-output-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	dc := &DaemonChecker{
		config:     config,
		tempDir:    tempDir,
		outputFile: filepath.Join(tempDir, "output.log"),
	}

	// Parse expected output lines
	if config.ExpectedOutput != "" {
		dc.expectedLines = strings.Split(strings.TrimSpace(config.ExpectedOutput), "\n")
		// Remove empty lines
		filtered := make([]string, 0, len(dc.expectedLines))
		for _, line := range dc.expectedLines {
			if strings.TrimSpace(line) != "" {
				filtered = append(filtered, strings.TrimSpace(line))
			}
		}
		dc.expectedLines = filtered
	}

	// Parse error strings
	if config.ErrorStrings != "" {
		dc.errorLines = strings.Split(strings.TrimSpace(config.ErrorStrings), "\n")
		// Remove empty lines
		filtered := make([]string, 0, len(dc.errorLines))
		for _, line := range dc.errorLines {
			if strings.TrimSpace(line) != "" {
				filtered = append(filtered, strings.TrimSpace(line))
			}
		}
		dc.errorLines = filtered
	}

	return dc, nil
}

func (dc *DaemonChecker) Cleanup() {
	if dc.daemonCmd != nil && dc.daemonCmd.Process != nil {
		dc.terminateProcess(dc.daemonCmd.Process.Pid, 0, 30)
	}
	if dc.tempDir != "" {
		os.RemoveAll(dc.tempDir)
	}
}

func (dc *DaemonChecker) runScript(script string) error {
	if script == "" {
		return nil
	}

	scriptFile := filepath.Join(dc.tempDir, "script.sh")
	
	// Check if script has shebang
	shebang := "#!/bin/sh -ex\n"
	if strings.HasPrefix(script, "#!") {
		shebang = ""
	}
	
	content := shebang + script
	if err := os.WriteFile(scriptFile, []byte(content), 0755); err != nil {
		return fmt.Errorf("failed to write script: %w", err)
	}

	cmd := exec.Command("/bin/sh", scriptFile)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	
	return cmd.Run()
}

func (dc *DaemonChecker) startDaemon() error {
	args := strings.Fields(dc.config.Start)
	if len(args) == 0 {
		return fmt.Errorf("start command cannot be empty")
	}

	// Create output file
	outFile, err := os.Create(dc.outputFile)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	dc.daemonCmd = exec.Command(args[0], args[1:]...)
	dc.daemonCmd.Stdout = outFile
	dc.daemonCmd.Stderr = outFile
	dc.daemonCmd.Stdin = nil

	if err := dc.daemonCmd.Start(); err != nil {
		outFile.Close()
		return fmt.Errorf("failed to start daemon: %w", err)
	}

	dc.daemonPid = dc.daemonCmd.Process.Pid
	clog.Infof("daemon started as pid %d with: %s", dc.daemonPid, dc.config.Start)

	// Close the file handle since the process now owns it
	outFile.Close()

	return nil
}

func (dc *DaemonChecker) terminateProcess(pid int, termWait, killWait int) {
	clog.Infof("terminating process %d", pid)
	
	// Check if process exists
	if !dc.processExists(pid) {
		clog.Infof("process %d does not exist", pid)
		return
	}

	// Wait for termWait seconds
	for i := 0; i < termWait; i++ {
		if !dc.processExists(pid) {
			clog.Infof("process %d exited within %d seconds", pid, i)
			return
		}
		time.Sleep(time.Second)
	}

	// Send SIGTERM
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		clog.Infof("failed to send SIGTERM to %d: %v", pid, err)
	} else {
		clog.Infof("SIGTERM sent to pid %d", pid)
	}

	// Wait for killWait seconds
	for i := 0; i < killWait; i++ {
		if !dc.processExists(pid) {
			clog.Infof("process %d exited within %d seconds after SIGTERM", pid, i)
			return
		}
		time.Sleep(time.Second)
	}

	// Send SIGKILL
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		clog.Infof("failed to send SIGKILL to %d: %v", pid, err)
	} else {
		clog.Infof("SIGKILL sent to pid %d", pid)
	}
}

func (dc *DaemonChecker) processExists(pid int) bool {
	_, err := os.Stat(fmt.Sprintf("/proc/%d", pid))
	return err == nil
}

func (dc *DaemonChecker) monitorOutput() error {
	clog.Infof("looking for %d lines in output within %d seconds", len(dc.expectedLines), dc.config.Timeout)

	timeout := time.After(time.Duration(dc.config.Timeout) * time.Second)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	expectedToFind := make([]string, len(dc.expectedLines))
	copy(expectedToFind, dc.expectedLines)
	found := 0

	for {
		select {
		case <-timeout:
			clog.Infof("timeout %d seconds reached", dc.config.Timeout)
			return dc.checkFinalResults(expectedToFind, found)
		case <-ticker.C:
			// Check if daemon is still running
			if !dc.processExists(dc.daemonPid) {
				clog.Infof("daemon process %d died", dc.daemonPid)
				return dc.checkFinalResults(expectedToFind, found)
			}

			// Check output file for expected patterns
			newFound := dc.checkPatterns(expectedToFind)
			if newFound > 0 {
				found += newFound
				// Remove found patterns from expectedToFind
				remaining := make([]string, 0)
				for _, pattern := range expectedToFind {
					if !dc.patternFound(pattern) {
						remaining = append(remaining, pattern)
					}
				}
				expectedToFind = remaining
			}

			// If all patterns found, we're done
			if len(expectedToFind) == 0 {
				clog.Infof("all expected patterns found")
				return dc.checkFinalResults(expectedToFind, found)
			}
		}
	}
}

func (dc *DaemonChecker) checkPatterns(patterns []string) int {
	found := 0
	for _, pattern := range patterns {
		if dc.patternFound(pattern) {
			clog.Infof("found pattern: %s", pattern)
			found++
		}
	}
	return found
}

func (dc *DaemonChecker) patternFound(pattern string) bool {
	file, err := os.Open(dc.outputFile)
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		matched, err := regexp.MatchString(pattern, line)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func (dc *DaemonChecker) checkFinalResults(missing []string, found int) error {
	// Print output
	clog.Infof("-- begin output --")
	dc.printOutput()
	clog.Infof("-- end output --")

	// Check for missing patterns
	totalExpected := len(dc.expectedLines)
	if found != totalExpected {
		clog.Errorf("found %d of expected %d lines in output", found, totalExpected)
		clog.Errorf("missing:")
		for _, pattern := range missing {
			clog.Errorf("> %s", pattern)
		}
		return fmt.Errorf("missing expected output patterns")
	} else {
		clog.Infof("found %d of expected %d lines in output", found, totalExpected)
	}

	// Check for error patterns
	errorsFound := 0
	if len(dc.errorLines) > 0 {
		errorsFound = dc.checkErrorPatterns()
		if errorsFound > 0 {
			clog.Errorf("matched %d error strings in output", errorsFound)
			return fmt.Errorf("found error patterns in output")
		} else {
			clog.Infof("found 0 / %d error strings in output", len(dc.errorLines))
		}
	}

	return nil
}

func (dc *DaemonChecker) checkErrorPatterns() int {
	errorsFound := 0
	file, err := os.Open(dc.outputFile)
	if err != nil {
		return 0
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		for _, errorPattern := range dc.errorLines {
			matched, err := regexp.MatchString(errorPattern, line)
			if err == nil && matched {
				clog.Errorf("found error pattern '%s' in output: %s", errorPattern, line)
				errorsFound++
			}
		}
	}
	return errorsFound
}

func (dc *DaemonChecker) printOutput() {
	file, err := os.Open(dc.outputFile)
	if err != nil {
		clog.Errorf("failed to read output file: %v", err)
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		clog.Infof("> %s", scanner.Text())
	}
}

func (dc *DaemonChecker) Run() error {
	defer dc.Cleanup()

	// Run setup script
	if dc.config.Setup != "" {
		clog.Infof("running setup script")
		if err := dc.runScript(dc.config.Setup); err != nil {
			return fmt.Errorf("setup script failed: %w", err)
		}
	}

	// Start daemon
	if err := dc.startDaemon(); err != nil {
		return err
	}

	// Monitor output
	monitorErr := dc.monitorOutput()

	// Run post script
	if dc.config.Post != "" {
		clog.Infof("running post script")
		if err := dc.runScript(dc.config.Post); err != nil {
			clog.Errorf("post script failed: %v", err)
		}
	}

	return monitorErr
}

func Command() *cobra.Command {
	var config Config

	cmd := &cobra.Command{
		Use:   "daemon-check-output",
		Short: "Check daemon output for expected patterns",
		Long: `Start a daemon process and monitor its output for expected patterns.
This tool starts a daemon, monitors its output for specified patterns,
and optionally runs setup/post scripts.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			checker, err := NewDaemonChecker(config)
			if err != nil {
				return err
			}
			return checker.Run()
		},
	}

	cmd.Flags().StringVar(&config.Start, "start", "", "Command to start the daemon (required)")
	cmd.Flags().StringVar(&config.Setup, "setup", "", "Setup script to run before starting daemon")
	cmd.Flags().StringVar(&config.Post, "post", "", "Post script to run after monitoring")
	cmd.Flags().IntVar(&config.Timeout, "timeout", 30, "Timeout in seconds to wait for expected output")
	cmd.Flags().StringVar(&config.ExpectedOutput, "expected-output", "", "Newline separated patterns to find in output (required)")
	cmd.Flags().StringVar(&config.ErrorStrings, "error-strings", "ERROR\nFAIL\nFATAL\nTraceback.*most.recent.call\nException in thread\njava.lang.NullPointerException\njava.lang.RuntimeException\nGem::MissingSpecError\ncommand not found", "Newline separated error patterns")

	cmd.MarkFlagRequired("start")
	cmd.MarkFlagRequired("expected-output")

	return cmd
}