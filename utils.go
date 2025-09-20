package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
)

// die is print-and-exit-with-error helper, ala Perl.
func die(format string, a ...any) {
	fmt.Printf(format, a...)
	os.Exit(1)
}

// fileExists returns true if a file exists, and false if it doesn't.
func fileExists(file string) bool {
	if _, err := os.Stat(file); errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return true
}

// simplerun is an abstraction to execute a Command.
func simpleRun(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	return cmd.Run()
}

// calculateSHA256Sum calculates the SHA-256 checksum of a file.
func calculateSHA256Sum(filePath string) (string, error) {
	//#nosec G304 -- Yes, but no.
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close() // Ensure the file is closed when the function exits

	hash := sha256.New() // Create a new SHA-256 hash function

	// Copy the file's content into the hash function
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("failed to copy file content to hash: %w", err)
	}

	// Get the final hash sum and encode it to a hexadecimal string
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// copyFile ... copies a file.
func copyFile(src, dst string) (int64, error) {
	//#nosec G304 - Open the source file for reading
	sourceFile, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer sourceFile.Close() // Ensure the source file is closed

	// Get file info to preserve permissions
	sourceFileInfo, err := sourceFile.Stat()
	if err != nil {
		return 0, err
	}

	//#nosec G304 - Create the destination file with the same permissions as the source
	destinationFile, err := os.OpenFile(dst, os.O_RDWR|os.O_CREATE|os.O_TRUNC, sourceFileInfo.Mode())
	if err != nil {
		return 0, err
	}
	defer destinationFile.Close() // Ensure the destination file is closed

	// Copy the contents from source to destination
	bytesCopied, err := io.Copy(destinationFile, sourceFile)
	if err != nil {
		return 0, err
	}

	return bytesCopied, nil
}
