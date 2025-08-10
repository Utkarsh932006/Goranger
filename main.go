// gobrowse - advanced Go TUI file browser
// Single-file implementation (main.go)
// Features:
// - Dual-pane TUI using tview (file list + preview)
// - Navigation (Enter, Backspace), bookmarks, search/filter
// - File operations: open (with system default), delete, rename, copy, move
// - Async text preview with size limit
// - Status bar and help modal
// - Configurable keybindings (easy to change at top)
//
// Usage:
//   go mod init gobrowse
//   go get github.com/rivo/tview github.com/gdamore/tcell/v2
//   go run main.go

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// -----------------------------
// Config / Keybindings
// -----------------------------
var (
	PreviewMaxBytes  = 200 * 1024 // 200 KB
	TextPreviewLines = 1000

	KeyOpen     = 'o' // open with system default
	KeyDelete   = 'd'
	KeyRename   = 'r'
	KeyCopy     = 'c'
	KeyMove     = 'm'
	KeyBookmark = 'b'
	KeyListBook = 'B'
	KeySearch   = '/'
	KeyHelp     = 'h'
	KeyQuit     = 'q'
)

// -----------------------------
// App State
// -----------------------------

type AppState struct {
	app        *tview.Application
	filesList  *tview.List
	preview    *tview.TextView
	status     *tview.TextView
	currentDir string
	files      []fs.DirEntry
	lock       sync.Mutex
	bookmarks  []string
	searchTerm string
}

// -----------------------------
// Helpers
// -----------------------------

func humanSize(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	kb := float64(n) / 1024.0
	if kb < 1024 {
		return fmt.Sprintf("%.1f KB", kb)
	}
	mb := kb / 1024.0
	if mb < 1024 {
		return fmt.Sprintf("%.1f MB", mb)
	}
	gb := mb / 1024.0
	return fmt.Sprintf("%.1f GB", gb)
}

func isTextFile(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	textExt := map[string]bool{
		".txt": true, ".md": true, ".go": true, ".py": true, ".java": true, ".c": true, ".cpp": true, ".json": true, ".yaml": true, ".yml": true, ".xml": true, ".html": true, ".css": true, ".js": true, ".sh": true}
	return textExt[ext]
}

func systemOpen(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("cmd", "/C", "start", "", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	return cmd.Start()
}

// -----------------------------
// App Methods
// -----------------------------

func NewAppState() (*AppState, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	state := &AppState{
		app:        tview.NewApplication(),
		filesList:  tview.NewList().ShowSecondaryText(false),
		preview:    tview.NewTextView().SetDynamicColors(true).SetWrap(true).SetChangedFunc(func() { state.app.Draw() }),
		status:     tview.NewTextView().SetDynamicColors(true),
		currentDir: cwd,
		bookmarks:  make([]string, 0),
	}
	return state, nil
}

func (s *AppState) loadFiles() error {
	s.lock.Lock()
	defer s.lock.Unlock()

	entries, err := os.ReadDir(s.currentDir)
	if err != nil {
		return err
	}

	s.files = entries
	s.sortFiles()
	return nil
}

func (s *AppState) sortFiles() {
	s.lock.Lock()
	defer s.lock.Unlock()

	slice := make([]fs.DirEntry, 0, len(s.files))
	for _, e := range s.files {
		slice = append(slice, e)
	}
	sort.Slice(slice, func(i, j int) bool {
		a, b := slice[i], slice[j]
		// directories first
		if a.IsDir() && !b.IsDir() {
			return true
		}
		if !a.IsDir() && b.IsDir() {
			return false
		}
		return strings.ToLower(a.Name()) < strings.ToLower(b.Name())
	})
	s.files = slice
}

func (s *AppState) refreshList() {
	_ = s.loadFiles()

	s.app.QueueUpdateDraw(func() {
		s.filesList.Clear()
		// optionally filter by searchTerm
		for _, e := range s.files {
			name := e.Name()
			if s.searchTerm != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(s.searchTerm)) {
				continue
			}
			label := name
			if e.IsDir() {
				label = "[::b][DIR] " + name
			}
			// capture for closure
			entry := e
			s.filesList.AddItem(label, "", 0, func() {
				s.onEnter(entry)
			})
		}
		// add go back entry
		if parent := filepath.Dir(s.currentDir); parent != s.currentDir {
			s.filesList.AddItem("[..] Go up", "", 0, func() {
				s.changeDir(filepath.Dir(s.currentDir))
			})
		}
		// set default selection to first
		if s.filesList.GetItemCount() > 0 {
			s.filesList.SetCurrentItem(0)
		}
		// update status
		s.updateStatus("Ready")
	})
}

