package management

import (
	"fmt"
	"runtime"

	"loki/internal/tools"
)

// ConfigureMode selects the acquisition/runtime contract before installing
// tools. Changing a populated host requires removing its owned installations;
// retained user/provider data are independent of this setting.
func (s Store) ConfigureMode(mode tools.Mode) error {
	if mode != tools.ProjectHost && mode != tools.Full {
		return fmt.Errorf("runtime mode must be project-host or full")
	}
	if mode == tools.Full && runtime.GOOS != "linux" {
		return fmt.Errorf("full mode runs on a Linux execution host; select an explicit WSL or SSH host")
	}
	unlock, err := s.Lock()
	if err != nil {
		return err
	}
	defer unlock()
	if err := s.RequireMutable(); err != nil {
		return err
	}
	state, err := s.Load()
	if err != nil {
		return err
	}
	if state.Config.Mode == mode {
		return s.Save(state)
	}
	if len(state.Installed) != 0 {
		return fmt.Errorf("remove installed tools before changing runtime mode; retained tool data will remain")
	}
	state.Config.Mode = mode
	return s.Save(state)
}
