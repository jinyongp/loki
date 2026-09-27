//go:build windows

package windows

func NewWindowsOperatorClient() OperatorClient {
	runner := ExecNativeRunner{}
	return OperatorClient{WSL: WSLClient{Runner: runner}}
}

func NewWindowsPreflightCollector() PreflightCollector {
	runner := ExecNativeRunner{}
	return PreflightCollector{
		Filesystem: OSStateFilesystem{},
		Tasks:      PowerShellStartupTaskSource{},
		WSL:        WSLClient{Runner: runner},
	}
}

func NewWindowsReplicaSynchronizer(reconciler ConnectionRuntimeReconciler) ReplicaSynchronizer {
	operator := NewWindowsOperatorClient()
	return ReplicaSynchronizer{
		Source:     WSLLiveReplicaSource{Operator: operator},
		Store:      NewWindowsReplicaStore(),
		Reconciler: reconciler,
	}
}

func NewWindowsUninstallController(connections LocalConnectionRemover) UninstallController {
	if connections == nil {
		connections = WindowsConnectionRemovalGuard{}
	}
	runner := ExecNativeRunner{}
	wsl := WSLClient{Runner: runner}
	tasks := PowerShellStartupTaskSource{}
	filesystem := OSStateFilesystem{}
	collector := PreflightCollector{Filesystem: filesystem, Tasks: tasks, WSL: wsl}
	return UninstallController{Platform: UninstallAdapter{
		Collector:   collector,
		Tasks:       tasks,
		Filesystem:  filesystem,
		Remover:     OSPathRemover{},
		Connections: connections,
	}}
}
