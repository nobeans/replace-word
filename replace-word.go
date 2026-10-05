package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

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
	// Expand glob pattern
	matches, err := filepath.Glob(value)
	if err != nil {
		return err
	}

	// If no matches, treat as literal path
	if len(matches) == 0 {
		*t = append(*t, value)
		return nil
	}

	// Filter only directories
	for _, match := range matches {
		info, err := os.Stat(match)
		if err != nil {
			continue
		}
		if info.IsDir() {
			*t = append(*t, match)
		}
	}

	return nil
}

func main() {
	targetDirs, before, after, dryRun, yes, excludes, includeGitignored, err := parseArgs()
	if err != nil {
		printError(err.Error())
		flag.Usage()
		os.Exit(1)
	}

	// Collect paths ignored by Git in all target directories before replacing anything.
	ignored := map[string]bool{}
	if !includeGitignored {
		for _, dir := range targetDirs {
			checked, err := collectGitIgnoredPaths(dir, ignored)
			if err != nil {
				printError(err.Error())
				os.Exit(1)
			}
			if !checked {
				printNote("%s is not inside a Git repository or git command is not found, no files are excluded by Git", dir)
			}
		}
	}

	// Collect all paths from all target directories
	type dirPaths struct {
		dir   string
		paths []string
	}
	var allDirPaths []dirPaths
	var allPaths []string
	for _, dir := range targetDirs {
		found, err := findTargetFiles(dir, excludes, ignored)
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

func parseArgs() (targetDirs, string, string, bool, bool, excludePatterns, bool, error) {
	var dirs targetDirs
	flag.Var(&dirs, "dir", "Target directory (can be specified multiple times, default: .)")
	dryRun := flag.Bool("dry-run", false, "Enable dry run")
	yes := flag.Bool("yes", false, "Skip confirmation prompt")
	var excludes excludePatterns
	flag.Var(&excludes, "exclude", "Exclude file pattern (glob, can be specified multiple times)")
	includeGitignored := flag.Bool("include-gitignored", false, "Include files ignored by Git (.gitignore, .git/info/exclude and core.excludesFile), which are excluded by default")
	var showVersion bool
	flag.BoolVar(&showVersion, "v", false, "Show version")
	flag.BoolVar(&showVersion, "version", false, "Show version")
	flag.Usage = func() {
		o := flag.CommandLine.Output()
		_, name := filepath.Split(flag.CommandLine.Name())
		_, _ = fmt.Fprintf(o, "Usage: %s <hyphenated-before-words> <hyphenated-after-words>\n\nOptions:\n", name)
		flag.PrintDefaults()
	}
	flag.Parse()
	if showVersion {
		fmt.Println(filepath.Base(os.Args[0]), version())
		os.Exit(0)
	}
	if flag.NArg() != 2 {
		return nil, "", "", false, false, nil, false, errors.New("required two arguments")
	}
	if len(dirs) == 0 {
		dirs = targetDirs{"."}
	}
	return dirs, flag.Arg(0), flag.Arg(1), *dryRun, *yes, excludes, *includeGitignored, nil
}

// version returns the version embedded by Go at build time.
// go install with a version (e.g. @latest) embeds the tag of the module, so no hard-coded version is needed.
func version() string {
	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return "unknown"
	}
	return info.Main.Version
}

// collectGitIgnoredPaths adds the paths ignored by Git under dir to ignored.
// Git decides them with all of its rules (.gitignore in the directory and its ancestors and descendants,
// .git/info/exclude and core.excludesFile), so they are the same as what Git ignores.
// Tracked files are not included even if they match .gitignore.
// It returns false if git command is not found or dir is not inside a Git repository.
func collectGitIgnoredPaths(dir string, ignored map[string]bool) (bool, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return false, nil
	}
	if err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Run(); err != nil {
		return false, nil
	}
	// --directory lists an ignored directory itself instead of the files in it,
	// so that it can be skipped without walking into it.
	out, err := exec.Command("git", "-C", dir, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z").Output()
	if err != nil {
		return false, fmt.Errorf("failed to list files ignored by Git in %s: %v", dir, err)
	}
	for _, path := range strings.Split(string(out), "\x00") {
		if path == "" {
			continue
		}
		// Paths are relative to dir. Directories end with "/".
		ignored[filepath.Join(dir, path)] = true
	}
	return true, nil
}

func findTargetFiles(dir string, excludes excludePatterns, ignored map[string]bool) ([]string, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var paths []string
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

		// Check paths ignored by Git
		if ignored[path] {
			continue
		}

		if isDir(file, path) {
			// Ignore Git's own directory
			if file.Name() == ".git" {
				continue
			}

			foundInChild, err := findTargetFiles(path, excludes, ignored)
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
	r, size := utf8.DecodeRuneInString(str)
	if size == 0 {
		return ""
	}
	return string(unicode.ToUpper(r)) + str[size:]
}

func decapitalize(str string) string {
	r, size := utf8.DecodeRuneInString(str)
	if size == 0 {
		return ""
	}
	return string(unicode.ToLower(r)) + str[size:]
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

func printNote(format string, args ...interface{}) {
	_, _ = fmt.Fprintln(os.Stderr, colorize(color.FgYellow, "NOTE: "+format, args...))
}

func colorize(attr color.Attribute, format string, args ...interface{}) string {
	return color.New(attr).Sprintf(format, args...)
}
