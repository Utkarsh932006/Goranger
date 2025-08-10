# Goranger — A Terminal File Explorer in Go

Goranger is a lightweight, text-based file browser for your terminal. It’s designed to make navigating directories faster and more convenient without switching to a graphical interface.

## What is Goranger?
Goranger is built in [Go](https://go.dev/) using [tview](https://github.com/rivo/tview) and [tcell](https://github.com/gdamore/tcell). It’s a simple, practical tool for anyone who prefers working in a terminal and wants quick file navigation.

## Current Features
- Fast directory listing for efficient navigation.
- Modal previews to quickly check file paths.
- Cross-platform support for Linux, macOS, and Windows.

## Installation
```bash
git clone https://github.com/yourusername/goranger.git
cd goranger
go mod tidy
go run main.go
```

## Usage
- Use the arrow keys to move through directories.
- Press **Enter** to preview the selected file path.
- Press **q** or **Ctrl+C** to exit.

## Why This Exists
Sometimes a quick, terminal-based file browser is all you need. Goranger keeps it simple while remaining functional.

## License
This project is licensed under the [CC0 1.0 Universal License](https://creativecommons.org/publicdomain/zero/1.0/). This means it has been dedicated to the public domain, and you are free to use, copy, modify, and distribute it without any restrictions.
