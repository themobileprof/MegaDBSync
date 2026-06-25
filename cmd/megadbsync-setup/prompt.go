//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func promptYesNo(question string, defaultYes bool) bool {
	if !isInteractive() {
		return defaultYes
	}
	hint := "[y/N]"
	if defaultYes {
		hint = "[Y/n]"
	}
	fmt.Printf("%s %s: ", question, hint)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return defaultYes
	}
	ans := strings.TrimSpace(strings.ToLower(scanner.Text()))
	if ans == "" {
		return defaultYes
	}
	return ans == "y" || ans == "yes"
}

func promptMenu(title string, options []string) int {
	if !isInteractive() {
		return 0
	}
	fmt.Println()
	fmt.Println(title)
	for i, opt := range options {
		fmt.Printf("  [%d] %s\n", i+1, opt)
	}
	fmt.Print("Choice: ")
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || n < 1 || n > len(options) {
		return 0
	}
	return n
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

func printlnSection(title string) {
	fmt.Println()
	fmt.Println("========================================")
	fmt.Printf("  %s\n", title)
	fmt.Println("========================================")
}
