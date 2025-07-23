package main

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckSymlink(t *testing.T) {
	tempDir, err := ioutil.TempDir("", "symlink-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	targetFile := filepath.Join(tempDir, "target.txt")
	if err := ioutil.WriteFile(targetFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	tests := []struct {
		name           string
		setup          func() string
		allowAbsolute  bool
		allowDangling  bool
		expectedPasses int64
		expectedFails  int64
	}{
		{
			name: "valid relative symlink",
			setup: func() string {
				linkPath := filepath.Join(tempDir, "valid_relative")
				if err := os.Symlink("target.txt", linkPath); err != nil {
					t.Fatalf("Failed to create symlink: %v", err)
				}
				return linkPath
			},
			allowAbsolute:  false,
			allowDangling:  false,
			expectedPasses: 1,
			expectedFails:  0,
		},
		{
			name: "absolute symlink not allowed",
			setup: func() string {
				linkPath := filepath.Join(tempDir, "valid_absolute")
				if err := os.Symlink(targetFile, linkPath); err != nil {
					t.Fatalf("Failed to create symlink: %v", err)
				}
				return linkPath
			},
			allowAbsolute:  false,
			allowDangling:  false,
			expectedPasses: 0,
			expectedFails:  1,
		},
		{
			name: "absolute symlink allowed",
			setup: func() string {
				linkPath := filepath.Join(tempDir, "absolute_skip")
				if err := os.Symlink(targetFile, linkPath); err != nil {
					t.Fatalf("Failed to create symlink: %v", err)
				}
				return linkPath
			},
			allowAbsolute:  true,
			allowDangling:  false,
			expectedPasses: 1,
			expectedFails:  0,
		},
		{
			name: "broken relative symlink",
			setup: func() string {
				linkPath := filepath.Join(tempDir, "broken_relative")
				if err := os.Symlink("nonexistent.txt", linkPath); err != nil {
					t.Fatalf("Failed to create broken symlink: %v", err)
				}
				return linkPath
			},
			allowAbsolute:  false,
			allowDangling:  false,
			expectedPasses: 0,
			expectedFails:  1,
		},
		{
			name: "broken absolute symlink",
			setup: func() string {
				linkPath := filepath.Join(tempDir, "broken_absolute")
				if err := os.Symlink(filepath.Join(tempDir, "nonexistent.txt"), linkPath); err != nil {
					t.Fatalf("Failed to create broken symlink: %v", err)
				}
				return linkPath
			},
			allowAbsolute:  true,
			allowDangling:  false,
			expectedPasses: 0,
			expectedFails:  1,
		},
		{
			name: "circular symlink",
			setup: func() string {
				linkPath := filepath.Join(tempDir, "circular")
				if err := os.Symlink("circular", linkPath); err != nil {
					t.Fatalf("Failed to create circular symlink: %v", err)
				}
				return linkPath
			},
			allowAbsolute:  false,
			allowDangling:  false,
			expectedPasses: 0,
			expectedFails:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			linkPath := tt.setup()
			result := &Result{
				FailMessages: make([]string, 0),
			}

			checkSymlink(linkPath, result, tt.allowDangling, tt.allowAbsolute)

			if result.Passes != tt.expectedPasses {
				t.Errorf("Expected %d passes, got %d", tt.expectedPasses, result.Passes)
			}

			if result.Fails != tt.expectedFails {
				t.Errorf("Expected %d fails, got %d", tt.expectedFails, result.Fails)
			}
		})
	}
}

func TestResultMethods(t *testing.T) {
	t.Run("AddPass", func(t *testing.T) {
		result := &Result{
			FailMessages: make([]string, 0),
		}

		result.AddPass("test pass message")

		if result.Passes != 1 {
			t.Errorf("Expected 1 pass, got %d", result.Passes)
		}

		if result.Fails != 0 {
			t.Errorf("Expected 0 fails, got %d", result.Fails)
		}
	})

	t.Run("AddFail", func(t *testing.T) {
		result := &Result{
			FailMessages: make([]string, 0),
		}

		result.AddFail("test fail message")

		if result.Passes != 0 {
			t.Errorf("Expected 0 passes, got %d", result.Passes)
		}

		if result.Fails != 1 {
			t.Errorf("Expected 1 fail, got %d", result.Fails)
		}

		if len(result.FailMessages) != 1 {
			t.Errorf("Expected 1 fail message, got %d", len(result.FailMessages))
		}

		if !strings.Contains(result.FailMessages[0], "test fail message") {
			t.Errorf("Expected fail message to contain 'test fail message', got %s", result.FailMessages[0])
		}
	})
}

func TestTargetEscapesTree(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		target   string
		expected bool
	}{
		{
			name:     "normal relative path within tree",
			src:      "usr/bin/foo",
			target:   "../lib/bar",
			expected: false,
		},
		{
			name:     "target escapes tree with many parent dirs",
			src:      "etc/passwd",
			target:   "../../../../../../etc/passwd",
			expected: true,
		},
		{
			name:     "target within same directory",
			src:      "usr/bin/foo",
			target:   "bar",
			expected: false,
		},
		{
			name:     "target goes up one level but stays in tree",
			src:      "usr/bin/foo",
			target:   "../share/file",
			expected: false,
		},
		{
			name:     "target escapes with simple parent reference",
			src:      "file",
			target:   "../../../outside",
			expected: true,
		},
		{
			name:     "complex path that stays within tree",
			src:      "a/b/c/d/file",
			target:   "../../../x/y/z",
			expected: false,
		},
		{
			name:     "target exactly at root boundary",
			src:      "a/file",
			target:   "../..",
			expected: true,
		},
		{
			name:     "deeply nested source with escaping target",
			src:      "a/b/c/d/e/f/file",
			target:   "../../../../../../../..",
			expected: true,
		},
		{
			name:     "absolute",
			src:      "foo/bar",
			target:   "/foo/bar",
			expected: true,
		},
		{
			name:     "confusing",
			src:      "foo/bar",
			target:   "././../usr/./../../dest",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := targetEscapesTree(tt.target, tt.src)
			if result != tt.expected {
				t.Errorf("targetEscapesTree(%q, %q) = %v, expected %v", tt.target, tt.src, result, tt.expected)
			}
		})
	}
}
