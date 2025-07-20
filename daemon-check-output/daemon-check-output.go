package main

import (
	"bufio"
	"fmt"
	"io"
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
	config           Config
	tempDir          string
	outputFile       string
	daemonCmd        *exec.Cmd
	daemonPid        int
	expectedLines    []string
	errorLines       []string
	workingDir       string
	expectedPatterns []*regexp.Regexp
	errorPatterns    []*regexp.Regexp
	outputBuffer     []string
	lastReadPos      int64
}

// executeShellScript executes shell script using system shell but with better handling
func (dc *DaemonChecker) executeShellScript(script string) error {
	if script == "" {
		return nil
	}

	// Expand environment variables in the script
	expandedScript := dc.expandEnvVars(script)
	
	// Use /bin/sh to execute the script properly
	cmd := exec.Command("/bin/sh", "-c", expandedScript)
	cmd.Dir = dc.workingDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = os.Environ()
	
	return cmd.Run()
}

// readNewOutput reads new content from the output file since the last read position
func (dc *DaemonChecker) readNewOutput() error {
	file, err := os.Open(dc.outputFile)
	if err != nil {
		return err
	}
	defer file.Close()
	
	// Seek to the last read position
	_, err = file.Seek(dc.lastReadPos, io.SeekStart)
	if err != nil {
		return err
	}
	
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		dc.outputBuffer = append(dc.outputBuffer, line)
	}
	
	// Update the last read position
	dc.lastReadPos, _ = file.Seek(0, io.SeekCurrent)
	
	return scanner.Err()
}

// expandEnvVars expands environment variables in a string
func (dc *DaemonChecker) expandEnvVars(s string) string {
	return os.ExpandEnv(s)
}


func NewDaemonChecker(config Config) (*DaemonChecker, error) {
	tempDir, err := os.MkdirTemp("", "daemon-check-output-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}

	workingDir, err := os.Getwd()
	if err != nil {
		workingDir = "/"
	}

	dc := &DaemonChecker{
		config:     config,
		tempDir:    tempDir,
		outputFile: filepath.Join(tempDir, "output.log"),
		workingDir: workingDir,
	}

	// Parse expected output lines
	if config.ExpectedOutput != "" {
		dc.expectedLines = strings.Split(strings.TrimSpace(config.ExpectedOutput), "\n")
		// Remove empty lines and compile patterns
		filtered := make([]string, 0, len(dc.expectedLines))
		patterns := make([]*regexp.Regexp, 0, len(dc.expectedLines))
		for _, line := range dc.expectedLines {
			line = strings.TrimSpace(line)
			if line != "" {
				pattern, err := regexp.Compile(line)
				if err != nil {
					return nil, fmt.Errorf("failed to compile expected pattern '%s': %w", line, err)
				}
				filtered = append(filtered, line)
				patterns = append(patterns, pattern)
			}
		}
		dc.expectedLines = filtered
		dc.expectedPatterns = patterns
	}

	// Parse error strings
	if config.ErrorStrings != "" {
		dc.errorLines = strings.Split(strings.TrimSpace(config.ErrorStrings), "\n")
		// Remove empty lines and compile patterns
		filtered := make([]string, 0, len(dc.errorLines))
		patterns := make([]*regexp.Regexp, 0, len(dc.errorLines))
		for _, line := range dc.errorLines {
			line = strings.TrimSpace(line)
			if line != "" {
				pattern, err := regexp.Compile(line)
				if err != nil {
					return nil, fmt.Errorf("failed to compile error pattern '%s': %w", line, err)
				}
				filtered = append(filtered, line)
				patterns = append(patterns, pattern)
			}
		}
		dc.errorLines = filtered
		dc.errorPatterns = patterns
	}

	return dc, nil
}

func (dc *DaemonChecker) Cleanup() {
	if dc.daemonCmd != nil && dc.daemonCmd.Process != nil {
		dc.terminateProcess(dc.daemonCmd.Process.Pid, 0, 5)
	}
	if dc.tempDir != "" {
		os.RemoveAll(dc.tempDir)
	}
}

func (dc *DaemonChecker) runScript(script string) error {
	return dc.executeShellScript(script)
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
		clog.Infof("SIGTERM sent to pid %d - waiting up to %d seconds for graceful shutdown", pid, killWait)
	}

	// Wait for killWait seconds
	for i := 0; i < killWait; i++ {
		if !dc.processExists(pid) {
			clog.Infof("process %d exited gracefully after %d seconds", pid, i+1)
			return
		}
		time.Sleep(time.Second)
	}

	// Send SIGKILL
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		clog.Infof("failed to send SIGKILL to %d: %v", pid, err)
	} else {
		clog.Infof("SIGKILL sent to pid %d (process did not exit gracefully)", pid)
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
			clog.Errorf("timeout %d seconds reached - stopping daemon monitoring", dc.config.Timeout)
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
	// Find the pre-compiled pattern
	var compiledPattern *regexp.Regexp
	for i, expectedLine := range dc.expectedLines {
		if expectedLine == pattern {
			compiledPattern = dc.expectedPatterns[i]
			break
		}
	}
	
	if compiledPattern == nil {
		// Fallback to compiling on the fly if not found
		var err error
		compiledPattern, err = regexp.Compile(pattern)
		if err != nil {
			return false
		}
	}

	// Read any new output first
	dc.readNewOutput()

	// Check all buffered lines
	for _, line := range dc.outputBuffer {
		if compiledPattern.MatchString(line) {
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
		if len(missing) > 0 {
			clog.Errorf("missing expected patterns:")
			for _, pattern := range missing {
				clog.Errorf("> %s", pattern)
			}
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
	
	// Read any new output first
	dc.readNewOutput()

	// Check all buffered lines for error patterns
	for _, line := range dc.outputBuffer {
		for i, errorPattern := range dc.errorLines {
			if dc.errorPatterns[i].MatchString(line) {
				clog.Errorf("found error pattern '%s' in output: %s", errorPattern, line)
				errorsFound++
			}
		}
	}
	return errorsFound
}

func (dc *DaemonChecker) printOutput() {
	// Read any remaining output
	dc.readNewOutput()
	
	// Print all buffered output
	for _, line := range dc.outputBuffer {
		clog.Infof("> %s", line)
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
	defaultErrorStrings := strings.Join([]string{
		"ERROR",
		"FAIL",
		"FATAL",
		"Traceback.*most.recent.call",
		"Exception in thread",
		"java.lang.NullPointerException",
		"java.lang.RuntimeException",
		"Gem::MissingSpecError",
		"command not found",
	}, "\n")
	cmd.Flags().StringVar(&config.ErrorStrings, "error-strings", defaultErrorStrings, "Newline separated error patterns")

	cmd.MarkFlagRequired("start")
	cmd.MarkFlagRequired("expected-output")

	return cmd
}

func main() {
	if err := Command().Execute(); err != nil {
		clog.Errorf("failed to execute command: %v", err)
		os.Exit(1)
	}
}