func (s *AppState) changeDir(dir string) {
	abs, _ := filepath.Abs(dir)
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		s.showModal("Not a directory: "+dir, []string{"OK"}, func(_ int, _ string) {})
		return
	}
	s.currentDir = abs
	s.searchTerm = ""
	s.refreshList()
	s.loadPreviewForSelection()
}

func (s *AppState) onEnter(entry fs.DirEntry) {
	if entry.IsDir() {
		s.changeDir(filepath.Join(s.currentDir, entry.Name()))
		return
	}
	// file: preview or open
	s.openPreview(filepath.Join(s.currentDir, entry.Name()))
}

func (s *AppState) openPreview(path string) {
	// open in system default if small binary? we provide both options. Default: preview if text
	if isTextFile(path) {
		s.loadTextPreview(path)
	} else {
		s.preview.Clear()
		s.preview.SetText("(No text preview available. Press 'o' to open with system default.)")
	}
}

func (s *AppState) loadPreviewForSelection() {
	index := s.filesList.GetCurrentItem()
	if index < 0 || index >= s.filesList.GetItemCount() {
		s.preview.Clear()
		return
	}
	label, _ := s.filesList.GetItemText(index)
	// strip dir tag if present
	name := strings.TrimPrefix(label, "[::b][DIR] ")
	if name == "[..] Go up" {
		s.preview.SetText("")
		return
	}
	path := filepath.Join(s.currentDir, name)
	// if dir do nothing
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		s.preview.SetText("[DIR] " + name)
		return
	}
	if isTextFile(path) {
		go s.loadTextPreview(path)
	} else {
		// show file metadata
		if info, err := os.Stat(path); err == nil {
			s.preview.SetText(fmt.Sprintf("%s\nSize: %s\nModified: %s", name, humanSize(info.Size()), info.ModTime().Format(time.RFC1123)))
		} else {
			s.preview.SetText("(Unable to stat file)")
		}
	}
}

func (s *AppState) loadTextPreview(path string) {
	s.app.QueueUpdateDraw(func() { s.preview.SetText("Loading preview...") })

	f, err := os.Open(path)
	if err != nil {
		s.app.QueueUpdateDraw(func() { s.preview.SetText("Error opening file: " + err.Error()) })
		return
	}
	defer f.Close()

	var buf bytes.Buffer
	reader := bufio.NewReader(f)
	// Read up to PreviewMaxBytes
	n := 0
	for n < PreviewMaxBytes {
		line, err := reader.ReadString('\n')
		buf.WriteString(line)
		n += len(line)
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			break
		}
		// stop if too many lines
		if strings.Count(buf.String(), "\n") > TextPreviewLines {
			break
		}
	}

	text := buf.String()
	if len(text) == PreviewMaxBytes {
		text += "\n... (truncated)"
	}

	s.app.QueueUpdateDraw(func() {
		s.preview.SetText(text)
	})
}

func (s *AppState) updateStatus(msg string) {
	s.app.QueueUpdateDraw(func() {
		s.status.SetText(fmt.Sprintf("[yellow]Dir:[-] %s  [green]|[-] %s", s.currentDir, msg))
	})
}

func (s *AppState) showModal(message string, buttons []string, done func(int, string)) {
	modal := tview.NewModal().SetText(message).AddButtons(buttons).SetDoneFunc(done)
	// push modal
	root := s.app.GetRoot()
	flex := tview.NewFlex().SetDirection(tview.FlexRow)
	flex.AddItem(root, 0, 1, false)
	flex.AddItem(modal, 0, 1, true)
	_ = s.app.SetRoot(flex, true)
	// when modal closed restore layout handled by done
}

// File operations

func (s *AppState) askInput(title, label, initial string, done func(text string, ok bool)) {
	form := tview.NewForm()
	input := tview.NewInputField().SetLabel(label).SetText(initial)
	form.AddFormItem(input)
	form.AddButton("OK", func() {
		text := input.GetText()
		_ = s.app.SetRoot(s.layout(), true)
		done(text, true)
	})
	form.AddButton("Cancel", func() {
		_ = s.app.SetRoot(s.layout(), true)
		done("", false)
	})
	form.SetBorder(true).SetTitle(title)
	_ = s.app.SetRoot(form, true)
}

func (s *AppState) confirm(message string, done func(bool)) {
	modal := tview.NewModal().SetText(message).AddButtons([]string{"Yes", "No"}).SetDoneFunc(func(index int, label string) {
		_ = s.app.SetRoot(s.layout(), true)
		done(label == "Yes")
	})
	_ = s.app.SetRoot(modal, true)
}

