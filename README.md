Replace Word
============

Replaces words of text and file names.

The words are given as hyphenated words (e.g. `user-name`), and are replaced in each naming style:
`UserName`, `userName`, `USER_NAME`, `user_name`, `USER-NAME`, `user-name`, `USERNAME`, `username`, `User Name`, `User name` and `user name`.

Files ignored by Git (`.gitignore`, `.git/info/exclude` and `core.excludesFile`) are excluded by default.
The `.git` directory, symbolic links and binary files are always excluded.


## Installation

```sh
$ go install github.com/nobeans/replace-word@latest
```


## Usage

```
Usage: replace-word <hyphenated-before-words> <hyphenated-after-words>

Options:
  -dir value
        Target directory (can be specified multiple times, default: .)
  -dry-run
        Enable dry run
  -exclude value
        Exclude file pattern (glob, can be specified multiple times)
  -exclude-from value
        Read exclude file patterns from a file (one glob per line; blank lines and lines starting with # are ignored)
  -include-gitignored
        Include files ignored by Git (.gitignore, .git/info/exclude and core.excludesFile), which are excluded by default
  -v    Show version
  -version
        Show version
  -yes
        Skip confirmation prompt
```

For example, the following command shows what will be replaced without changing anything:

```sh
$ replace-word -dry-run user-name account-id
```
