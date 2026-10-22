package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

var flowPlaceholderReference = regexp.MustCompile(`(^|[^A-Za-z0-9_.+-])@([A-Za-z_][A-Za-z0-9_-]*)`)
var flowPlaceholderDefinition = regexp.MustCompile(`^\s*@([A-Za-z_][A-Za-z0-9_-]*)\s*=\s*(.*?)\s*$`)

var flowCommands = []string{
	"serve", "set-url", "open", "click", "fill", "dropdown", "choose",
	"list-selectors", "page-contains", "random-sleep", "help",
}

func newFlowID() string {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(suffix[:]))
}

func flowRunsDir(flowID string) string {
	return filepath.Join(appDataDir(), "flows", flowID, "runs")
}

func pruneFlowRunLogs(flowID string, keep int) error {
	if keep < 1 {
		keep = 1
	}
	entries, err := os.ReadDir(flowRunsDir(flowID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var files []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			files = append(files, entry)
		}
	}
	if len(files) <= keep {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name() > files[j].Name() })
	for _, entry := range files[keep:] {
		if err := os.Remove(filepath.Join(flowRunsDir(flowID), entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func latestFlowRunLog(flowID string) string {
	entries, err := os.ReadDir(flowRunsDir(flowID))
	if err != nil {
		return ""
	}
	for index := len(entries) - 1; index >= 0; index-- {
		entry := entries[index]
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(flowRunsDir(flowID), entry.Name()))
		if err == nil {
			return string(data)
		}
	}
	return ""
}

func flowRunLogFiles(flowID string) []os.DirEntry {
	entries, err := os.ReadDir(flowRunsDir(flowID))
	if err != nil {
		return nil
	}
	var logs []os.DirEntry
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".log") {
			logs = append(logs, entry)
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].Name() > logs[j].Name() })
	return logs
}

func newFlowRunLogsPage(flowID, flowName string, onBack func()) fyne.CanvasObject {
	logs := flowRunLogFiles(flowID)
	header := container.NewHBox(widget.NewButton("Back to flows", onBack), layout.NewSpacer())
	if len(logs) == 0 {
		return container.NewBorder(header, nil, nil, nil,
			widget.NewCard("Run logs — "+flowName, "This flow has no saved run logs yet.", widget.NewLabel("Run the flow to create its first log.")),
		)
	}
	options := make([]string, len(logs))
	byOption := make(map[string]string, len(logs))
	for index, entry := range logs {
		label := "Run " + strings.TrimSuffix(entry.Name(), ".log")
		if index == 0 {
			label = "Latest — " + label
		} else if index == 1 {
			label = "Previous — " + label
		}
		options[index] = label
		byOption[label] = filepath.Join(flowRunsDir(flowID), entry.Name())
	}
	logText := widget.NewMultiLineEntry()
	logText.Disable()
	logText.Wrapping = fyne.TextWrapWord
	selectRun := widget.NewSelect(options, func(selected string) {
		data, err := os.ReadFile(byOption[selected])
		if err != nil {
			logText.SetText("Could not read this run log: " + err.Error())
			return
		}
		logText.SetText(string(data))
	})
	selectRun.SetSelected(options[0])
	content := container.NewBorder(selectRun, nil, nil, nil, container.NewScroll(logText))
	logCard := widget.NewCard("Run logs — "+flowName, "Choose the latest, previous, or another retained flow run.", content)
	return container.NewBorder(header, nil, nil, nil, logCard)
}

func parseFlowPlaceholders(text string) (map[string]string, error) {
	placeholders := make(map[string]string)
	for lineNumber, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		match := flowPlaceholderDefinition.FindStringSubmatch(line)
		if len(match) != 3 {
			return nil, fmt.Errorf("placeholder line %d must look like @name=value", lineNumber+1)
		}
		rawValue := strings.TrimSpace(match[2])
		value := rawValue
		if strings.HasPrefix(rawValue, "\"") {
			var err error
			value, err = strconv.Unquote(rawValue)
			if err != nil {
				return nil, fmt.Errorf("placeholder line %d has an invalid quoted value: %w", lineNumber+1, err)
			}
		}
		placeholders[match[1]] = value
	}
	return placeholders, nil
}

func formatFlowPlaceholders(placeholders map[string]string) string {
	keys := make([]string, 0, len(placeholders))
	for key := range placeholders {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, "@"+key+"="+strconv.Quote(placeholders[key]))
	}
	return strings.Join(lines, "\n")
}
