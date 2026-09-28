//go:build windows

package windows

func WindowsConnectionAdapters() []RemoteConnectionAdapter {
	return WindowsConnectionAdaptersWithOpenAISetup(OpenAISetupConfig{})
}

func WindowsConnectionAdaptersWithOpenAISetup(setup OpenAISetupConfig) []RemoteConnectionAdapter {
	return []RemoteConnectionAdapter{NewWindowsOpenAIAdapter(setup)}
}
