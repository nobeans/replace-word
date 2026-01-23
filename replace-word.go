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

type excludePatterns []string

func (e *excludePatterns) String() string {
	return strings.Join(*e, ",")
}

func (e *excludePatterns) Set(value string) error {
	*e = append(*e, value)
	return nil
}

type targetDirs []string

func (t *targetDirs) String() string {
	if len(*t) == 0 {
		return "."
	}
	return strings.Join(*t, ",")
}

func (t *targetDirs) Set(value string) error {
	*t = append(*t, value)
	return nil
}

func main() {
	targetDirs, before, after, dryRun, yes, excludes, err := parseArgs()
	if err != nil {
		printError(err.Error())
		flag.Usage()
		os.Exit(1)
	}

	// Collect all paths from all target directories
	type dirPaths struct {
		dir   string
		paths []string
	}
	var allDirPaths []dirPaths
	var allPaths []string
	for _, dir := range targetDirs {
		found, err := findTargetFiles(dir, excludes)
		if err != nil {
			printError(err.Error())
			os.Exit(1)
		}
		allDirPaths = append(allDirPaths, dirPaths{dir: dir, paths: found})
		allPaths = append(allPaths, found...)
	}
	if len(allPaths) == 0 {
		printError("no target files")
		os.Exit(1)
	}
	fmt.Println(colorize(color.FgCyan, ">> Target files"))
	fmt.Println(strings.Join(allPaths, "\n"))

	textDict := generateDictForText(before, after)
	fmt.Println(colorize(color.FgCyan, ">> Dictionary for text replacement"))
	fmt.Println(textDict)

	fileNameDict := generateDictForFileName(before, after)
	fmt.Println(colorize(color.FgCyan, ">> Dictionary for file rename"))
	fmt.Println(fileNameDict)

	if dryRun {
		fmt.Println(colorize(color.FgYellow, "Dry running..."))
	} else if !yes {
		fmt.Print(colorize(color.FgYellow, "Do you replace words, sure? [y/N]: "))
		if strings.ToLower(readInput()) != "y" {
			fmt.Println("Cancelled.")
			os.Exit(0)
		}
	}

	fmt.Println(colorize(color.FgCyan, ">> Replacing text..."))
	if err := replaceText(allPaths, textDict, dryRun); err != nil {
		printError(err.Error())
		os.Exit(1)
	}

	fmt.Println(colorize(color.FgCyan, ">> Renaming files and dirs..."))
	for _, dp := range allDirPaths {
		if err := renameFilesAndDirs(dp.dir, dp.paths, fileNameDict, dryRun); err != nil {
			printError(err.Error())
			os.Exit(1)
		}
	}
}

