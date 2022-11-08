package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/fatih/color"
	"github.com/hexops/gotextdiff"
	"github.com/hexops/gotextdiff/myers"
	"github.com/hexops/gotextdiff/span"
)

func main() {
	targetDir, before, after, dryRun, err := parseArgs()
	if err != nil {
		printError(err.Error())
		flag.Usage()
		os.Exit(1)
	}

	paths, err := findTargetFiles(targetDir)
	if err != nil {
		printError(err.Error())
		os.Exit(1)
	}
	if len(paths) == 0 {
		printError("no target files")
		os.Exit(1)
	}
	fmt.Println(colorize(color.FgCyan, ">> Target files"))
	fmt.Println(strings.Join(paths, "\n"))

	textDict := generateDictForText(before, after)
	fmt.Println(colorize(color.FgCyan, ">> Dictionary for text replacement"))
	fmt.Println(textDict)

	fileNameDict := generateDictForFileName(before, after)
	fmt.Println(colorize(color.FgCyan, ">> Dictionary for file rename"))
	fmt.Println(fileNameDict)

	if dryRun {
		fmt.Println(colorize(color.FgYellow, "Dry running..."))
	} else {
		fmt.Print(colorize(color.FgYellow, "Do you replace words, sure? [y/N]: "))
		if strings.ToLower(readInput()) != "y" {
			fmt.Println("Cancelled.")
			os.Exit(0)
		}
	}

	fmt.Println(colorize(color.FgCyan, ">> Replacing text..."))
	if err := replaceText(paths, textDict, dryRun); err != nil {
		printError(err.Error())
		os.Exit(1)
	}

	fmt.Println(colorize(color.FgCyan, ">> Renaming files and dirs..."))
	if err := renameFilesAndDirs(targetDir, paths, fileNameDict, dryRun); err != nil {
		printError(err.Error())
		os.Exit(1)
	}
}

func parseArgs() (string, string, string, bool, error) {
	dir := flag.String("dir", ".", "Target directory")
	dryRun := flag.Bool("dry-run", false, "Enable dry run")
	flag.Usage = func() {
		o := flag.CommandLine.Output()
		_, name := filepath.Split(flag.CommandLine.Name())
		_, _ = fmt.Fprintf(o, "Usage: %s <hyphenated-before-words> <hyphenated-after-words>\n\nOptions:\n", name)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		return "", "", "", false, errors.New("required two arguments")
	}
	return *dir, flag.Arg(0), flag.Arg(1), *dryRun, nil
}

