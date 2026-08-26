package tui

import (
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"omniharness/internal/config"
	"omniharness/internal/runtime"
)

// Run launches the cockpit and blocks until it exits. The runtime is built by
// the caller (so the CLI can run pre-flight steps like interactive auth); pass
// nil to build one from cfg here. configPath is where a picked stack is
// persisted (the config file the CLI loaded); pass "" to keep it in-memory.
func Run(cfg config.Config, rt *runtime.Runtime, configPath string) error {
	if rt == nil {
		var err error
		rt, err = runtime.New(cfg, runtime.Options{})
		if err != nil {
			return fmt.Errorf("runtime: %w", err)
		}
	}
	defer rt.Close()

	model := New(cfg, rt, configPath)
	prog := tea.NewProgram(model, tea.WithAltScreen())
	model.program = prog

	// The TUI is the human: approvals flow through it.
	rt.SetApprover(&approvalApprover{model: model})

	// Persistent event subscription: forward every bus event into the program.
	// prog.Send is safe from other goroutines.
	ch, cancel := rt.Bus.Subscribe(1024)
	go func() {
		for e := range ch {
			prog.Send(eventMsg{E: e})
		}
	}()
	defer cancel()

	if _, err := prog.Run(); err != nil {
		return err
	}
	return nil
}