func parseArgs() (targetDirs, string, string, bool, bool, excludePatterns, error) {
	var dirs targetDirs
	flag.Var(&dirs, "dir", "Target directory (can be specified multiple times, default: .)")
	dryRun := flag.Bool("dry-run", false, "Enable dry run")
	yes := flag.Bool("yes", false, "Skip confirmation prompt")
	var excludes excludePatterns
	flag.Var(&excludes, "exclude", "Exclude file pattern (glob, can be specified multiple times)")
	flag.Usage = func() {
		o := flag.CommandLine.Output()
		_, name := filepath.Split(flag.CommandLine.Name())
		_, _ = fmt.Fprintf(o, "Usage: %s <hyphenated-before-words> <hyphenated-after-words>\n\nOptions:\n", name)
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 2 {
		return nil, "", "", false, false, nil, errors.New("required two arguments")
	}
	if len(dirs) == 0 {
		dirs = targetDirs{"."}
	}
	return dirs, flag.Arg(0), flag.Arg(1), *dryRun, *yes, excludes, nil
}

func findTargetFiles(dir string, excludes excludePatterns) ([]string, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var paths []string
loop:
	for _, file := range files {
		path := filepath.Join(dir, file.Name())

		// Ignore symbolic links
		fileInfo, err := file.Info()
		if err != nil {
			continue
		}
		if fileInfo.Mode()&os.ModeSymlink != 0 {
			continue
		}

		// Check exclude patterns
		if matchesExclude(path, excludes) {
			continue
		}

		if isDir(file, path) {
			// Ignore specified dirs
			for _, ignore := range []string{".idea", ".git", "node_modules", "build", "public"} {
				if file.Name() == ignore {
					continue loop
				}
			}

			foundInChild, err := findTargetFiles(path, excludes)
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

func matchesExclude(path string, excludes excludePatterns) bool {
	for _, pattern := range excludes {
		// Match against basename
		matched, err := filepath.Match(pattern, filepath.Base(path))
		if err == nil && matched {
			return true
		}
		// Match against full path
		matched, err = filepath.Match(pattern, path)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func isDir(file os.DirEntry, path string) bool {
	return file.IsDir()
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
			{before: upperCamelCase(before), after: upperCamelCase(after)},                                   // UpperCamelCase
			{before: lowerCamelCase(before), after: lowerCamelCase(after)},                                   // lowerCamelCase
			{before: screamingSnakeCase(before), after: screamingSnakeCase(after)},                           // SCREAMING_SNAKE_CASE
			{before: snakeCase(before), after: snakeCase(after)},                                             // snake_case
			{before: screamingKebabCase(before), after: screamingKebabCase(after)},                           // SCREAMING-KEBAB-CASE
			{before: kebabCase(before), after: kebabCase(after)},                                             // kebab-case
			{before: noSign(screamingKebabCase(before)), after: noSign(screamingKebabCase(after))},           // flatcase
			{before: noSign(kebabCase(before)), after: noSign(kebabCase(after))},                             // UPPERCASE
			{before: upperSpaceSeparated(before), after: upperSpaceSeparated(after)},                         // Upper Space Separated
			{before: capitalize(lowerSpaceSeparated(before)), after: capitalize(lowerSpaceSeparated(after))}, // Lower space separated
			{before: lowerSpaceSeparated(before), after: lowerSpaceSeparated(after)},                         // lower space separated
		},
	}
}

func generateDictForFileName(before string, after string) dict {
	return dict{
		items: []dictItem{
			{before: upperCamelCase(before), after: upperCamelCase(after)},                         // UpperCamelCase
			{before: lowerCamelCase(before), after: lowerCamelCase(after)},                         // lowerCamelCase
			{before: screamingSnakeCase(before), after: screamingSnakeCase(after)},                 // SCREAMING_SNAKE_CASE
			{before: snakeCase(before), after: snakeCase(after)},                                   // snake_case
			{before: screamingKebabCase(before), after: screamingKebabCase(after)},                 // SCREAMING-KEBAB-CASE
			{before: kebabCase(before), after: kebabCase(after)},                                   // kebab-case
			{before: noSign(screamingKebabCase(before)), after: noSign(screamingKebabCase(after))}, // flatcase
			{before: noSign(kebabCase(before)), after: noSign(kebabCase(after))},                   // UPPERCASE
		},
	}
}

func upperCamelCase(str string) string {
	var words []string
	for _, w := range strings.Split(str, "-") {
		words = append(words, capitalize(w))
	}
	return strings.Join(words, "")
}

func lowerCamelCase(str string) string {
	return decapitalize(upperCamelCase(str))
}

func screamingSnakeCase(str string) string {
	return strings.ToUpper(regexp.MustCompile(`-`).ReplaceAllString(str, "_"))
}

func snakeCase(str string) string {
	return strings.ToLower(regexp.MustCompile(`-`).ReplaceAllString(str, "_"))
}

func screamingKebabCase(str string) string {
	return strings.ToUpper(str)
}

func kebabCase(str string) string {
	return strings.ToLower(str)
}

func noSign(str string) string {
	return regexp.MustCompile(`[_-]`).ReplaceAllString(str, "")
}

func upperSpaceSeparated(str string) string {
	var words []string
	for _, w := range strings.Split(str, "-") {
		words = append(words, capitalize(w))
	}
	return strings.Join(words, " ")
}

func lowerSpaceSeparated(str string) string {
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
