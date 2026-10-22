// Package flow implements autoipo's finite-state automation flows: the graph
// model persisted in .data/flows.json, placeholder substitution, the
// qualifier expression language, and the execution engine.
//
// The package has no UI dependencies so the desktop app and the Telegram
// daemon share one implementation.
package flow

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"autoipo/internal/bridge"
)

// Node types.
const (
	TypeStart     = "start"
	TypeCommand   = "command"
	TypeQualifier = "qualifier"
	TypeSuccess   = "success"
	TypeTerminal  = "terminal"
)

// Local (non-bridge) commands understood by the engine.
const (
	CommandServe       = "serve"
	CommandRandomSleep = "random-sleep"
)

// DefaultRunsToKeep is the default run-log retention per flow.
const DefaultRunsToKeep = 10

// Commands lists every command a command node may run, in display order.
var Commands = append([]string{CommandServe}, append(append([]string(nil), bridge.CLICommands...), CommandRandomSleep)...)

// CommandArgCount returns the number of arguments a flow command takes.
func CommandArgCount(name string) int {
	switch name {
	case CommandServe:
		return 0
	case CommandRandomSleep:
		return 1
	}
	count, _ := bridge.CommandArgCount(name)
	return count
}

// KnownCommand reports whether name is a valid flow command.
func KnownCommand(name string) bool {
	if name == CommandServe || name == CommandRandomSleep {
		return true
	}
	_, ok := bridge.CommandArgCount(name)
	return ok
}

// ArgPlaceholders returns UI hints for a command's arguments.
func ArgPlaceholders(name string) []string {
	hints := bridge.ArgPlaceholders(name)
	for i := range hints {
		hints[i] += " or @name"
	}
	return hints
}

// Flow is a saved automation graph.
type Flow struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	RunsToKeep   int               `json:"runs_to_keep"`
	Placeholders map[string]string `json:"placeholders,omitempty"` // legacy; migrated into the start node
	StartID      string            `json:"start_id"`
	SuccessID    string            `json:"success_id"`
	TerminalID   string            `json:"terminal_id"`
	Nodes        []Node            `json:"nodes"`
	UpdatedAt    time.Time         `json:"updated_at,omitempty"`
}

// Node is one state in the graph.
type Node struct {
	ID              string            `json:"id"`
	Type            string            `json:"type"`
	Name            string            `json:"name,omitempty"`
	Command         string            `json:"command,omitempty"`
	Args            []string          `json:"args,omitempty"`
	SaveAs          string            `json:"save_as,omitempty"` // store command output as @SaveAs
	NextID          string            `json:"next_id,omitempty"`
	Expression      string            `json:"expression,omitempty"`
	Retries         int               `json:"retries,omitempty"`
	SuccessID       string            `json:"success_id,omitempty"`
	FallbackID      string            `json:"fallback_id,omitempty"`
	Placeholders    map[string]string `json:"placeholders,omitempty"`
	PlaceholderText string            `json:"placeholder_text,omitempty"`
}

// NewID returns a unique identifier for flows, nodes and runs. IDs sort
// chronologically.
func NewID() string {
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(suffix[:]))
}

// New creates a flow with the three mandatory states.
func New(name string) Flow {
	start, success, terminal := NewID(), NewID(), NewID()
	return Flow{
		ID:         NewID(),
		Name:       name,
		RunsToKeep: DefaultRunsToKeep,
		StartID:    start,
		SuccessID:  success,
		TerminalID: terminal,
		Nodes: []Node{
			{ID: start, Type: TypeStart, Name: "Start", Placeholders: map[string]string{}},
			{ID: success, Type: TypeSuccess, Name: "Success"},
			{ID: terminal, Type: TypeTerminal, Name: "Terminal"},
		},
	}
}

// NewCommandNode returns a fresh command node with a default command.
func NewCommandNode() Node {
	return Node{ID: NewID(), Type: TypeCommand, Name: "Command", Command: "open"}
}

// Validate checks the structural invariants every saved flow must satisfy.
func (f Flow) Validate() error {
	if f.StartID == "" || f.SuccessID == "" || f.TerminalID == "" {
		return fmt.Errorf("Start, Success, and Terminal states are required")
	}
	types := make(map[string]string, len(f.Nodes))
	counts := map[string]int{}
	for _, node := range f.Nodes {
		if node.ID == "" {
			return fmt.Errorf("every state needs a unique ID")
		}
		if _, exists := types[node.ID]; exists {
			return fmt.Errorf("duplicate state ID %q", node.ID)
		}
		switch node.Type {
		case TypeStart, TypeSuccess, TypeTerminal:
			counts[node.Type]++
		case TypeCommand:
			if node.Command != "" && !KnownCommand(node.Command) {
				return fmt.Errorf("state %q uses unknown command %q", node.ID, node.Command)
			}
		case TypeQualifier:
		default:
			return fmt.Errorf("state %q has unknown type %q", node.ID, node.Type)
		}
		types[node.ID] = node.Type
	}
	if counts[TypeStart] != 1 || counts[TypeSuccess] != 1 || counts[TypeTerminal] != 1 {
		return fmt.Errorf("flow must have exactly one Start, Success, and Terminal state")
	}
	for _, required := range []struct{ id, kind string }{{f.StartID, TypeStart}, {f.SuccessID, TypeSuccess}, {f.TerminalID, TypeTerminal}} {
		if types[required.id] != required.kind {
			return fmt.Errorf("flow must contain its required %s state", required.kind)
		}
	}
	return nil
}

