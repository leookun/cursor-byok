package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

func bailIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func findPrettier() (string, error) {
	// 尝试常见的 prettier 命令名
	names := []string{"prettier", "prettier.cmd", "npx"}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("prettier not found in PATH, please install: npm install -g prettier")
}

func main() {
	// 命令行参数
	inputPath := flag.String("input", "", "Path to JS file (e.g., extensionHostProcess.js)")
	outputDir := flag.String("output", "", "Output directory for proto files (default: ./cursor_proto)")
	skipFormat := flag.Bool("skip-format", false, "Skip prettier formatting")
	strict := flag.Bool("strict", true, "Fail when extraction validation detects unresolved/placeholder output")
	flag.Parse()

	// 如果没有 -input 参数，尝试从位置参数获取
	if *inputPath == "" && flag.NArg() > 0 {
		*inputPath = flag.Arg(0)
	}

	if *inputPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: ext -input <path-to-js-file> [-output <dir>] [-skip-format]")
		fmt.Fprintln(os.Stderr, "       ext <path-to-js-file>")
		fmt.Fprintln(os.Stderr, "\nExample:")
		fmt.Fprintln(os.Stderr, "  ext -input /path/to/extensionHostProcess.js")
		fmt.Fprintln(os.Stderr, "  ext C:\\Users\\xxx\\AppData\\Local\\Programs\\cursor\\resources\\app\\out\\vs\\workbench\\api\\node\\extensionHostProcess.js")
		os.Exit(1)
	}

	// 验证输入文件
	info, err := os.Stat(*inputPath)
	bailIf(err)

	if info.IsDir() {
		bailIf(fmt.Errorf("expected %s to be file, is dir", *inputPath))
	}

	// 设置输出目录
	if *outputDir == "" {
		wd, err := os.Getwd()
		bailIf(err)
		*outputDir = filepath.Join(wd, "cursor_proto")
	}

	// 复制到临时文件（不修改原文件）
	fmt.Println("Copying source file to temp directory...")
	originalFile, err := os.Open(*inputPath)
	bailIf(err)

	tempFile, err := os.CreateTemp(os.TempDir(), "cursor-source-*.js")
	bailIf(err)
	tempFileName := tempFile.Name()

	_, err = io.Copy(tempFile, originalFile)
	bailIf(err)

	bailIf(originalFile.Close())
	bailIf(tempFile.Close())

	fmt.Printf("Temp file: %s\n", tempFileName)

	// 格式化临时文件
	if !*skipFormat {
		prettierBin, err := findPrettier()
		if err != nil {
			fmt.Printf("Warning: %v\n", err)
			fmt.Println("Skipping formatting, extraction may be less accurate...")
		} else {
			fmt.Println("Formatting file (this may take a while)...")
			var prettierCmd *exec.Cmd
			if filepath.Base(prettierBin) == "npx" {
				prettierCmd = exec.Command(prettierBin, "prettier", "--write", tempFileName)
			} else {
				prettierCmd = exec.Command(prettierBin, "--write", tempFileName)
			}
			out, err := prettierCmd.CombinedOutput()
			if err != nil {
				fmt.Printf("Prettier output: %s\n", string(out))
				fmt.Println("Warning: formatting failed, continuing anyway...")
			} else {
				fmt.Println("Formatting complete")
			}
		}
	} else {
		fmt.Println("Skipping formatting (--skip-format)")
	}

	// 运行提取器
	fmt.Println("Extracting Proto definitions...")
	SetStrictMode(*strict)
	ExtractProtos(tempFileName, *outputDir)

	// 清理临时文件
	os.Remove(tempFileName)

	fmt.Printf("\nOutput directory: %s\n", *outputDir)
}