func findTargetFiles(dir string) ([]string, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var paths []string
loop:
	for _, file := range files {
		path := filepath.Join(dir, file.Name())

		if file.IsDir() {
			// Ignore specified dirs
			for _, ignore := range []string{".idea", ".git", "node_modules", "build", "public"} {
				if file.Name() == ignore {
					continue loop
				}
			}

			foundInChild, err := findTargetFiles(path)
			if err != nil {
				return nil, err
			}

			paths = append(paths, foundInChild...)
			continue
		}

		// Ignore binary files
		bs, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(http.DetectContentType(bs), "text/") {
			continue
		}

		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}

type dict struct {
	items []dictItem
}

type dictItem struct {
	before string
	after  string
}

func (d dict) String() string {
	var its []string
	var ambiguous bool
	for _, it := range d.items {
		s := it.String()
		for _, itt := range its {
			if s == itt {
				ambiguous = true
			}
		}
		its = append(its, s)
	}
	if ambiguous {
		its = append(its, colorize(color.FgYellow, "WARN: dictionary is ambiguous"))
		its = append(its, colorize(color.FgYellow, "HINT: It may cause unexpected result. You'd better add another word at least."))
	}
	return strings.Join(its, "\n")
}

func (di dictItem) String() string {
	return fmt.Sprintf(`"%s" => "%s"`, di.before, di.after)
}

func generateDictForText(before string, after string) dict {
	return dict{
		items: []dictItem{
			{before: largeCamelCase(before), after: largeCamelCase(after)},
			{before: smallCamelCase(before), after: smallCamelCase(after)},
			{before: largeSnakeCase(before), after: largeSnakeCase(after)},
			{before: smallSnakeCase(before), after: smallSnakeCase(after)},
			{before: allLargeCase(before), after: allLargeCase(after)},
			{before: allSmallCase(before), after: allSmallCase(after)},
			{before: noSign(allLargeCase(before)), after: noSign(allLargeCase(after))},
			{before: noSign(allSmallCase(before)), after: noSign(allSmallCase(after))},
			{before: largeSpaceSeparated(before), after: largeSpaceSeparated(after)},
			{before: capitalize(smallSpaceSeparated(before)), after: capitalize(smallSpaceSeparated(after))},
			{before: smallSpaceSeparated(before), after: smallSpaceSeparated(after)},
		},
	}
}

func generateDictForFileName(before string, after string) dict {
	return dict{
		items: []dictItem{
			{before: largeCamelCase(before), after: largeCamelCase(after)},
			{before: smallCamelCase(before), after: smallCamelCase(after)},
			{before: largeSnakeCase(before), after: largeSnakeCase(after)},
			{before: smallSnakeCase(before), after: smallSnakeCase(after)},
			{before: allLargeCase(before), after: allLargeCase(after)},
			{before: allSmallCase(before), after: allSmallCase(after)},
			{before: noSign(allLargeCase(before)), after: noSign(allLargeCase(after))},
			{before: noSign(allSmallCase(before)), after: noSign(allSmallCase(after))},
		},
	}
}

func largeCamelCase(str string) string {
	var words []string
	for _, w := range strings.Split(str, "-") {
		words = append(words, capitalize(w))
	}
	return strings.Join(words, "")
}

func smallCamelCase(str string) string {
	return decapitalize(largeCamelCase(str))
}

func largeSnakeCase(str string) string {
	return strings.ToUpper(regexp.MustCompile(`-`).ReplaceAllString(str, "_"))
}

func smallSnakeCase(str string) string {
	return strings.ToLower(regexp.MustCompile(`-`).ReplaceAllString(str, "_"))
}

func allLargeCase(str string) string {
	return strings.ToUpper(str)
}

func allSmallCase(str string) string {
	return strings.ToLower(str)
}

func noSign(str string) string {
	return regexp.MustCompile(`[_-]`).ReplaceAllString(str, "")
}

func largeSpaceSeparated(str string) string {
	var words []string
	for _, w := range strings.Split(str, "-") {
		words = append(words, capitalize(w))
	}
	return strings.Join(words, " ")
}

func smallSpaceSeparated(str string) string {
	return regexp.MustCompile(`[_-]`).ReplaceAllString(str, " ")
}

func capitalize(str string) string {
	for i, v := range str {
		return string(unicode.ToUpper(v)) + str[i+1:]
	}
	return ""
}

func decapitalize(str string) string {
	for i, v := range str {
		return string(unicode.ToLower(v)) + str[i+1:]
	}
	return ""
}

func readInput() string {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Scan()
	return scanner.Text()
}

func replaceText(paths []string, dict dict, dryRun bool) error {
	for _, path := range paths {
		bs, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		beforeText := string(bs)
		afterText := beforeText
		for _, it := range dict.items {
			afterText = strings.ReplaceAll(afterText, it.before, it.after)
		}
		if beforeText == afterText {
			continue
		}

		if !dryRun {
			if err := os.WriteFile(path, []byte(afterText), 0); err != nil {
				return err
			}
		}

		fmt.Println(diffText(path, beforeText, afterText))
	}
	return nil
}

func diffText(path string, a string, b string) string {
	edits := myers.ComputeEdits(span.URIFromPath(path), a, b)
	diff := fmt.Sprint(gotextdiff.ToUnified("a/"+path, "b/"+path, a, edits))
	diff = regexp.MustCompile(`(?m)^-.*$`).ReplaceAllStringFunc(diff, func(s string) string {
		if strings.HasPrefix(s, "---") {
			return s
		}
		return colorize(color.FgRed, s)
	})
	diff = regexp.MustCompile(`(?m)^\+.*$`).ReplaceAllStringFunc(diff, func(s string) string {
		if strings.HasPrefix(s, "+++") {
			return s
		}
		return colorize(color.FgGreen, s)
	})
	return diff
}

func renameFilesAndDirs(baseDir string, paths []string, dict dict, dryRun bool) error {
	// e.g. ["aaa/bbb/ccc.txt"] -> ["aaa/bbb/ccc.txt", "aaa/bbb", "aaa"] (sorted from leaf to root)
	var expandedPaths []string
	found := map[string]bool{}
	for _, path := range paths {
		for _, expanded := range expandAncestorDirs(baseDir, path) {
			if !found[expanded] {
				found[expanded] = true
				expandedPaths = append(expandedPaths, expanded)
			}
		}
	}
	sort.Slice(expandedPaths, func(i, j int) bool {
		return expandedPaths[i] > expandedPaths[j]
	})

	for _, beforePath := range expandedPaths {
		dir, beforeFile := filepath.Split(beforePath)
		dir = filepath.Dir(dir)

		afterFile := beforeFile
		for _, it := range dict.items {
			afterFile = strings.ReplaceAll(afterFile, it.before, it.after)
		}
		if beforeFile == afterFile {
			continue
		}

		if !dryRun {
			afterPath := filepath.Join(dir, afterFile)
			if err := os.Rename(beforePath, afterPath); err != nil {
				return err
			}
		}
		fmt.Printf("%s => %s\n", filepath.Join(dir, colorize(color.FgRed, beforeFile)), filepath.Join(dir, colorize(color.FgGreen, afterFile)))
	}
	return nil
}

func expandAncestorDirs(baseDir string, path string) []string {
	var paths []string
	paths = append(paths, path)
	dir, _ := filepath.Split(path)
	dir = filepath.Dir(dir)
	if dir != baseDir {
		paths = append(paths, expandAncestorDirs(baseDir, dir)...)
	}
	return paths
}

func printError(format string, args ...interface{}) {
	_, _ = fmt.Fprintln(os.Stderr, colorize(color.FgRed, "ERROR: "+format, args...))
}

func colorize(attr color.Attribute, format string, args ...interface{}) string {
	return color.New(attr).Sprintf(format, args...)
}