// Issue is a non-fatal problem reported by Lint.
type Issue struct {
	NodeID  string
	Message string
}

// Lint reports unreachable states, dangling transitions, invalid expressions
// and missing arguments. Flows with issues can still be saved.
func (f Flow) Lint() []Issue {
	var issues []Issue
	index := f.index()
	if start, ok := index[f.StartID]; ok && start.NextID == "" {
		issues = append(issues, Issue{NodeID: f.StartID, Message: "Start has no next state; the flow ends at Terminal immediately"})
	}
	reachable := f.reachable()
	for _, node := range f.Nodes {
		if !reachable[node.ID] && node.Type != TypeSuccess && node.Type != TypeTerminal {
			issues = append(issues, Issue{NodeID: node.ID, Message: "state is unreachable from Start"})
		}
		for _, target := range []struct{ label, id string }{{"next", node.NextID}, {"true", node.SuccessID}, {"false", node.FallbackID}} {
			if target.id != "" {
				if _, ok := index[target.id]; !ok {
					issues = append(issues, Issue{NodeID: node.ID, Message: target.label + " target points to a missing state"})
				}
			}
		}
		switch node.Type {
		case TypeCommand:
			if node.Command == "" {
				issues = append(issues, Issue{NodeID: node.ID, Message: "command is not set"})
				break
			}
			want := CommandArgCount(node.Command)
			for i := 0; i < want; i++ {
				if i >= len(node.Args) || strings.TrimSpace(node.Args[i]) == "" {
					issues = append(issues, Issue{NodeID: node.ID, Message: fmt.Sprintf("%s needs argument %d", node.Command, i+1)})
					break
				}
			}
			if node.NextID == "" {
				issues = append(issues, Issue{NodeID: node.ID, Message: "no next state; success routes to Terminal"})
			}
		case TypeQualifier:
			if _, err := ParseExpression(node.Expression); err != nil {
				issues = append(issues, Issue{NodeID: node.ID, Message: "invalid expression: " + err.Error()})
			}
			if node.SuccessID == "" {
				issues = append(issues, Issue{NodeID: node.ID, Message: "no true target; routes to Terminal"})
			}
			if node.FallbackID == "" {
				issues = append(issues, Issue{NodeID: node.ID, Message: "no false target; routes to Terminal"})
			}
		}
	}
	return issues
}

func (f Flow) index() map[string]Node {
	index := make(map[string]Node, len(f.Nodes))
	for _, node := range f.Nodes {
		index[node.ID] = node
	}
	return index
}

// NodeIndex returns the position of id in Nodes, or -1.
func (f *Flow) NodeIndex(id string) int {
	if f == nil {
		return -1
	}
	for i := range f.Nodes {
		if f.Nodes[i].ID == id {
			return i
		}
	}
	return -1
}

// Node returns the node with id.
func (f *Flow) Node(id string) (Node, bool) {
	if i := f.NodeIndex(id); i >= 0 {
		return f.Nodes[i], true
	}
	return Node{}, false
}

// StartNode returns the start node.
func (f *Flow) StartNode() *Node {
	if i := f.NodeIndex(f.StartID); i >= 0 {
		return &f.Nodes[i]
	}
	return nil
}

// Label renders a node for lists and the graph canvas.
func (n Node) Label() string {
	name := strings.TrimSpace(n.Name)
	if name == "" {
		name = n.Type
	}
	if n.Type == TypeCommand && n.Command != "" {
		name += " · " + n.Command
	}
	return name + "  [" + n.ShortID() + "]"
}

// ShortID is the first 8 characters of the node ID.
func (n Node) ShortID() string {
	if len(n.ID) > 8 {
		return n.ID[:8]
	}
	return n.ID
}

// IsFixed reports whether the node is one of the mandatory states.
func (n Node) IsFixed() bool {
	return n.Type == TypeStart || n.Type == TypeSuccess || n.Type == TypeTerminal
}

// Targets returns the outgoing edges with their labels. Empty targets are
// resolved to the terminal state and labelled "default".
type Edge struct {
	To      string
	Label   string // "Next", "True", "False" or "default"
	Default bool
}

// Edges returns the outgoing transitions of node within f.
func (f Flow) Edges(node Node) []Edge {
	resolve := func(id, label string) Edge {
		if id == "" {
			return Edge{To: f.TerminalID, Label: "default", Default: true}
		}
		return Edge{To: id, Label: label}
	}
	switch node.Type {
	case TypeStart, TypeCommand:
		return []Edge{resolve(node.NextID, "Next")}
	case TypeQualifier:
		return []Edge{resolve(node.SuccessID, "True"), resolve(node.FallbackID, "False")}
	default:
		return nil
	}
}