func (s *AppState) deleteSelection() {
	idx := s.filesList.GetCurrentItem()
	if idx < 0 {
		return
	}
	label, _ := s.filesList.GetItemText(idx)
	name := strings.TrimPrefix(label, "[::b][DIR] ")
	path := filepath.Join(s.currentDir, name)
	// confirm
	s.confirm("Delete '"+name+"'? This cannot be undone.", func(ok bool) {
		if !ok {
			return
		}
		err := os.RemoveAll(path)
		if err != nil {
			s.showModal("Delete failed: "+err.Error(), []string{"OK"}, func(_ int, _ string) {})
			return
		}
		s.updateStatus("Deleted: " + name)
		s.refreshList()
	})
}

func (s *AppState) renameSelection() {
	idx := s.filesList.GetCurrentItem()
	if idx < 0 {
		return
	}
	label, _ := s.filesList.GetItemText(idx)
	name := strings.TrimPrefix(label, "[::b][DIR] ")
	old := filepath.Join(s.currentDir, name)
	initial := name
	s.askInput("Rename", "New name:", initial, func(text string, ok bool) {
		if !ok || strings.TrimSpace(text) == "" {
			return
		}
		newPath := filepath.Join(s.currentDir, text)
		err := os.Rename(old, newPath)
		if err != nil {
			s.showModal("Rename failed: "+err.Error(), []string{"OK"}, func(_ int, _ string) {})
			return
		}
		s.updateStatus("Renamed to: " + text)
		s.refreshList()
	})
}

func (s *AppState) copySelection() {
	idx := s.filesList.GetCurrentItem()
	if idx < 0 {
		return
	}
	label, _ := s.filesList.GetItemText(idx)
	name := strings.TrimPrefix(label, "[::b][DIR] ")
	s.askInput("Copy to", "Destination path:", filepath.Join(s.currentDir, name+".copy"), func(text string, ok bool) {
		if !ok || strings.TrimSpace(text) == "" {
			return
		}
		s.updateStatus("Copying...")
		err := copyPath(filepath.Join(s.currentDir, name), text)
		if err != nil {
			s.showModal("Copy failed: "+err.Error(), []string{"OK"}, func(_ int, _ string) {})
			return
		}
		s.updateStatus("Copied to: " + text)
		s.refreshList()
	})
}

func (s *AppState) moveSelection() {
	idx := s.filesList.GetCurrentItem()
	if idx < 0 {
		return
	}
	label, _ := s.filesList.GetItemText(idx)
	name := strings.TrimPrefix(label, "[::b][DIR] ")
	old := filepath.Join(s.currentDir, name)
	s.askInput("Move to", "Destination path:", filepath.Join(s.currentDir, name), func(text string, ok bool) {
		if !ok || strings.TrimSpace(text) == "" {
			return
		}
		err := os.Rename(old, text)
		if err != nil {
			s.showModal("Move failed: "+err.Error(), []string{"OK"}, func(_ int, _ string) {})
			return
		}
		s.updateStatus("Moved to: " + text)
		s.refreshList()
	})
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		// copy directory recursively
		return copyDir(src, dst)
	}
	// copy file
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			if err := copyPath(srcPath, dstPath); err != nil {
				return err
			}
		}
	}
	return nil
}

// Bookmarks

func (s *AppState) toggleBookmark() {
	for i, b := range s.bookmarks {
		if b == s.currentDir {
			// remove
			s.bookmarks = append(s.bookmarks[:i], s.bookmarks[i+1:]...)
			s.updateStatus("Removed bookmark")
			return
		}
	}
	s.bookmarks = append(s.bookmarks, s.currentDir)
	s.updateStatus("Bookmarked")
}

func (s *AppState) listBookmarks() {
	if len(s.bookmarks) == 0 {
		s.showModal("No bookmarks set", []string{"OK"}, func(_ int, _ string) {})
		return
	}
	list := tview.NewList()
	for _, b := range s.bookmarks {
		bb := b
		list.AddItem(b, "", 0, func() { s.changeDir(bb) })
	}
	list.SetDoneFunc(func() { _ = s.app.SetRoot(s.layout(), true) })
	list.SetBorder(true).SetTitle("Bookmarks")
	_ = s.app.SetRoot(list, true)
}

// Search

func (s *AppState) promptSearch() {
	s.askInput("Search", "Filter filenames:", s.searchTerm, func(text string, ok bool) {
		if !ok {
			return
		}
		s.searchTerm = text
		s.refreshList()
	})
}

// Help

func (s *AppState) showHelp() {
	help := `[::b]Keys[-]

Up/Down - Navigate
Enter - Open directory / preview file
Backspace - Go up
` + fmt.Sprintf("'%c' - Open with system default\n'%c' - Delete\n'%c' - Rename\n'%c' - Copy\n'%c' - Move\n'%c' - Bookmark toggle\n'%c' - List bookmarks\n'%c' - Search\n'%c' - Help\n'%c' - Quit\n",
		KeyOpen, KeyDelete, KeyRename, KeyCopy, KeyMove, KeyBookmark, KeyListBook, KeySearch, KeyHelp, KeyQuit)

	s.showModal(help, []string{"OK"}, func(_ int, _ string) { _ = s.app.SetRoot(s.layout(), true) })
}

// Layout

func (s *AppState) layout() tview.Primitive {
	// left: files list
	left := tview.NewFlex().SetDirection(tview.FlexRow)
	left.AddItem(s.filesList, 0, 1, true)
	left.SetBorder(true).SetTitle("Files")

	// right: preview
	right := tview.NewFlex().SetDirection(tview.FlexRow)
	right.AddItem(s.preview, 0, 1, false)
	right.SetBorder(true).SetTitle("Preview")

	// main flex
	main := tview.NewFlex().SetDirection(tview.FlexColumn)
	main.AddItem(left, 0, 3, true)
	main.AddItem(right, 0, 5, false)

	// footer
	footer := tview.NewFlex().SetDirection(tview.FlexColumn)
	footer.AddItem(s.status, 0, 1, false)
	footer.SetBorder(true)

	root := tview.NewFlex().SetDirection(tview.FlexRow)
	root.AddItem(main, 0, 1, true)
	root.AddItem(footer, 1, 0, false)
	return root
}

// Key handlers

func (s *AppState) setupKeys() {
	s.filesList.SetSelectedFunc(func(idx int, mainText string, secondaryText string, shortcut rune) {
		// open on enter
		// emulate pressing Enter: call onEnter for that item
		if idx < 0 || idx >= s.filesList.GetItemCount() {
			return
		}
		label, _ := s.filesList.GetItemText(idx)
		name := strings.TrimPrefix(label, "[::b][DIR] ")
		if name == "[..] Go up" {
			s.changeDir(filepath.Dir(s.currentDir))
			return
		}
		path := filepath.Join(s.currentDir, name)
		info, err := os.Stat(path)
		if err == nil && info.IsDir() {
			s.changeDir(path)
		} else {
			s.openPreview(path)
		}
	})

	s.app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Rune() {
		case KeyQuit:
			s.app.Stop()
		case KeyOpen:
			// open selected
			idx := s.filesList.GetCurrentItem()
			if idx < 0 {
				break
			}
			label, _ := s.filesList.GetItemText(idx)
			name := strings.TrimPrefix(label, "[::b][DIR] ")
			path := filepath.Join(s.currentDir, name)
			_ = systemOpen(path)
		case KeyDelete:
			s.deleteSelection()
		case KeyRename:
			s.renameSelection()
		case KeyCopy:
			s.copySelection()
		case KeyMove:
			s.moveSelection()
		case KeyBookmark:
			s.toggleBookmark()
		case KeyListBook:
			s.listBookmarks()
		case KeySearch:
			s.promptSearch()
		case KeyHelp:
			s.showHelp()
		}
		// navigation keys
		switch event.Key() {
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			s.changeDir(filepath.Dir(s.currentDir))
		case tcell.KeyEsc:
			s.app.Stop()
		case tcell.KeyUp, tcell.KeyDown:
			// let the list handle
		}
		// on any key, update preview after a short delay for selection changes
		go func() {
			time.Sleep(50 * time.Millisecond)
			s.loadPreviewForSelection()
		}()
		return event
	})
}

// -----------------------------
// Main
// -----------------------------

func main() {
	state, err := NewAppState()
	if err != nil {
		fmt.Println("Error creating app:", err)
		return
	}

	if err := state.loadFiles(); err != nil {
		fmt.Println("Error reading directory:", err)
		return
	}

	state.refreshList()
	state.updateStatus("Ready")
	state.setupKeys()

	root := state.layout()
	state.app.SetRoot(root, true).EnableMouse(true)

	// handle resize by redrawing preview
	state.preview.SetChangedFunc(func() { state.app.Draw() })

	if err := state.app.Run(); err != nil {
		fmt.Println("Error running app:", err)
	}
}