// TargetLabel renders a transition target for summaries.
func (f Flow) TargetLabel(id string) string {
	if id == "" {
		return "(terminal by default)"
	}
	node, ok := f.Node(id)
	if !ok {
		return "(terminal by default)"
	}
	return node.Label()
}

// TransitionSummary renders one node's transitions.
func (f Flow) TransitionSummary(node Node) string {
	switch node.Type {
	case TypeStart, TypeCommand:
		return node.Label() + " → " + f.TargetLabel(node.NextID)
	case TypeQualifier:
		return node.Label() + " → true: " + f.TargetLabel(node.SuccessID) + " · false after retries: " + f.TargetLabel(node.FallbackID)
	default:
		return node.Label()
	}
}

// reachable returns the set of node IDs reachable from Start.
func (f Flow) reachable() map[string]bool {
	index := f.index()
	seen := map[string]bool{}
	queue := []string{f.StartID}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		node, ok := index[id]
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		for _, edge := range f.Edges(node) {
			queue = append(queue, edge.To)
		}
	}
	return seen
}

// Layers groups nodes by breadth-first distance from Start; nodes that are
// not reachable are appended as their own trailing layers. Within a layer,
// nodes keep their order in Nodes.
func (f Flow) Layers() [][]Node {
	index := f.index()
	levels := map[string]int{}
	if _, ok := index[f.StartID]; ok {
		levels[f.StartID] = 0
	}
	queue := []string{f.StartID}
	maxLevel := 0
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		node, ok := index[id]
		if !ok {
			continue
		}
		for _, edge := range f.Edges(node) {
			if _, exists := index[edge.To]; !exists {
				continue
			}
			if _, seen := levels[edge.To]; seen {
				continue
			}
			levels[edge.To] = levels[id] + 1
			if levels[edge.To] > maxLevel {
				maxLevel = levels[edge.To]
			}
			queue = append(queue, edge.To)
		}
	}
	for _, node := range f.Nodes {
		if _, seen := levels[node.ID]; !seen {
			maxLevel++
			levels[node.ID] = maxLevel
		}
	}
	layers := make([][]Node, maxLevel+1)
	for _, node := range f.Nodes {
		level := levels[node.ID]
		layers[level] = append(layers[level], node)
	}
	return layers
}

// Clone returns a deep copy.
func (f Flow) Clone() Flow {
	data, err := json.Marshal(f)
	if err != nil {
		return f
	}
	var copied Flow
	if err := json.Unmarshal(data, &copied); err != nil {
		return f
	}
	return copied
}

// Duplicate returns a copy with a new flow ID, fresh node IDs and name.
func (f Flow) Duplicate(name string) Flow {
	copied := f.Clone()
	copied.ID = NewID()
	copied.Name = name
	remap := make(map[string]string, len(copied.Nodes))
	for i := range copied.Nodes {
		remap[copied.Nodes[i].ID] = NewID()
	}
	rename := func(id string) string {
		if id == "" {
			return ""
		}
		if mapped, ok := remap[id]; ok {
			return mapped
		}
		return ""
	}
	for i := range copied.Nodes {
		node := &copied.Nodes[i]
		node.ID = remap[node.ID]
		node.NextID, node.SuccessID, node.FallbackID = rename(node.NextID), rename(node.SuccessID), rename(node.FallbackID)
	}
	copied.StartID, copied.SuccessID, copied.TerminalID = remap[copied.StartID], remap[copied.SuccessID], remap[copied.TerminalID]
	return copied
}

// RemoveNode deletes a non-fixed node and re-routes incoming edges to the
// terminal default.
func (f *Flow) RemoveNode(id string) bool {
	index := f.NodeIndex(id)
	if index < 0 || f.Nodes[index].IsFixed() {
		return false
	}
	f.Nodes = append(f.Nodes[:index], f.Nodes[index+1:]...)
	for i := range f.Nodes {
		node := &f.Nodes[i]
		if node.NextID == id {
			node.NextID = ""
		}
		if node.SuccessID == id {
			node.SuccessID = ""
		}
		if node.FallbackID == id {
			node.FallbackID = ""
		}
	}
	return true
}

// InsertAfter inserts newNode on the chosen outgoing route of afterID
// ("next", "true" or "false"), inheriting the previous target.
func (f *Flow) InsertAfter(afterID, route string, newNode Node) bool {
	index := f.NodeIndex(afterID)
	if index < 0 {
		return false
	}
	var previous *string
	switch f.Nodes[index].Type {
	case TypeStart, TypeCommand:
		previous = &f.Nodes[index].NextID
	case TypeQualifier:
		if route == "false" {
			previous = &f.Nodes[index].FallbackID
		} else {
			previous = &f.Nodes[index].SuccessID
		}
	default:
		return false
	}
	newNode.NextID = *previous
	*previous = newNode.ID
	f.Nodes = append(f.Nodes, Node{})
	copy(f.Nodes[index+2:], f.Nodes[index+1:])
	f.Nodes[index+1] = newNode
	return true
}

// SortedNames returns flow names in display order.
func SortedNames(flows []Flow) []string {
	names := make([]string, 0, len(flows))
	for _, f := range flows {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	return names
}
